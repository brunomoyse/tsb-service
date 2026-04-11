-- +goose Up
-- VAT categorization on products — Belgian SCE 2.0 context.
-- The mapping to percentages / SCE codes (A/B/C/D/X) is computed in Go
-- (see internal/modules/product/domain/vat.go) based on order service type.
ALTER TABLE products ADD COLUMN vat_category TEXT
    CHECK (vat_category IN ('food', 'beverage', 'zero_rated', 'out_of_scope'));
UPDATE products SET vat_category = 'food' WHERE vat_category IS NULL;
ALTER TABLE products ALTER COLUMN vat_category SET NOT NULL;

-- Monotonic menu version via Postgres sequence (race-free).
CREATE SEQUENCE menu_catalog_version_seq START WITH 1;

-- Audit trail of every menu mutation.
CREATE TABLE menu_change_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    changed_by UUID REFERENCES users(id),
    entity_type TEXT NOT NULL CHECK (entity_type IN (
        'product', 'product_category', 'product_choice',
        'product_translation', 'product_category_translation', 'product_choice_translation'
    )),
    entity_id UUID NOT NULL,
    operation TEXT NOT NULL CHECK (operation IN ('create', 'update', 'delete')),
    before_json JSONB,
    after_json JSONB,
    catalog_version BIGINT NOT NULL
);
CREATE INDEX idx_menu_change_log_entity ON menu_change_log(entity_type, entity_id);
CREATE INDEX idx_menu_change_log_version ON menu_change_log(catalog_version);

-- +goose Down
DROP INDEX IF EXISTS idx_menu_change_log_version;
DROP INDEX IF EXISTS idx_menu_change_log_entity;
DROP TABLE IF EXISTS menu_change_log;
DROP SEQUENCE IF EXISTS menu_catalog_version_seq;
ALTER TABLE products DROP COLUMN IF EXISTS vat_category;
