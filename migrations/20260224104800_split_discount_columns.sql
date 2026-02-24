-- +goose Up
-- Split discount_amount into takeaway_discount and coupon_discount
ALTER TABLE orders
    ADD COLUMN takeaway_discount numeric(10,2) NOT NULL DEFAULT 0,
    ADD COLUMN coupon_discount numeric(10,2) NOT NULL DEFAULT 0;

-- Migrate existing data: if coupon_code is set, assume it's all coupon discount;
-- otherwise assume it's all takeaway discount (for PICKUP orders).
UPDATE orders
SET takeaway_discount = CASE
    WHEN coupon_code IS NULL AND order_type = 'PICKUP' THEN COALESCE(discount_amount, 0)
    ELSE 0
END,
coupon_discount = CASE
    WHEN coupon_code IS NOT NULL THEN COALESCE(discount_amount, 0)
    ELSE 0
END;

-- Drop the old combined column
ALTER TABLE orders DROP COLUMN discount_amount;

-- +goose Down
-- Restore the combined discount_amount column
ALTER TABLE orders
    ADD COLUMN discount_amount numeric(10,2) NOT NULL DEFAULT 0;

-- Recombine the split columns
UPDATE orders
SET discount_amount = takeaway_discount + coupon_discount;

-- Drop the split columns
ALTER TABLE orders
    DROP COLUMN takeaway_discount,
    DROP COLUMN coupon_discount;
