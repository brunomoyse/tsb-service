-- +goose Up
ALTER TABLE orders
    ADD COLUMN cancellation_reason TEXT
        CHECK (cancellation_reason IN ('OUT_OF_STOCK', 'KITCHEN_CLOSED', 'DELIVERY_AREA', 'OTHER'));

-- +goose Down
ALTER TABLE orders
    DROP COLUMN cancellation_reason;
