-- +goose Up
ALTER TABLE pos_devices
    ADD COLUMN fcm_token TEXT,
    ADD COLUMN fcm_token_updated_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE pos_devices
    DROP COLUMN fcm_token,
    DROP COLUMN fcm_token_updated_at;
