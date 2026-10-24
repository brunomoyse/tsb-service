-- +goose Up
-- Remove all platform order support (reverses migration 012)

DROP INDEX IF EXISTS idx_orders_platform_unique;
DROP INDEX IF EXISTS idx_orders_source_status;
DROP INDEX IF EXISTS idx_orders_platform_order_id;
DROP INDEX IF EXISTS idx_orders_source;

ALTER TABLE public.orders
    DROP CONSTRAINT IF EXISTS orders_source_check;

ALTER TABLE public.orders
    DROP COLUMN IF EXISTS platform_data,
    DROP COLUMN IF EXISTS platform_order_id,
    DROP COLUMN IF EXISTS source;

-- Remove orphan platform orders (no user_id) before re-enforcing NOT NULL
DELETE FROM public.orders WHERE user_id IS NULL;

-- Re-enforce user_id NOT NULL (was made nullable for platform orders)
ALTER TABLE public.orders ALTER COLUMN user_id SET NOT NULL;

-- +goose Down
-- Nothing to restore: 20260219215100_remove_platform_orders_support.sql already dropped the platform
-- order columns, indexes and constraint, so this migration only repeated that (all IF EXISTS) and
-- deleted the orphan rows, which cannot come back. Rolling back 20260219215100 re-adds the support.
SELECT 1;
