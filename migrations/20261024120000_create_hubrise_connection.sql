-- +goose Up
-- HubRise integration, part 1: the connection of this installation to a HubRise location.
-- New tables only: an installation that never connects (or never sets the HUBRISE_* env) carries
-- empty tables and behaves exactly as before. See docs/hubrise/PLAN.md section 4.

-- One row at most, same idiom as restaurant_config: one deployment = one database = one location.
-- Absence of the row (or access_token_enc IS NULL) means "not connected" = standalone mode.
CREATE TABLE hubrise_connection (
    id                          BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    -- Runtime switch: connected but disabled = paused (nothing leaves, nothing is imported).
    enabled                     BOOLEAN NOT NULL DEFAULT FALSE,
    -- AES-256-GCM: 1 key version byte + 12 byte nonce + ciphertext. NULL = disconnected.
    access_token_enc            BYTEA,
    account_id                  TEXT,
    account_name                TEXT,
    location_id                 TEXT,
    location_name               TEXT,
    catalog_id                  TEXT,
    catalog_name                TEXT,
    customer_list_id            TEXT,
    customer_list_name          TEXT,
    scope                       TEXT,
    -- Feature toggles: all start OFF, so a new connection does nothing until each one is turned on.
    push_orders                 BOOLEAN NOT NULL DEFAULT FALSE,
    sync_status                 BOOLEAN NOT NULL DEFAULT FALSE,
    sync_inventory              BOOLEAN NOT NULL DEFAULT FALSE,
    sync_catalog                BOOLEAN NOT NULL DEFAULT FALSE,
    sync_customers              BOOLEAN NOT NULL DEFAULT FALSE,
    -- sync_catalog = HubRise owns the managed fields; this one only decides whether a catalog
    -- change is imported by itself or just announced (catalog_update_available_at).
    catalog_auto_import         BOOLEAN NOT NULL DEFAULT FALSE,
    auto_refund_on_cancel       BOOLEAN NOT NULL DEFAULT FALSE,
    catalog_language            TEXT NOT NULL DEFAULT 'fr' CHECK (catalog_language IN ('fr', 'en', 'nl', 'zh')),
    variant_ref                 TEXT,
    channel_name                TEXT,
    -- Charge refs sent with an order; NULL = the built-in defaults (DELIVERY, PAYMENT_FEE).
    delivery_charge_ref         TEXT,
    payment_fee_charge_ref      TEXT,
    import_images               BOOLEAN NOT NULL DEFAULT FALSE,
    -- Catalog bookkeeping: hash of the last imported catalog, and of the last successful dry run
    -- (a recent dry run is required before sync_catalog can be turned on).
    catalog_hash                TEXT,
    catalog_preview_hash        TEXT,
    catalog_preview_at          TIMESTAMPTZ,
    catalog_update_available_at TIMESTAMPTZ,
    -- Orders created before this instant are never pushed (stamped each time order pushing
    -- becomes effective, so a toggle can never replay history).
    push_orders_since           TIMESTAMPTZ,
    -- Stamped once the shop's existing sold-out products were pushed to HubRise.
    inventory_baseline_at       TIMESTAMPTZ,
    -- Set when the connection was re-authorized against another location or catalog while links
    -- exist: nothing runs until the old links are cleared.
    relink_required_at          TIMESTAMPTZ,
    last_catalog_sync_at        TIMESTAMPTZ,
    last_event_at               TIMESTAMPTZ,
    -- A 401/403 from HubRise (revoked, blocked or unpaid): the pipeline is paused until reconnect.
    auth_error_at               TIMESTAMPTZ,
    last_error                  TEXT,
    callback_registered_at      TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One-time OAuth "state" nonces, bound to the admin who started the connection.
CREATE TABLE hubrise_oauth_states (
    state         TEXT PRIMARY KEY,
    admin_user_id UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX hubrise_oauth_states_expires_at_idx ON hubrise_oauth_states (expires_at);

-- Leases of the singleton background jobs (reconciler, sweeper...), acquired with a
-- compare-and-set UPDATE so two replicas never run the same job at once.
CREATE TABLE hubrise_job_leases (
    name         TEXT PRIMARY KEY,
    holder       TEXT NOT NULL,
    leased_until TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE hubrise_job_leases;
DROP TABLE hubrise_oauth_states;
DROP TABLE hubrise_connection;
