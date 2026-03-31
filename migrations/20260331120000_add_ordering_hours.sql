-- +goose Up
ALTER TABLE restaurant_config
ADD COLUMN ordering_hours JSONB;

-- +goose Down
ALTER TABLE restaurant_config
DROP COLUMN IF EXISTS ordering_hours;
