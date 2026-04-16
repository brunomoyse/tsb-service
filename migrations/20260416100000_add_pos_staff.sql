-- +goose Up
CREATE TABLE pos_staff (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name        TEXT        NOT NULL,
    rrn_hash            TEXT        NOT NULL UNIQUE,
    pin_hash            TEXT        NOT NULL,
    failed_pin_attempts INT         NOT NULL DEFAULT 0,
    pin_locked_until    TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE pos_staff;
