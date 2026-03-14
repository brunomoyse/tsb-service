-- +goose Up
ALTER TABLE users ADD COLUMN notify_marketing BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE users DROP COLUMN notify_marketing;
