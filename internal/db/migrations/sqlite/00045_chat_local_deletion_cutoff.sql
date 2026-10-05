-- +goose Up
ALTER TABLE chat_sessions ADD COLUMN local_messages_cleared_at INTEGER NOT NULL DEFAULT 0;
UPDATE chat_sessions SET local_messages_cleared_at=user_hidden_at WHERE user_hidden_at>0;

-- +goose Down
ALTER TABLE chat_sessions DROP COLUMN local_messages_cleared_at;
