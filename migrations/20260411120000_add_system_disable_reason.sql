-- +goose Up
-- Allow the circuit breaker (Phase C of the HubRise resilience plan)
-- to distinguish between admin-driven ordering disables and
-- system-driven disables (automatic shutoff during HubRise outages).
-- NULL = no system disable reason active (either ordering is enabled
-- or the admin manually disabled it).
ALTER TABLE restaurant_config ADD COLUMN system_disable_reason TEXT;

-- +goose Down
ALTER TABLE restaurant_config DROP COLUMN IF EXISTS system_disable_reason;
