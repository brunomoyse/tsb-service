-- +goose Up
ALTER TABLE orders
    ADD COLUMN cash_payment_amount NUMERIC(10, 2)
        CHECK (cash_payment_amount IS NULL OR cash_payment_amount >= 0);

-- +goose Down
ALTER TABLE orders DROP COLUMN cash_payment_amount;
