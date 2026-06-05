-- +goose Up
-- TEMPORARY (store-review): flags orders placed by Google Play / App Store
-- review accounts so they stay invisible to staff (no list, no subscription,
-- no push) and auto-cancel 10 min after creation. Revert this whole change
-- once the app is published.
ALTER TABLE orders ADD COLUMN is_test BOOLEAN NOT NULL DEFAULT FALSE;

-- Partial index keeps the auto-cancel sweeper cheap (it only scans test orders).
CREATE INDEX idx_orders_is_test_active ON orders (created_at) WHERE is_test = TRUE;

-- +goose Down
DROP INDEX IF EXISTS idx_orders_is_test_active;
ALTER TABLE orders DROP COLUMN is_test;
