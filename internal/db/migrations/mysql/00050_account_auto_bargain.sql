-- +goose Up
ALTER TABLE cookies ADD COLUMN auto_bargain INT NOT NULL DEFAULT 0 AFTER auto_consign;

-- +goose Down
ALTER TABLE cookies DROP COLUMN auto_bargain;
