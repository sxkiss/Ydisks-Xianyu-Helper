-- +goose Up
ALTER TABLE chat_sessions ADD COLUMN account_role TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE chat_sessions ADD COLUMN buyer_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN seller_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN role_item_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_sessions ADD COLUMN role_source TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_sessions DROP COLUMN role_source;
ALTER TABLE chat_sessions DROP COLUMN role_item_id;
ALTER TABLE chat_sessions DROP COLUMN seller_user_id;
ALTER TABLE chat_sessions DROP COLUMN buyer_user_id;
ALTER TABLE chat_sessions DROP COLUMN account_role;
