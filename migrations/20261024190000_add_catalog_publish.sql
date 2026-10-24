-- +goose Up
-- HubRise integration, part 5: the dashboard-owned menu. The local menu is published to the HubRise
-- catalog (catalog direction PUBLISH). Additive only: two defaulted columns, two new tables, and a
-- widened CHECK on the links of this unreleased series, so the previous release keeps working on
-- this schema. See docs/hubrise/PLAN.md section 21, amendment A15.

-- ALTER TABLE takes ACCESS EXCLUSIVE: lock_timeout makes this migration fail fast behind a long
-- transaction instead of queueing every reader of the table behind itself.
SET LOCAL lock_timeout = '5s';

-- The catalog direction is derived: sync_catalog = IMPORT, publish_catalog = PUBLISH, neither = NONE.
-- Both true is impossible by construction (one UPDATE reads the row FOR UPDATE); no CHECK is added to
-- an existing table.
ALTER TABLE hubrise_connection
    ADD COLUMN publish_catalog BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN publish_images  BOOLEAN NOT NULL DEFAULT FALSE;

-- The state machine of the catalog publisher: one row, not a queue. "One publish pending" is the row
-- itself: dirty_since is set while edits wait, change_seq says which edits a publish covered.
CREATE TABLE hubrise_catalog_publish (
    id                         BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    -- Bumped by every menu change. A publish clears the dirty flag only WHERE change_seq still equals
    -- the value it read at claim time, so an edit made during the PUT is never lost.
    change_seq                 BIGINT NOT NULL DEFAULT 0,
    dirty_since                TIMESTAMPTZ,
    last_change_at             TIMESTAMPTZ,
    next_attempt_at            TIMESTAMPTZ,
    attempts                   INT NOT NULL DEFAULT 0,
    lease_holder               TEXT,
    leased_until               TIMESTAMPTZ,
    -- Start of the last claimed run: the 30 s minimum interval holds across replicas and restarts.
    last_started_at            TIMESTAMPTZ,
    last_published_at          TIMESTAMPTZ,
    last_published_seq         BIGINT NOT NULL DEFAULT 0,
    last_published_local_hash  TEXT,
    last_published_remote_hash TEXT,
    -- NULL until an admin confirmed the first publish after switching to PUBLISH: nothing is
    -- written to HubRise before.
    first_publish_confirmed_at TIMESTAMPTZ,
    -- Someone changed the HubRise catalog elsewhere (callback or drift check).
    external_change_at         TIMESTAMPTZ,
    last_error                 TEXT,
    last_error_code            TEXT,
    last_error_at              TIMESTAMPTZ,
    -- A permanent refusal: cleared by the next menu change or a manual publish.
    blocked_at                 TIMESTAMPTZ,
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO hubrise_catalog_publish (id) VALUES (TRUE);

-- Exactly what the last successful PUT contained for the items the shop owns. It is the source of the
-- removal set (published minus desired), which the cascading delete of a link row would erase, and of
-- the per item publish state.
CREATE TABLE hubrise_published_items (
    entity_type  TEXT NOT NULL CHECK (entity_type IN ('category', 'product', 'choice_group', 'choice')),
    hubrise_key  TEXT NOT NULL,
    hubrise_ref  TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_type, hubrise_key)
);

-- A new item the shop generated the ref of. The constraint belongs to the table of this series (small,
-- nothing in it is older than the integration); the old releases only write the three old origins,
-- which the widened constraint still accepts.
-- +goose StatementBegin
DO $$
DECLARE
    old_name text;
BEGIN
    SELECT conname INTO old_name
    FROM pg_constraint
    WHERE conrelid = 'hubrise_catalog_links'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%origin%' AND pg_get_constraintdef(oid) NOT LIKE '%entity_type%';
    IF old_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE hubrise_catalog_links DROP CONSTRAINT %I', old_name);
    END IF;
    ALTER TABLE hubrise_catalog_links ADD CONSTRAINT hubrise_catalog_links_origin_check
        CHECK (origin IN ('import', 'adopted', 'manual', 'published'));
END $$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
-- Rewrite the new origin first, or the strict CHECK would refuse the rows.
UPDATE hubrise_catalog_links SET origin = 'manual' WHERE origin = 'published';
-- +goose StatementBegin
DO $$
DECLARE
    old_name text;
BEGIN
    SELECT conname INTO old_name
    FROM pg_constraint
    WHERE conrelid = 'hubrise_catalog_links'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%origin%' AND pg_get_constraintdef(oid) NOT LIKE '%entity_type%';
    IF old_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE hubrise_catalog_links DROP CONSTRAINT %I', old_name);
    END IF;
    ALTER TABLE hubrise_catalog_links ADD CONSTRAINT hubrise_catalog_links_origin_check
        CHECK (origin IN ('import', 'adopted', 'manual'));
END $$;
-- +goose StatementEnd
DROP TABLE hubrise_published_items;
DROP TABLE hubrise_catalog_publish;
ALTER TABLE hubrise_connection DROP COLUMN publish_images, DROP COLUMN publish_catalog;
