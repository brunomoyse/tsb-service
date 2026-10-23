-- +goose Up
-- HubRise integration, part 3: order links, the durable outbox, the inbound event store and the
-- foreign-order cache. New tables only. See docs/hubrise/PLAN.md sections 4, 6 and 7.

-- The foreign keys below take SHARE ROW EXCLUSIVE on orders, which conflicts with every
-- writer of that table. lock_timeout bounds the wait: a long transaction there makes this
-- migration fail fast (the deploy is simply re-run) instead of queueing every writer behind it.
SET LOCAL lock_timeout = '5s';
-- One row per order known to the integration. Also the idempotency anchor of order_create:
-- enqueueing is INSERT ... ON CONFLICT (order_id) DO NOTHING, and the outbox row is written in
-- the same transaction only when the insert happened.
CREATE TABLE hubrise_orders (
    order_id              UUID PRIMARY KEY REFERENCES orders (id) ON DELETE CASCADE,
    hubrise_order_id      TEXT,
    push_state            TEXT NOT NULL DEFAULT 'pending' CHECK (push_state IN ('pending', 'sent', 'parked', 'skipped')),
    pushed_at             TIMESTAMPTZ,
    last_remote_status    TEXT,
    -- Set ONLY from remote event or remote GET data (never from our own clock).
    last_remote_event_at  TIMESTAMPTZ,
    -- Set when we send a status PATCH (HubRise does not echo our own changes back to us).
    last_local_patch_at   TIMESTAMPTZ,
    -- When the reconciler last read the order from HubRise, by OUR clock. The refresh reads the order
    -- read longest ago first; unlike last_remote_event_at it never takes part in the stale-event guard.
    last_remote_read_at   TIMESTAMPTZ,
    -- The confirmed_time (estimated ready time) last seen at HubRise: it is applied to the local
    -- order only when HubRise changes it, so a ready time edited locally afterwards is kept.
    last_remote_confirmed_time TIMESTAMPTZ,
    remote_total          NUMERIC(10, 2),
    needs_attention       TEXT,
    -- Set once staff were told that HubRise still had the order as new (the alert is sent once).
    stuck_alerted_at      TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX hubrise_orders_hubrise_order_id_uidx ON hubrise_orders (hubrise_order_id) WHERE hubrise_order_id IS NOT NULL;
CREATE INDEX hubrise_orders_needs_attention_idx ON hubrise_orders (order_id) WHERE needs_attention IS NOT NULL;

-- Durable queue of everything that leaves for HubRise.
CREATE TABLE hubrise_outbox (
    id              BIGSERIAL PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('order_create', 'order_status', 'inventory', 'customer', 'customer_anonymise')),
    order_id        UUID REFERENCES orders (id) ON DELETE CASCADE,
    dedupe_key      TEXT,
    payload         JSONB NOT NULL DEFAULT '{}',
    state           TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sending', 'sent', 'parked', 'skipped')),
    -- Number of claims (incremented by the claim statement, so it is durable before the HTTP call).
    attempts        INT NOT NULL DEFAULT 0,
    leased_until    TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    parked_at       TIMESTAMPTZ
);

-- Inventory rows collapse to the latest pending state. A row already sending is outside the
-- index, so a newer toggle inserts a fresh row and is never merged into an in-flight one.
-- order_status rows are deliberately NOT deduplicated (every transition is sent, in order).
CREATE UNIQUE INDEX hubrise_outbox_inventory_pending_uidx ON hubrise_outbox (dedupe_key)
    WHERE state = 'pending' AND kind = 'inventory';
CREATE INDEX hubrise_outbox_state_next_idx ON hubrise_outbox (state, next_attempt_at);
CREATE INDEX hubrise_outbox_order_idx ON hubrise_outbox (order_id, id);
CREATE INDEX hubrise_outbox_dedupe_idx ON hubrise_outbox (dedupe_key, id) WHERE dedupe_key IS NOT NULL;

-- Callback events received from HubRise. The HubRise event id is the primary key, which makes
-- redelivery idempotent.
CREATE TABLE hubrise_events (
    id              TEXT PRIMARY KEY,
    resource_type   TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    run_after       TIMESTAMPTZ,
    state           TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed', 'ignored')),
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    processed_at    TIMESTAMPTZ,
    last_error      TEXT
);

CREATE INDEX hubrise_events_pending_idx ON hubrise_events (received_at) WHERE state = 'pending';

-- HubRise orders known not to be ours (placed by other channels), so the processor never fetches
-- them twice. Purged after a few days.
CREATE TABLE hubrise_foreign_orders (
    hubrise_order_id TEXT PRIMARY KEY,
    seen_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX hubrise_foreign_orders_seen_at_idx ON hubrise_foreign_orders (seen_at);

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP TABLE hubrise_foreign_orders;
DROP TABLE hubrise_events;
DROP TABLE hubrise_outbox;
DROP TABLE hubrise_orders;
