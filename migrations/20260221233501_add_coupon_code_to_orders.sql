-- +goose Up
ALTER TABLE orders ADD COLUMN coupon_code VARCHAR(50);

-- +goose Down
ALTER TABLE orders DROP COLUMN coupon_code;
