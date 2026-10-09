-- +goose Up
-- Staff cancel the first of two orders a customer placed for the same meal (a
-- double submit, or a re-order with a change); "OTHER" said nothing about it.
ALTER TABLE orders DROP CONSTRAINT orders_cancellation_reason_check;
ALTER TABLE orders ADD CONSTRAINT orders_cancellation_reason_check
    CHECK (cancellation_reason IN ('OUT_OF_STOCK', 'KITCHEN_CLOSED', 'DELIVERY_AREA', 'DUPLICATE', 'OTHER'));

-- +goose Down
UPDATE orders SET cancellation_reason = 'OTHER' WHERE cancellation_reason = 'DUPLICATE';
ALTER TABLE orders DROP CONSTRAINT orders_cancellation_reason_check;
ALTER TABLE orders ADD CONSTRAINT orders_cancellation_reason_check
    CHECK (cancellation_reason IN ('OUT_OF_STOCK', 'KITCHEN_CLOSED', 'DELIVERY_AREA', 'OTHER'));
