-- +goose Up
-- HubRise integration, part 4: where a translation came from. 'manual' (typed in the dashboard,
-- the default, which is also the correct backfill: nothing ever came from HubRise) or 'hubrise'
-- (written by the catalog import). The importer only writes rows that are missing or already
-- 'hubrise', so a manual override survives every re-import. See docs/hubrise/PLAN.md sections 4
-- and 5.2.
--
-- These are ADD COLUMN with a constant default: metadata only on PostgreSQL 11+, no table rewrite.
-- No CHECK constraint (it would need a table scan under the same lock): the values are validated in
-- Go. lock_timeout bounds the wait for the lock, so a busy table fails the migration (the deploy
-- job retries it) instead of queueing every writer behind an ACCESS EXCLUSIVE request. SET LOCAL lasts for the migration's transaction only.
SET LOCAL lock_timeout = '5s';

ALTER TABLE product_translations ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE product_category_translations ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE product_choice_translations ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE product_choice_group_translations ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE product_choice_group_translations DROP COLUMN source;
ALTER TABLE product_choice_translations DROP COLUMN source;
ALTER TABLE product_category_translations DROP COLUMN source;
ALTER TABLE product_translations DROP COLUMN source;
