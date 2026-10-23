-- +goose Up
-- HubRise integration, part 2: links between HubRise catalog entries and local rows. One link
-- table keeps the core catalog tables free of HubRise columns. See docs/hubrise/PLAN.md section 4.

-- The foreign keys below take SHARE ROW EXCLUSIVE on products, product_categories,
-- product_choice_groups and product_choices, which conflicts with every writer of those
-- tables. lock_timeout bounds the wait: a long transaction there makes this migration fail fast
-- (the deploy is simply re-run) instead of queueing every writer behind it.
SET LOCAL lock_timeout = '5s';
CREATE TABLE hubrise_catalog_links (
    id                  BIGSERIAL PRIMARY KEY,
    entity_type         TEXT NOT NULL CHECK (entity_type IN ('category', 'product', 'choice_group', 'choice')),
    -- Exactly the column matching entity_type is set. Deleting the local row (for example a choice
    -- in the dashboard) removes its link.
    category_id         UUID REFERENCES product_categories (id) ON DELETE CASCADE,
    product_id          UUID REFERENCES products (id) ON DELETE CASCADE,
    choice_group_id     UUID REFERENCES product_choice_groups (id) ON DELETE CASCADE,
    choice_id           UUID REFERENCES product_choices (id) ON DELETE CASCADE,
    -- category: ref. product: SKU ref. choice_group: skuRef|listRef. choice: skuRef|listRef|optionRef.
    hubrise_key         TEXT NOT NULL,
    -- Last segment of the key; the option ref is what inventory entries name.
    hubrise_ref         TEXT NOT NULL,
    hubrise_id          TEXT,
    content_hash        TEXT NOT NULL DEFAULT '',
    origin              TEXT NOT NULL DEFAULT 'import' CHECK (origin IN ('import', 'adopted', 'manual')),
    stock               NUMERIC(12, 3),
    -- TRUE only while the current unavailability was caused by HubRise inventory: the system never
    -- restores availability it did not take away.
    inventory_sold_out  BOOLEAN NOT NULL DEFAULT FALSE,
    sold_out_until      TIMESTAMPTZ,
    -- HubRise restrictions say "disabled". Effective availability of a managed row is
    -- NOT catalog_disabled AND NOT inventory_sold_out.
    catalog_disabled    BOOLEAN NOT NULL DEFAULT FALSE,
    inventory_synced_at TIMESTAMPTZ,
    image_md5           TEXT,
    retired_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT hubrise_catalog_links_target_chk CHECK (
        (entity_type = 'category'     AND category_id IS NOT NULL AND product_id IS NULL AND choice_group_id IS NULL AND choice_id IS NULL) OR
        (entity_type = 'product'      AND product_id IS NOT NULL AND category_id IS NULL AND choice_group_id IS NULL AND choice_id IS NULL) OR
        (entity_type = 'choice_group' AND choice_group_id IS NOT NULL AND category_id IS NULL AND product_id IS NULL AND choice_id IS NULL) OR
        (entity_type = 'choice'       AND choice_id IS NOT NULL AND category_id IS NULL AND product_id IS NULL AND choice_group_id IS NULL)
    )
);

-- One link per local row.
CREATE UNIQUE INDEX hubrise_catalog_links_category_uidx     ON hubrise_catalog_links (category_id)     WHERE category_id IS NOT NULL;
CREATE UNIQUE INDEX hubrise_catalog_links_product_uidx      ON hubrise_catalog_links (product_id)      WHERE product_id IS NOT NULL;
CREATE UNIQUE INDEX hubrise_catalog_links_choice_group_uidx ON hubrise_catalog_links (choice_group_id) WHERE choice_group_id IS NOT NULL;
CREATE UNIQUE INDEX hubrise_catalog_links_choice_uidx       ON hubrise_catalog_links (choice_id)       WHERE choice_id IS NOT NULL;

-- One link per HubRise entry.
CREATE UNIQUE INDEX hubrise_catalog_links_key_uidx ON hubrise_catalog_links (entity_type, hubrise_key);

-- Inventory entries name a SKU ref or an option ref.
CREATE INDEX hubrise_catalog_links_ref_idx ON hubrise_catalog_links (entity_type, hubrise_ref);

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP TABLE hubrise_catalog_links;
