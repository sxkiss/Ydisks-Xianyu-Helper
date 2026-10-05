-- +goose Up
ALTER TABLE ai_reply_settings ADD COLUMN general_enabled INT NOT NULL DEFAULT 0;
ALTER TABLE ai_reply_settings ADD COLUMN general_prompt_enabled INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ai_reply_settings DROP COLUMN general_prompt_enabled;
ALTER TABLE ai_reply_settings DROP COLUMN general_enabled;
