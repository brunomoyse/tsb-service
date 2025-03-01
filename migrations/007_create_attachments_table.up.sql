-- Table: public.attachments

CREATE TABLE IF NOT EXISTS public.attachments
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    product_id uuid NOT NULL,
    url text NOT NULL,
    is_primary boolean NOT NULL DEFAULT false,
    CONSTRAINT attachments_pkey PRIMARY KEY (id),
    CONSTRAINT attachments_product_id_foreign FOREIGN KEY (product_id)
        REFERENCES public.products (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION
);

CREATE UNIQUE INDEX IF NOT EXISTS attachments_is__product_id_unique
    ON public.attachments USING btree
    (product_id ASC NULLS LAST)
    TABLESPACE pg_default
    WHERE is_primary = true;