-- +goose Up
ALTER TABLE mollie_payments RENAME COLUMN language TO locale;

-- +goose Down
ALTER TABLE mollie_payments RENAME COLUMN locale TO language;
