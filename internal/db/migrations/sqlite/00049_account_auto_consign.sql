-- +goose Up
ALTER TABLE cookies ADD COLUMN auto_consign INTEGER NOT NULL DEFAULT 0;
UPDATE cookies SET auto_consign = auto_confirm;

-- +goose Down
ALTER TABLE cookies DROP COLUMN auto_consign;
