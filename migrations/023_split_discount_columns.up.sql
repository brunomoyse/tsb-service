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
