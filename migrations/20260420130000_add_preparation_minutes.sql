-- +goose Up
ALTER TABLE restaurant_config
ADD COLUMN preparation_minutes INT NOT NULL DEFAULT 30;

-- +goose Down
ALTER TABLE restaurant_config
DROP COLUMN IF EXISTS preparation_minutes;
