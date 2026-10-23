-- +goose Up
-- HubRise integration, part 5: a choice can be sold out. HubRise inventory can sell out an option
-- (an egg, an extra sauce) and a catalog restriction can take it off sale, and choices had no
-- availability until now. A constant default makes this a metadata-only change: every existing
-- choice stays available, which is also the correct backfill. See docs/hubrise/PLAN.md section 4.
SET LOCAL lock_timeout = '5s';

ALTER TABLE product_choices ADD COLUMN is_available BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE product_choices DROP COLUMN is_available;
