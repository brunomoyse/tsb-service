-- +goose Up
-- HubRise integration, part 6: where an order was placed from ('web' or 'app'), used as the HubRise
-- channel fallback and for reporting. Nullable, no default, no CHECK: the column is metadata-only on
-- PostgreSQL 11+ and the allowed values are validated in Go. Rows that existed before are NULL, which
-- the code reads as 'web'. orders is a hot table, so lock acquisition is bounded: a busy table makes
-- this migration fail fast (the deploy is simply re-run) instead of queueing every writer behind an
-- ACCESS EXCLUSIVE request. See docs/hubrise/PLAN.md section 4.
SET LOCAL lock_timeout = '5s';
ALTER TABLE orders ADD COLUMN channel TEXT;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE orders DROP COLUMN channel;
