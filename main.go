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
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/joho/godotenv"
)

var (
	dbconn   *sql.DB // Ensures safe concurrent access
	s3Client *s3.Client
	bucket   string
	queries  *db.Queries // Add queries variable
	mu       sync.Mutex
	userFile = make(map[string]string) // maps userID -> filename
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
			} `json:"message"`
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

// handleMessages processes the POST requests from Facebook.
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
	// Inside your handleMessages function, replace the existing message handling loop with:
	for _, entry := range webhookReq.Entry {
		for _, messaging := range entry.Messaging {
			if messaging.Message != nil && messaging.Message.Text != "" {
				senderID := messaging.Sender.ID
				messageText := messaging.Message.Text

				// Split the incoming text by spaces.
				tokens := strings.Fields(messageText)
				if len(tokens) == 0 {
					// If there are no tokens, skip processing.
					continue
				}

				// Use the first token (converted to lowercase) to determine the command.
				command := strings.ToLower(tokens[0])
				var responseText string

				switch command {
				case "rename":
					responseText = "Rename command triggered."
					// Add your rename logic here.
				case "open":
					responseText = "Open command triggered."
					// Add your open logic here.
				case "upload":
					responseText = "Upload command triggered."
					// Add your upload logic here.
				case "delete":
					responseText = "Delete command triggered."
					// Add your delete logic here.
				case "list":
					responseText = "List command triggered."
					// Add your list logic here.
				default:
					// Fallback to echoing the entire message if command is not recognized.
					responseText = "Unknown command: " + command
				}

				// Send the response back using your callSendAPI function.
				if err := callSendAPI(senderID, responseText, config); err != nil {
					log.Printf("Error sending message: %v", err)
				}
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
