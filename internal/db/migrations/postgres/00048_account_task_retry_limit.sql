-- +goose Up
ALTER TABLE account_task_runs ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE account_task_runs DROP COLUMN attempt_count;
