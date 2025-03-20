-- name: InsertFileMetadata :exec
INSERT INTO line_01 (user_id, file_name, file_content, created_at, theme) 
VALUES ($1, $2, $3, $4, $5);

-- name: UpdateFileURL :exec
UPDATE line_01 SET file_content = $1 WHERE file_name = $2;

-- name: GetFileURL :one
SELECT file_content FROM line_01 WHERE file_name = $1;

-- name: ListAllCategories :many
SELECT DISTINCT theme FROM line_01;

-- name: ListFilesInCategory :many
SELECT file_name FROM line_01 WHERE theme = $1;

-- name: RenameFile :exec
UPDATE line_01 SET file_name = $1 WHERE file_name = $2;

-- name: DeleteFile :exec
DELETE FROM line_01 WHERE file_name = $1;