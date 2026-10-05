-- +goose Up
ALTER TABLE chat_sessions ADD COLUMN user_hidden_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chat_sessions ADD COLUMN messages_cleared_at INTEGER NOT NULL DEFAULT 0;
DROP INDEX IF EXISTS idx_chat_sessions_account_visibility_recent;
CREATE INDEX idx_chat_sessions_account_visibility_recent
    ON chat_sessions(cookie_id, is_visible, user_hidden_at, last_message_at DESC, chat_id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_chat_sessions_account_visibility_recent;
CREATE INDEX idx_chat_sessions_account_visibility_recent
    ON chat_sessions(cookie_id, is_visible, last_message_at DESC, chat_id DESC);
ALTER TABLE chat_sessions DROP COLUMN messages_cleared_at;
ALTER TABLE chat_sessions DROP COLUMN user_hidden_at;
