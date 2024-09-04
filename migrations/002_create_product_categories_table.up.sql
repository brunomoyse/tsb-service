-- Table: public.product_categories

CREATE TABLE IF NOT EXISTS public.product_categories
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP,
    "order" integer,
    CONSTRAINT product_categories_pkey PRIMARY KEY (id)
);

SELECT diesel_manage_updated_at('product_categories');
