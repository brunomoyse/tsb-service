-- Table: public.products

CREATE TABLE IF NOT EXISTS public.products
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone,
    category_id uuid NOT NULL,
    price double precision,
    is_active boolean NOT NULL DEFAULT true,
    code text,
    slug text,
    CONSTRAINT products_pkey PRIMARY KEY (id),
    CONSTRAINT products_category_id_foreign FOREIGN KEY (category_id)
        REFERENCES public.product_categories (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION
    CONSTRAINT products_slug_unique UNIQUE (slug)
);
