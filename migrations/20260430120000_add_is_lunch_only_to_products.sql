-- +goose Up
ALTER TABLE products
    ADD COLUMN is_lunch_only BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE products
    DROP COLUMN is_lunch_only;
