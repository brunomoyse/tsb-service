-- +goose Up
ALTER TABLE orders
    ADD COLUMN street_id text,
    ADD COLUMN street_name text,
    ADD COLUMN house_number text,
    ADD COLUMN box_number text,
    ADD COLUMN municipality_name text,
    ADD COLUMN postcode text,
    ADD COLUMN address_distance double precision,
    ADD COLUMN is_manual_address boolean NOT NULL DEFAULT false;

-- +goose StatementBegin
-- Backfill existing delivery orders (only if addresses table exists)
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'addresses') THEN
        UPDATE orders o
        SET
            street_id = a.street_id,
            street_name = a.streetname_fr,
            house_number = a.house_number,
            box_number = a.box_number,
            municipality_name = a.municipality_name_fr,
            postcode = a.postcode,
            address_distance = COALESCE(ad.distance, 10000),
            is_manual_address = false
        FROM addresses a
        LEFT JOIN address_distance ad ON a.address_id = ad.address_id
        WHERE o.address_id = a.address_id::uuid;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE orders
    DROP COLUMN IF EXISTS street_id,
    DROP COLUMN IF EXISTS street_name,
    DROP COLUMN IF EXISTS house_number,
    DROP COLUMN IF EXISTS box_number,
    DROP COLUMN IF EXISTS municipality_name,
    DROP COLUMN IF EXISTS postcode,
    DROP COLUMN IF EXISTS address_distance,
    DROP COLUMN IF EXISTS is_manual_address;
