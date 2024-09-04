-- Table: public.product_product_category

CREATE TABLE IF NOT EXISTS public.product_product_category
(
    product_id uuid NOT NULL,
    product_category_id uuid NOT NULL,
    CONSTRAINT pk_product_product_category PRIMARY KEY (product_id, product_category_id),
    CONSTRAINT product_product_category_product_id_foreign FOREIGN KEY (product_id)
        REFERENCES public.products (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE,
    CONSTRAINT product_product_category_product_category_id_foreign FOREIGN KEY (product_category_id)
        REFERENCES public.product_categories (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE
);
