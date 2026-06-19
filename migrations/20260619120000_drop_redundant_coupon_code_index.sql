-- +goose Up
-- The coupons.code column already carries a UNIQUE constraint, which Postgres
-- backs with its own index. The separate idx_coupons_code is therefore
-- redundant (extra write cost, no read benefit) — drop it.
DROP INDEX IF EXISTS idx_coupons_code;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_coupons_code ON coupons (code);
