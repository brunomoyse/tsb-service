-- Table: public.product_translations

CREATE TABLE IF NOT EXISTS public.product_translations
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    product_id uuid NOT NULL,
    name text NOT NULL,
    description text,
    language text NOT NULL,
    CONSTRAINT product_translations_pkey PRIMARY KEY (id),
    CONSTRAINT product_translations_product_id_language_unique UNIQUE (product_id, language),
    CONSTRAINT product_translations_product_id_foreign FOREIGN KEY (product_id)
        REFERENCES public.products (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE,
    CONSTRAINT product_translations_language_check CHECK (language::text = ANY (ARRAY['en'::text, 'fr'::text, 'zh'::text]::text[]))
);
