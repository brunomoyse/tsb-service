-- +goose Up
CREATE TABLE address_cache (
    place_id              TEXT PRIMARY KEY,
    formatted_address     TEXT NOT NULL,
    lat                   DOUBLE PRECISION NOT NULL,
    lng                   DOUBLE PRECISION NOT NULL,
    street_name           TEXT,
    house_number          TEXT,
    box_number            TEXT,
    postcode              TEXT,
    municipality_name     TEXT,
    country_code          TEXT NOT NULL DEFAULT 'BE',
    distance_meters       INTEGER NOT NULL,
    duration_seconds      INTEGER NOT NULL,
    raw_place_details     JSONB,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    refreshed_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_address_cache_postcode ON address_cache(postcode);

ALTER TABLE orders
    ADD COLUMN address_place_id TEXT,
    ADD COLUMN address_lat DOUBLE PRECISION,
    ADD COLUMN address_lng DOUBLE PRECISION;

ALTER TABLE users
    ADD COLUMN default_place_id TEXT;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS default_place_id;
ALTER TABLE orders
    DROP COLUMN IF EXISTS address_place_id,
    DROP COLUMN IF EXISTS address_lat,
    DROP COLUMN IF EXISTS address_lng;
DROP TABLE IF EXISTS address_cache;
