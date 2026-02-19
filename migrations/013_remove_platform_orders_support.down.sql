-- Re-add platform order support

-- Add source column to track order origin
ALTER TABLE public.orders
    ADD COLUMN source TEXT NOT NULL DEFAULT 'TOKYO',
    ADD CONSTRAINT orders_source_check CHECK (
        source = ANY (ARRAY['TOKYO', 'DELIVEROO', 'UBER'])
    );

-- Add platform order ID for external platform orders
ALTER TABLE public.orders
    ADD COLUMN platform_order_id VARCHAR(255);

-- Add platform data for storing full external order details
ALTER TABLE public.orders
    ADD COLUMN platform_data JSONB;

-- Make user_id nullable for platform orders (may not have user initially)
ALTER TABLE public.orders
    ALTER COLUMN user_id DROP NOT NULL;

-- Add indexes for efficient lookups
CREATE INDEX idx_orders_source ON public.orders(source);
CREATE INDEX idx_orders_platform_order_id ON public.orders(platform_order_id);
CREATE INDEX idx_orders_source_status ON public.orders(source, order_status);

-- Add unique constraint to prevent duplicate platform orders
CREATE UNIQUE INDEX idx_orders_platform_unique ON public.orders(source, platform_order_id)
    WHERE platform_order_id IS NOT NULL;
