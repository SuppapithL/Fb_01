package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"main/db"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

var (
	dbconn     *sql.DB // Ensures safe concurrent access
	s3Client   *s3.Client
	bucket     string
	queries    *db.Queries // Add queries variable
	mu         sync.Mutex
	userUpload = make(map[string]string) // maps userID -> filename
)

// Config holds the application's configuration.
type Config struct {
	VerifyToken     string
	PageAccessToken string
	Port            string
	APIVersion      string
}

// WebhookRequest represents the incoming JSON payload from Facebook.
type WebhookRequest struct {
	Object string `json:"object"`
	Entry  []struct {
		Messaging []struct {
			Sender struct {
				ID string `json:"id"`
			} `json:"sender"`
			Message *struct {
				Text        string `json:"text,omitempty"`
				Attachments []struct {
					Type    string `json:"type"`
					Payload struct {
						URL string `json:"url"`
					} `json:"payload"`
				} `json:"attachments,omitempty"`
			} `json:"message,omitempty"`
			Optin *struct {
				Ref string `json:"ref"`
			} `json:"optin,omitempty"`
		} `json:"messaging"`
	} `json:"entry"`
}

// APIError represents an error returned by the Facebook API.
type APIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// loadConfig loads configuration from environment variables and sets defaults.
func loadConfig() (Config, error) {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables or defaults.")
	}

	config := Config{
		VerifyToken:     os.Getenv("FB_VERIFY_TOKEN"),
		PageAccessToken: os.Getenv("FB_PAGE_ACCESS_TOKEN"),
		Port:            os.Getenv("PORT"),
		APIVersion:      os.Getenv("FB_API_VERSION"),
	}

	// Set defaults if not provided
	if config.VerifyToken == "" {
		return Config{}, errors.New("FB_VERIFY_TOKEN environment variable is required")
	}
	if config.PageAccessToken == "" {
		return Config{}, errors.New("FB_PAGE_ACCESS_TOKEN environment variable is required")
	}
	if config.Port == "" {
		config.Port = "8080"
	}
	if config.APIVersion == "" {
		config.APIVersion = "v12.0" // Default API version
	}

	return config, nil
}

// verifyWebhook handles the GET request for webhook verification.
func verifyWebhook(w http.ResponseWriter, r *http.Request, config Config) {
	log.Println("verifyWebhook called")

	mode := r.URL.Query().Get("hub.mode")
	token := r.URL.Query().Get("hub.verify_token")
	challenge := r.URL.Query().Get("hub.challenge")

	log.Printf("Received mode: %s, token: %s, challenge: %s", mode, token, challenge)

	if mode == "subscribe" && token == config.VerifyToken {
		fmt.Fprint(w, challenge)
	} else {
		http.Error(w, "Verification failed", http.StatusForbidden)
	}
}

