-- +goose Up
ALTER TABLE orders
    ADD COLUMN transaction_fee NUMERIC(10, 2) NOT NULL DEFAULT 0
        CHECK (transaction_fee >= 0);

-- +goose Down
ALTER TABLE orders DROP COLUMN transaction_fee;
