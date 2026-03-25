-- +goose Up
ALTER TABLE users ADD COLUMN zitadel_user_id TEXT UNIQUE;
CREATE INDEX idx_users_zitadel_user_id ON users(zitadel_user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_users_zitadel_user_id;
ALTER TABLE users DROP COLUMN IF EXISTS zitadel_user_id;
