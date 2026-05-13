-- +goose Up
ALTER TABLE orders
ADD COLUMN preferred_ready_time timestamp(0) with time zone;

-- +goose Down
ALTER TABLE orders
DROP COLUMN IF EXISTS preferred_ready_time;
