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
