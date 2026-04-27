-- +goose Up
-- +goose StatementBegin

-- The POS Android app no longer authenticates a human user — the device IS
-- the principal. Drop the RRN/PIN columns and POS-only staff/refresh-token
-- tables. `pos_devices` stays (now seeded manually for the single device).

DROP TABLE IF EXISTS pos_refresh_tokens;
DROP TABLE IF EXISTS pos_staff;
DROP TABLE IF EXISTS pos_enrollment_nonces;

DROP INDEX IF EXISTS users_rrn_key;

ALTER TABLE users
    DROP COLUMN IF EXISTS pin_locked_until,
    DROP COLUMN IF EXISTS failed_pin_attempts,
    DROP COLUMN IF EXISTS pin_updated_at,
    DROP COLUMN IF EXISTS pin_hash,
    DROP COLUMN IF EXISTS rrn;

-- registered_by used to point at the Zitadel admin who enrolled the device.
-- With manual seeding there's no admin in the loop, so make the column
-- nullable to support `INSERT INTO pos_devices (...) VALUES (...)` without it.
ALTER TABLE pos_devices
    ALTER COLUMN registered_by DROP NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE pos_devices
    ALTER COLUMN registered_by SET NOT NULL;

ALTER TABLE users
    ADD COLUMN rrn                  VARCHAR(11),
    ADD COLUMN pin_hash             TEXT,
    ADD COLUMN pin_updated_at       TIMESTAMPTZ,
    ADD COLUMN failed_pin_attempts  INT         NOT NULL DEFAULT 0,
    ADD COLUMN pin_locked_until     TIMESTAMPTZ;

CREATE UNIQUE INDEX users_rrn_key ON users (rrn) WHERE rrn IS NOT NULL;

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

CREATE TABLE pos_staff (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name        TEXT        NOT NULL,
    rrn_hash            TEXT        NOT NULL UNIQUE,
    pin_hash            TEXT        NOT NULL,
    failed_pin_attempts INT         NOT NULL DEFAULT 0,
    pin_locked_until    TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pos_enrollment_nonces (
    nonce      TEXT        PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose StatementEnd
