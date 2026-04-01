-- +goose Up
ALTER TABLE device_push_tokens ADD COLUMN role VARCHAR(10) NOT NULL DEFAULT 'user';
CREATE INDEX idx_device_push_tokens_role ON device_push_tokens(role);

-- +goose Down
DROP INDEX IF EXISTS idx_device_push_tokens_role;
ALTER TABLE device_push_tokens DROP COLUMN role;
