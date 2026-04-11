-- +goose Up
-- HubRise OAuth token storage (one row per configured client, e.g. "tsb-webshop").
CREATE TABLE hubrise_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_name TEXT NOT NULL UNIQUE,
    location_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    catalog_id TEXT,
    customer_list_id TEXT,
    access_token TEXT NOT NULL,
    scope TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Incoming HubRise callback events. Primary key is the HubRise event id,
-- giving idempotence for free.
CREATE TABLE hubrise_webhook_events (
    id TEXT PRIMARY KEY,
    client_name TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    error_msg TEXT
);
CREATE INDEX idx_hubrise_webhook_events_unprocessed
    ON hubrise_webhook_events(received_at)
    WHERE processed_at IS NULL;

-- Catalog push state tracked per HubRise client (currently only "tsb-webshop").
CREATE TABLE hubrise_catalog_sync_state (
    client_name TEXT PRIMARY KEY REFERENCES hubrise_connections(client_name) ON DELETE CASCADE,
    last_pushed_version BIGINT,
    last_pushed_at TIMESTAMPTZ,
    last_push_status TEXT CHECK (last_push_status IN ('pending', 'success', 'failed')),
    last_error TEXT
);

-- Track HubRise order id + push state on local orders.
ALTER TABLE orders ADD COLUMN hubrise_order_id TEXT;
ALTER TABLE orders ADD COLUMN hubrise_push_status TEXT
    CHECK (hubrise_push_status IN ('pending', 'pushed', 'failed'));
ALTER TABLE orders ADD COLUMN hubrise_push_attempts INT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN hubrise_last_push_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE orders DROP COLUMN IF EXISTS hubrise_last_push_at;
ALTER TABLE orders DROP COLUMN IF EXISTS hubrise_push_attempts;
ALTER TABLE orders DROP COLUMN IF EXISTS hubrise_push_status;
ALTER TABLE orders DROP COLUMN IF EXISTS hubrise_order_id;
DROP TABLE IF EXISTS hubrise_catalog_sync_state;
DROP INDEX IF EXISTS idx_hubrise_webhook_events_unprocessed;
DROP TABLE IF EXISTS hubrise_webhook_events;
DROP TABLE IF EXISTS hubrise_connections;
