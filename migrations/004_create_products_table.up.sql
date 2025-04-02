-- Table: public.products

CREATE TABLE IF NOT EXISTS public.products
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    category_id uuid NOT NULL,
    price numeric(10,2),
    is_visible boolean NOT NULL DEFAULT true,
    is_available boolean NOT NULL DEFAULT true,
    code text,
    slug text,
    is_halal boolean NOT NULL DEFAULT false,
    is_vegan boolean NOT NULL DEFAULT false,
    piece_count integer,
    CONSTRAINT products_pkey PRIMARY KEY (id),
    CONSTRAINT products_category_id_foreign FOREIGN KEY (category_id)
        REFERENCES public.product_categories (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT products_slug_unique UNIQUE (slug)
);
