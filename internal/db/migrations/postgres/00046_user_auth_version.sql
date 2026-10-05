-- +goose Up
ALTER TABLE users ADD COLUMN auth_version BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE users DROP COLUMN auth_version;
