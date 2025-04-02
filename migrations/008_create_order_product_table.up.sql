-- Table: public.order_product

CREATE TABLE IF NOT EXISTS public.order_product
(
    order_id uuid NOT NULL,
    product_id uuid NOT NULL,
    unit_price numeric(10,2) NOT NULL,
    quantity integer NOT NULL,
    total_price numeric(10,2) NOT NULL,
    CONSTRAINT pk_order_product PRIMARY KEY (order_id, product_id),
    CONSTRAINT order_product_order_id_foreign FOREIGN KEY (order_id)
        REFERENCES public.orders (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE,
    CONSTRAINT order_product_product_id_foreign FOREIGN KEY (product_id)
        REFERENCES public.products (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE
)
