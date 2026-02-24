-- +goose Up
ALTER TABLE orders ADD COLUMN language VARCHAR(5) NOT NULL DEFAULT 'fr';

-- +goose Down
ALTER TABLE orders DROP COLUMN language;
