-- Remove platform order support

-- Drop indexes
DROP INDEX IF EXISTS idx_orders_platform_unique;
DROP INDEX IF EXISTS idx_orders_source_status;
DROP INDEX IF EXISTS idx_orders_platform_order_id;
DROP INDEX IF EXISTS idx_orders_source;

-- Drop columns
ALTER TABLE public.orders DROP COLUMN IF EXISTS platform_data;
ALTER TABLE public.orders DROP COLUMN IF EXISTS platform_order_id;
ALTER TABLE public.orders DROP COLUMN IF EXISTS source;

-- Make user_id NOT NULL again
ALTER TABLE public.orders ALTER COLUMN user_id SET NOT NULL;
