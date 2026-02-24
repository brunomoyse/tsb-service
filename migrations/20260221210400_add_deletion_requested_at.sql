-- +goose Up
ALTER TABLE users ADD COLUMN deletion_requested_at TIMESTAMP(0) WITH TIME ZONE;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS deletion_requested_at;
