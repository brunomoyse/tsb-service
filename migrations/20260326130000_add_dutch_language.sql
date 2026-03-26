-- +goose Up

-- Add 'nl' (Dutch) to the language CHECK constraint on product_translations
ALTER TABLE product_translations DROP CONSTRAINT product_translations_language_check;
ALTER TABLE product_translations ADD CONSTRAINT product_translations_language_check
    CHECK (language::text = ANY (ARRAY['en'::text, 'fr'::text, 'nl'::text, 'zh'::text]::text[]));

-- Add 'nl' (Dutch) to the language CHECK constraint on product_category_translations
ALTER TABLE product_category_translations DROP CONSTRAINT product_category_translations_language_check;
ALTER TABLE product_category_translations ADD CONSTRAINT product_category_translations_language_check
    CHECK (language::text = ANY (ARRAY['en'::text, 'fr'::text, 'nl'::text, 'zh'::text]::text[]));

-- +goose Down

-- Revert product_translations CHECK constraint (remove 'nl')
ALTER TABLE product_translations DROP CONSTRAINT product_translations_language_check;
ALTER TABLE product_translations ADD CONSTRAINT product_translations_language_check
    CHECK (language::text = ANY (ARRAY['en'::text, 'fr'::text, 'zh'::text]::text[]));

-- Revert product_category_translations CHECK constraint (remove 'nl')
ALTER TABLE product_category_translations DROP CONSTRAINT product_category_translations_language_check;
ALTER TABLE product_category_translations ADD CONSTRAINT product_category_translations_language_check
    CHECK (language::text = ANY (ARRAY['en'::text, 'fr'::text, 'zh'::text]::text[]));
