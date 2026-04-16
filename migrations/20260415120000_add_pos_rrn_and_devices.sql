-- +goose Up
-- +goose StatementBegin

-- Add RRN (11-digit Belgian national id) and PIN to users for POS login.
-- Optional columns — existing users without an RRN simply can't sign into
-- the POS. A dashboard admin sets these via a mutation.
ALTER TABLE users
    ADD COLUMN rrn                  VARCHAR(11),
    ADD COLUMN pin_hash             TEXT,
    ADD COLUMN pin_updated_at       TIMESTAMPTZ,
    ADD COLUMN failed_pin_attempts  INT         NOT NULL DEFAULT 0,
    ADD COLUMN pin_locked_until     TIMESTAMPTZ;

CREATE UNIQUE INDEX users_rrn_key ON users (rrn) WHERE rrn IS NOT NULL;

-- A device has to be enrolled (by an admin via Zitadel) before it can sign
-- any RRN login request. The device_secret_hash is a SHA-256 of the base64
-- HMAC key shared with the device; the plaintext is only shown once at
-- enrollment.
CREATE TABLE pos_devices (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    serial_number      TEXT        NOT NULL UNIQUE,
    device_secret_hash TEXT        NOT NULL,
    label              TEXT        NOT NULL,
    registered_by      UUID        NOT NULL REFERENCES users(id),
    registered_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at       TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ
);

CREATE INDEX pos_devices_revoked_at_idx ON pos_devices (revoked_at);

-- Opaque refresh tokens for the RRN-issued JWTs. The token value itself is
-- never stored — only its SHA-256. Short-lived access tokens (8h) + 14d
-- refresh tokens.
CREATE TABLE pos_refresh_tokens (
    token_hash  TEXT        PRIMARY KEY,
    user_id     UUID        NOT NULL REFERENCES users(id),
    device_id   UUID        NOT NULL REFERENCES pos_devices(id),
    issued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX pos_refresh_tokens_user_idx ON pos_refresh_tokens (user_id);
CREATE INDEX pos_refresh_tokens_device_idx ON pos_refresh_tokens (device_id);
CREATE INDEX pos_refresh_tokens_expires_idx ON pos_refresh_tokens (expires_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS pos_refresh_tokens;
DROP TABLE IF EXISTS pos_devices;

DROP INDEX IF EXISTS users_rrn_key;
ALTER TABLE users
    DROP COLUMN IF EXISTS pin_locked_until,
    DROP COLUMN IF EXISTS failed_pin_attempts,
    DROP COLUMN IF EXISTS pin_updated_at,
    DROP COLUMN IF EXISTS pin_hash,
    DROP COLUMN IF EXISTS rrn;

-- +goose StatementEnd