// handleMessages handles incoming messages from Facebook.
func handleMessages(w http.ResponseWriter, r *http.Request, config Config) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var webhookReq WebhookRequest
	if err := json.Unmarshal(body, &webhookReq); err != nil {
		log.Printf("Error unmarshalling JSON: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	if webhookReq.Object != "page" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Process each messaging event.
	for _, entry := range webhookReq.Entry {
		for _, messaging := range entry.Messaging {
			senderID := messaging.Sender.ID

			// Handle text messages.
			if messaging.Message != nil && messaging.Message.Text != "" {
				messageText := messaging.Message.Text
				tokens := strings.Fields(messageText)
				if len(tokens) == 0 {
					continue
				}

				// Command is determined by the first token.
				command := strings.ToLower(tokens[0])
				var responseText string

				switch command {
				case "upload":
					var category, filename string

					switch len(tokens) {
					case 1:
						responseText = "Usage: upload [category] <filename>"
						break
					case 2:
						// Only filename provided, default category
						category = "default"
						filename = tokens[1]
					case 3:
						// Category and filename provided
						category = tokens[1]
						filename = tokens[2]
					default:
						responseText = "Usage: upload [category] <filename>"
						break
					}

					if filename == "" {
						responseText = "Error: filename cannot be empty"
						break
					}

					// Validate filename (optional, but recommended)
					if !isValidFilename(filename) {
						responseText = "Error: Invalid filename. Please use alphanumeric characters, underscores, or hyphens."
						break
					}

					timestamp := time.Now().Format("2006-01-02 15:04:05")
					if err := insertFileMetadata(senderID, filename, category, timestamp); err != nil {
						log.Printf("Error inserting metadata: %v", err)
						responseText = "Error saving file metadata."
						break
					}

					// Save the pending upload state for this user.
					mu.Lock()
					userUpload[senderID] = filename
					mu.Unlock()

					responseText = fmt.Sprintf("Send file for '%s'.", filename) // Prompt the user to send the file.
				// Other cases such as open, list, rename, delete...
				case "open":
					if len(tokens) < 2 {
						responseText = "Usage: open <filename>"
						break
					}
					filename := tokens[1]
					fileContent, err := openFile(senderID, filename)
					if err != nil {
						log.Printf("Error opening file: %v", err)
						responseText = "Error opening file."
						break
					}
					responseText = fileContent
				case "list":
					var category string
					if len(tokens) < 2 {
						category = ""
					} else {
						category = tokens[1]
					}
					files, err := listFilesFromDB(senderID, category)
					if err != nil {
						log.Println("Database query error:", err)
						responseText = "Error fetching files from database."
						break
					}
					if len(files) == 0 {
						responseText = "No files found."
						break
					}
					responseText = "Files:\n" + strings.Join(files, "\n")
				case "rename":
					if len(tokens) < 3 {
						responseText = "Usage: rename <old_filename> <new_filename>"
						break
					}
					oldFilename := tokens[1]
					newFilename := tokens[2]
					if err := renameFileInDB(senderID, oldFilename, newFilename); err != nil {
						log.Printf("Error renaming file: %v", err)
						responseText = "Error renaming file."
						break
					}
					responseText = fmt.Sprintf("File '%s' renamed to '%s'.", oldFilename, newFilename)
				case "delete":
					if len(tokens) < 2 {
						responseText = "Usage: delete <filename>"
						break
					}
					filename := tokens[1]
					err := deleteFileInR2AndDB(senderID, filename)
					if err != nil {
						log.Printf("Error deleting file: %v", err)
						responseText = "Error deleting file."
						break
					}
					responseText = fmt.Sprintf("File '%s' deleted.", filename)
				default:
					responseText = "Unknown command: " + command
				}

				// Send the response back using your callSendAPI function.
				if err := callSendAPI(senderID, responseText, config); err != nil {
					log.Printf("Error sending message: %v", err)
				}
				// Continue to next messaging event.
				continue
			}

			// Handle file attachments.
			if messaging.Message != nil && len(messaging.Message.Attachments) > 0 {
				mu.Lock()
				pendingFilename, exists := userUpload[senderID]
				mu.Unlock()

				if !exists {
					// If no upload is pending, ignore or send an error message.
					if err := callSendAPI(senderID, "Please initiate an upload command first.", config); err != nil {
						log.Printf("Error sending message: %v", err)
					}
					continue
				}

				// Process the attachment.
				attachment := messaging.Message.Attachments[0]
				fileURL := attachment.Payload.URL

				// Download the file
				fileData, err := downloadFile(fileURL)
				if err != nil {
					log.Printf("Error downloading file: %v", err)
					callSendAPI(senderID, "Error downloading file.", config)
					continue
				}

				// Upload the file to R2
				newFileURL, err := uploadToR2(pendingFilename, fileData)
				if err != nil {
					log.Printf("Error uploading file: %v", err)
					callSendAPI(senderID, "Error uploading file.", config)
					continue
				}

				// Update the file URL in the database
				if err := updateFileURL(senderID, pendingFilename, newFileURL); err != nil {
					log.Printf("Error updating file URL: %v", err)
					callSendAPI(senderID, "Error updating file information.", config)
					continue
				}

				callSendAPI(senderID, "Upload successful!", config)

				// Clear the pending upload.
				mu.Lock()
				delete(userUpload, senderID)
				mu.Unlock()
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

// callSendAPI sends the echo message using Facebook's Send API.
func callSendAPI(senderID, messageText string, config Config) error {
	payload := map[string]interface{}{
		"recipient": map[string]string{
			"id": senderID,
		},
		"message": map[string]string{
			"text": messageText,
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshalling payload: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/%s/me/messages?access_token=%s", config.APIVersion, config.PageAccessToken)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr APIError
		if err := json.Unmarshal(bodyBytes, &apiErr); err == nil {
			return fmt.Errorf("Facebook API error: %s (type: %s, code: %d)", apiErr.Error.Message, apiErr.Error.Type, apiErr.Error.Code)
		}
		return fmt.Errorf("Facebook API error: %s", string(bodyBytes))
	}

	return nil
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	dbConnStr := os.Getenv("DB_CONN_STR")
	dbconn, err = sql.Open("postgres", dbConnStr)
	if err != nil {
		log.Fatalf("Error connecting to PostgreSQL: %v", err)
	}
	queries = db.New(dbconn) // Initialize queries here

	// Initialize R2 (AWS S3-compatible)
	s3Client, bucket, err = initR2()
	if err != nil {
		log.Fatalf("Error initializing R2: %v", err)
	}

	log.Printf("Starting with config: %+v", config)

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method)
		switch r.Method {
		case http.MethodGet:
			verifyWebhook(w, r, config)
		case http.MethodPost:
			handleMessages(w, r, config)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	log.Printf("Server is listening on port %s", config.Port)
	log.Fatal(http.ListenAndServe(":"+config.Port, nil))
}

func initR2() (*s3.Client, string, error) {
	// Load R2 credentials from environment variables
	accessKeyID := os.Getenv("R2_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("R2_SECRET_ACCESS_KEY")
	baseEndpoint := os.Getenv("R2_BASE_ENDPOINT")
	bucketName := os.Getenv("R2_BUCKET_NAME")

	if accessKeyID == "" || secretAccessKey == "" || baseEndpoint == "" || bucketName == "" {
		return nil, "", fmt.Errorf("missing R2 environment variables")
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("auto"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load R2 configuration: %w", err)
	}

	// Use BaseEndpoint (New Method)
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseEndpoint)
		o.UsePathStyle = true // Required for R2
	})

	return s3Client, bucketName, nil
}

func listFilesFromDB(userID, category string) ([]string, error) {
	if category == "" {
		// List all categories.
		categories, err := queries.ListAllCategories(context.Background(), userID)
		if err != nil {
			return nil, err
		}

		var result []string
		for _, cat := range categories {
			if cat.Valid {
				result = append(result, cat.String)
			}
		}
		return result, nil
	} else {
		// List files in the specified category.
		files, err := queries.ListFilesInCategory(context.Background(), db.ListFilesInCategoryParams{
			Theme:  sql.NullString{String: category, Valid: true},
			UserID: userID,
		})

		if err != nil {
			return nil, err
		}
		return files, nil
	}
}

func isValidFilename(filename string) bool {
	for _, r := range filename {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// insertFileMetadata inserts file metadata into the database.
func insertFileMetadata(userID, filename, theme string, timestamp string) error {
	// Convert timestamp string to time.Time
	createdAt, err := time.Parse("2006-01-02 15:04:05", timestamp)
	if err != nil {
		return fmt.Errorf("error parsing timestamp: %w", err)
	}

	// Check if a file with the same name already exists for this user and theme.
	existingFile, err := queries.GetFileMetadataByFilenameAndTheme(context.Background(), db.GetFileMetadataByFilenameAndThemeParams{
		UserID:   userID,
		FileName: filename,
		Theme:    sql.NullString{String: theme, Valid: true},
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("error checking for existing file: %w", err)
	}

	if existingFile.FileName != "" {
		return fmt.Errorf("file with name '%s' already exists in theme '%s' for this user", filename, theme)
	}

	// Create InsertFileMetadataParams
	params := db.InsertFileMetadataParams{
		UserID:      userID,
		FileName:    filename,
		FileContent: sql.NullString{String: "", Valid: false}, // Initially empty
		CreatedAt:   createdAt,
		Theme:       sql.NullString{String: theme, Valid: true},
	}

	// Use sqlc-generated function
	err = queries.InsertFileMetadata(context.Background(), params)
	if err != nil {
		return fmt.Errorf("error inserting file metadata: %w", err)
	}

	return nil
}

func uploadToR2(filename string, fileData []byte) (string, error) {
	// Check if s3Client is initialized
	if s3Client == nil {
		return "", errors.New("s3Client is not initialized")
	}

	// Load bucket name from environment variable
	bucket := os.Getenv("R2_BUCKET_NAME")
	if bucket == "" {
		return "", errors.New("R2_BUCKET_NAME environment variable is not set")
	}

	// Determine Content-Type based on file data
	contentType := http.DetectContentType(fileData)
	log.Println("Detected Content Type:", contentType)

	// Generate a new filename with the correct extension if needed
	newFilename, err := addExtensionIfNeeded(filename, contentType)
	if err != nil {
		return "", fmt.Errorf("error adding extension to filename: %w", err)
	}

	// Upload the file to R2 with the correct extension
	_, err = s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:        &bucket,
		Key:           &newFilename, // Use the new filename with extension
		Body:          bytes.NewReader(fileData),
		ContentType:   aws.String(contentType), // Use the already detected content type
		ContentLength: aws.Int64(int64(len(fileData))),
		ACL:           types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		return "", fmt.Errorf("error uploading file to R2: %w", err)
	}

	// Construct the public URL of the uploaded file using the r2.dev format
	bucketID := os.Getenv("R2_BUCKET_ID")
	if bucketID == "" {
		return "", errors.New("R2_BUCKET_ID environment variable is not set")
	}

	newFileURL := fmt.Sprintf("https://%s.r2.dev/%s", bucketID, newFilename)
	return newFileURL, nil // Correctly return the URL and nil error
}

func addExtensionIfNeeded(filename, contentType string) (string, error) {
	fileExtension := filepath.Ext(filename)
	if fileExtension != "" {
		return filename, nil // Extension already exists
	}

	switch contentType {
	case "image/jpeg":
		return filename + ".jpg", nil
	case "image/png":
		return filename + ".png", nil
	case "image/gif":
		return filename + ".gif", nil
	case "image/webp":
		return filename + ".webp", nil
	case "application/pdf":
		return filename + ".pdf", nil
	case "text/plain; charset=utf-8":
		return filename + ".txt", nil
	// Add more cases as needed
	default:
		return filename + ".bin", fmt.Errorf("unsupported content type: %s", contentType)
	}
}
func updateFileURL(userID, filename string, newFileURL string) error {
	// Check if queries is initialized
	if queries == nil {
		return errors.New("database queries are not initialized")
	}

	// Use sqlc-generated function to update the file URL.
	err := queries.UpdateFileURL(context.Background(), db.UpdateFileURLParams{
		FileContent: sql.NullString{String: newFileURL, Valid: true},
		UserID:      userID,
		FileName:    filename,
	})
	if err != nil {
		return fmt.Errorf("error updating file URL in database: %w", err)
	}

	return nil
}

// downloadFile downloads a file from the given URL.
func downloadFile(fileURL string) ([]byte, error) {
	resp, err := http.Get(fileURL)
	if err != nil {
		return nil, fmt.Errorf("error downloading file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("file download failed with status: %s", resp.Status)
	}

	fileData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading file data: %w", err)
	}

	return fileData, nil
}

// renameFileInDB renames a file in the database.
func renameFileInDB(userID, oldFilename, newFilename string) error {
	// Check if queries is initialized
	if queries == nil {
		return errors.New("database queries are not initialized")
	}

	// Validate new filename (optional, but recommended)
	if !isValidFilename(newFilename) {
		return errors.New("invalid new filename. Please use alphanumeric characters, underscores, or hyphens.")
	}

	// Check if the new filename already exists for this user
	existingFile, err := queries.GetFileMetadataByFilenameAndTheme(context.Background(), db.GetFileMetadataByFilenameAndThemeParams{
		UserID:   userID,
		FileName: newFilename,
		Theme:    sql.NullString{Valid: false}, // We don't care about the theme here
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("error checking for existing file: %w", err)
	}

	if existingFile.FileName != "" {
		return fmt.Errorf("file with name '%s' already exists for this user", newFilename)
	}

	// Use sqlc-generated function to rename the file.
	err = queries.RenameFile(context.Background(), db.RenameFileParams{
		FileName:   newFilename,
		FileName_2: oldFilename,
		UserID:     userID,
	})
	if err != nil {
		return fmt.Errorf("error renaming file in database: %w", err)
	}

	return nil
}

func openFile(userID, filename string) (string, error) {
	// Check if queries is initialized
	if queries == nil {
		return "", errors.New("database queries are not initialized")
	}

	// 1. Get file URL by filename
	fileURLResult, err := queries.GetFileURL(context.Background(), db.GetFileURLParams{
		FileName: filename,
		UserID:   userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("file '%s' not found", filename)
		}
		return "", fmt.Errorf("error getting file URL: %w", err)
	}

	if !fileURLResult.Valid {
		return "", fmt.Errorf("file '%s' has no content", filename)
	}

	fileURL := fileURLResult.String

	// 2. Extract extension from fileURL and check type
	fileExtension := strings.ToLower(filepath.Ext(fileURL))

	switch fileExtension {
	case ".txt":
		// Handle text files:
		// In this case, we will just return the url
		return fmt.Sprintf("Text file: %s", fileURL), nil
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		// Handle image files:
		// In this case, we will just return the url
		return fmt.Sprintf("Image file: %s", fileURL), nil
	case ".pdf":
		// Handle pdf files:
		// In this case, we will just return the url
		return fmt.Sprintf("PDF file: %s", fileURL), nil
	default:
		// Handle other file types or unknown types:
		return fmt.Sprintf("File: %s (unknown type)", fileURL), nil
	}
}

func deleteFileInR2AndDB(userID, filename string) error {
	// Check if queries is initialized
	if queries == nil {
		return errors.New("database queries are not initialized")
	}

	// 1. Get file URL from the database
	fileURLResult, err := queries.GetFileURL(context.Background(), db.GetFileURLParams{
		FileName: filename,
		UserID:   userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("file '%s' not found", filename)
		}
		return fmt.Errorf("error getting file URL: %w", err)
	}

	if !fileURLResult.Valid {
		return fmt.Errorf("file '%s' has no content", filename)
	}

	fileURL := fileURLResult.String

	// 2. Delete from R2 (if a URL exists)
	if fileURL != "" {
		// Extract the key (filename) from the URL
		key, err := extractKeyFromURL(fileURL)
		if err != nil {
			return fmt.Errorf("error extracting key from URL: %w", err)
		}

		// Delete from R2
		err = deleteFromR2(key)
		if err != nil {
			return fmt.Errorf("error deleting from R2: %w", err)
		}
	}

	// 3. Delete from the database
	err = queries.DeleteFile(context.Background(), db.DeleteFileParams{
		FileName: filename,
		UserID:   userID,
	})
	if err != nil {
		return fmt.Errorf("error deleting file from database: %w", err)
	}

	return nil
}

// deleteFromR2 deletes a file from R2.
func deleteFromR2(key string) error {
	// Check if s3Client is initialized
	if s3Client == nil {
		return errors.New("s3Client is not initialized")
	}

	// Load bucket name from environment variable
	bucket := os.Getenv("R2_BUCKET_NAME")
	if bucket == "" {
		return errors.New("R2_BUCKET_NAME environment variable is not set")
	}

	_, err := s3Client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("error deleting object from R2: %w", err)
	}

	return nil
}

func extractKeyFromURL(fileURL string) (string, error) {
	// Example URL: https://<bucket-id>.r2.dev/<filename>
	parts := strings.Split(fileURL, "/")
	if len(parts) < 4 {
		return "", fmt.Errorf("invalid R2 URL format: %s", fileURL)
	}
	key := parts[len(parts)-1] // The last part is the key
	return key, nil
}
