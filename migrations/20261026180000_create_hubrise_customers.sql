-- +goose Up
-- HubRise integration, part 4: the link between a shop user and its customer in the HubRise customer
-- list. New table only (plus an index on a hubrise table); sync_customers, customer_list_name and the
-- customer outbox kinds were created with the connection and the outbox. See docs/hubrise/PLAN.md
-- section 19, amendment A1.

-- The foreign keys below take SHARE ROW EXCLUSIVE on users, which conflicts with every
-- writer of that table. lock_timeout bounds the wait: a long transaction there makes this
-- migration fail fast (the deploy is simply re-run) instead of queueing every writer behind it.
SET LOCAL lock_timeout = '5s';
-- One row per user known to the customer list. A row without hubrise_customer_id records that the
-- customer may exist at HubRise already (a creation was attempted and its answer may have been lost),
-- so the next attempt looks the customer up by private_ref (= the user id) before it creates one.
-- ON DELETE CASCADE only matters if a user row is ever really deleted: accounts are anonymised, and
-- the anonymise row of the outbox carries the HubRise customer id itself so it never depends on this one.
CREATE TABLE hubrise_customers (
    user_id             UUID PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    hubrise_customer_id TEXT UNIQUE,
    -- What was last written to the HubRise customer: "<profile hash>|<address hash>", compared with the
    -- wanted state to skip a PATCH that would change nothing.
    content_hash        TEXT NOT NULL DEFAULT '',
    synced_at           TIMESTAMPTZ,
    -- Set once the HubRise customer was anonymised (the account was deleted): never created again.
    anonymised_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Profile updates of a user collapse into one pending row: the row only names the user, the profile is
-- read when it is sent. A row already sending is outside the index, so a change made while it is on its
-- way queues a fresh one.
CREATE UNIQUE INDEX hubrise_outbox_customer_pending_uidx ON hubrise_outbox (dedupe_key)
    WHERE state = 'pending' AND kind = 'customer';

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP INDEX hubrise_outbox_customer_pending_uidx;
DROP TABLE hubrise_customers;
