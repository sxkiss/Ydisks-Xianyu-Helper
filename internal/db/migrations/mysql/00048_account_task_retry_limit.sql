-- +goose Up
ALTER TABLE account_task_runs ADD COLUMN attempt_count INT NOT NULL DEFAULT 1 AFTER error_message;

-- +goose Down
ALTER TABLE account_task_runs DROP COLUMN attempt_count;
