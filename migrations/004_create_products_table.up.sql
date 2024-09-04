-- Table: public.products

CREATE TABLE IF NOT EXISTS public.products
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP,
    price double precision,
    is_active boolean NOT NULL DEFAULT true,
    code text,
    slug text,
    CONSTRAINT products_pkey PRIMARY KEY (id),
    CONSTRAINT products_slug_unique UNIQUE (slug)
);

SELECT diesel_manage_updated_at('products');
