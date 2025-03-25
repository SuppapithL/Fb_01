// Code generated by sqlc. DO NOT EDIT.
// versions:
//   sqlc v1.28.0
// source: query.sql

package db

import (
	"context"
	"database/sql"
	"time"
)

const deleteFile = `-- name: DeleteFile :exec
DELETE FROM line_01 WHERE file_name = $1 AND user_id = $2
`

type DeleteFileParams struct {
	FileName string
	UserID   string
}

func (q *Queries) DeleteFile(ctx context.Context, arg DeleteFileParams) error {
	_, err := q.db.ExecContext(ctx, deleteFile, arg.FileName, arg.UserID)
	return err
}

const getFileMetadataByFilenameAndTheme = `-- name: GetFileMetadataByFilenameAndTheme :one
SELECT file_name, file_content, theme FROM line_01 WHERE user_id = $1 AND file_name = $2 AND theme = $3
`

type GetFileMetadataByFilenameAndThemeParams struct {
	UserID   string
	FileName string
	Theme    sql.NullString
}

type GetFileMetadataByFilenameAndThemeRow struct {
	FileName    string
	FileContent sql.NullString
	Theme       sql.NullString
}

func (q *Queries) GetFileMetadataByFilenameAndTheme(ctx context.Context, arg GetFileMetadataByFilenameAndThemeParams) (GetFileMetadataByFilenameAndThemeRow, error) {
	row := q.db.QueryRowContext(ctx, getFileMetadataByFilenameAndTheme, arg.UserID, arg.FileName, arg.Theme)
	var i GetFileMetadataByFilenameAndThemeRow
	err := row.Scan(&i.FileName, &i.FileContent, &i.Theme)
	return i, err
}

const getFileURL = `-- name: GetFileURL :one
SELECT file_content FROM line_01 WHERE file_name = $1 AND user_id = $2
`

type GetFileURLParams struct {
	FileName string
	UserID   string
}

func (q *Queries) GetFileURL(ctx context.Context, arg GetFileURLParams) (sql.NullString, error) {
	row := q.db.QueryRowContext(ctx, getFileURL, arg.FileName, arg.UserID)
	var file_content sql.NullString
	err := row.Scan(&file_content)
	return file_content, err
}

const insertFileMetadata = `-- name: InsertFileMetadata :exec
INSERT INTO line_01 (user_id, file_name, file_content, created_at, theme) 
VALUES ($1, $2, $3, $4, $5)
`

type InsertFileMetadataParams struct {
	UserID      string
	FileName    string
	FileContent sql.NullString
	CreatedAt   time.Time
	Theme       sql.NullString
}

func (q *Queries) InsertFileMetadata(ctx context.Context, arg InsertFileMetadataParams) error {
	_, err := q.db.ExecContext(ctx, insertFileMetadata,
		arg.UserID,
		arg.FileName,
		arg.FileContent,
		arg.CreatedAt,
		arg.Theme,
	)
	return err
}

const listAllCategories = `-- name: ListAllCategories :many
SELECT DISTINCT theme FROM line_01 WHERE user_id = $1
`

func (q *Queries) ListAllCategories(ctx context.Context, userID string) ([]sql.NullString, error) {
	rows, err := q.db.QueryContext(ctx, listAllCategories, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []sql.NullString
	for rows.Next() {
		var theme sql.NullString
		if err := rows.Scan(&theme); err != nil {
			return nil, err
		}
		items = append(items, theme)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listFilesInCategory = `-- name: ListFilesInCategory :many
SELECT file_name FROM line_01 WHERE theme = $1 AND user_id = $2
`

type ListFilesInCategoryParams struct {
	Theme  sql.NullString
	UserID string
}

func (q *Queries) ListFilesInCategory(ctx context.Context, arg ListFilesInCategoryParams) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, listFilesInCategory, arg.Theme, arg.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var file_name string
		if err := rows.Scan(&file_name); err != nil {
			return nil, err
		}
		items = append(items, file_name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const renameFile = `-- name: RenameFile :exec
UPDATE line_01 SET file_name = $1 WHERE file_name = $2 AND user_id = $3
`

type RenameFileParams struct {
	FileName   string
	FileName_2 string
	UserID     string
}

func (q *Queries) RenameFile(ctx context.Context, arg RenameFileParams) error {
	_, err := q.db.ExecContext(ctx, renameFile, arg.FileName, arg.FileName_2, arg.UserID)
	return err
}

const updateFileURL = `-- name: UpdateFileURL :exec
UPDATE line_01 SET file_content = $1 WHERE file_name = $2 AND user_id = $3
`

type UpdateFileURLParams struct {
	FileContent sql.NullString
	FileName    string
	UserID      string
}

func (q *Queries) UpdateFileURL(ctx context.Context, arg UpdateFileURLParams) error {
	_, err := q.db.ExecContext(ctx, updateFileURL, arg.FileContent, arg.FileName, arg.UserID)
	return err
}
