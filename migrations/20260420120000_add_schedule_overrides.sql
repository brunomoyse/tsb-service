-- +goose Up
CREATE TABLE restaurant_schedule_overrides (
    date        DATE PRIMARY KEY,
    closed      BOOLEAN NOT NULL DEFAULT false,
    schedule    JSONB,
    note        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (closed = true OR schedule IS NOT NULL)
);

-- +goose Down
DROP TABLE IF EXISTS restaurant_schedule_overrides;
