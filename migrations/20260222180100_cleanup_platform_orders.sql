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
-- Re-add platform order support

ALTER TABLE public.orders
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'TOKYO',
    ADD COLUMN IF NOT EXISTS platform_order_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS platform_data JSONB;

ALTER TABLE public.orders
    ADD CONSTRAINT orders_source_check CHECK (
        source = ANY (ARRAY['TOKYO', 'DELIVEROO', 'UBER'])
    );

ALTER TABLE public.orders ALTER COLUMN user_id DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_orders_source ON public.orders(source);
CREATE INDEX IF NOT EXISTS idx_orders_platform_order_id ON public.orders(platform_order_id);
CREATE INDEX IF NOT EXISTS idx_orders_source_status ON public.orders(source, order_status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_platform_unique ON public.orders(source, platform_order_id)
    WHERE platform_order_id IS NOT NULL;
