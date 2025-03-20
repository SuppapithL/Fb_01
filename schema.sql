-- schema.sql
CREATE TABLE IF NOT EXISTS line_01 (
    id SERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_content TEXT,
    created_at TIMESTAMP NOT NULL,
    theme TEXT
);