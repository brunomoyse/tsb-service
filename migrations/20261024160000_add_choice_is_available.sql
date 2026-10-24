-- +goose Up
-- HubRise integration, part 5: availability of a choice (an option such as an egg or an extra sauce).
-- HubRise inventory can sell an option out, and choices had no availability until now. NOT NULL with a
-- constant default is metadata-only on PostgreSQL 11+, so no table rewrite and every existing choice
-- stays available. product_choices is read on every order, so lock acquisition is bounded: a busy
-- table makes this migration fail fast (the deploy is simply re-run) instead of queueing every
-- reader behind an ACCESS EXCLUSIVE request. See docs/hubrise/PLAN.md section 4.
SET LOCAL lock_timeout = '5s';
ALTER TABLE product_choices ADD COLUMN is_available BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE product_choices DROP COLUMN is_available;
