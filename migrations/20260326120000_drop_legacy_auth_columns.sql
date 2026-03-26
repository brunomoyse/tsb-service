-- +goose Up
-- Drop legacy authentication columns that are now managed by Zitadel OIDC.
-- password_hash, salt: passwords managed by Zitadel
-- remember_token: unused since cookie-based JWT removal
-- google_id: IdP linking managed by Zitadel
-- email_verified_at: email verification tracked by Zitadel

ALTER TABLE users
    DROP COLUMN IF EXISTS password_hash,
    DROP COLUMN IF EXISTS salt,
    DROP COLUMN IF EXISTS remember_token,
    DROP COLUMN IF EXISTS google_id,
    DROP COLUMN IF EXISTS email_verified_at;

-- +goose Down
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS password_hash TEXT,
    ADD COLUMN IF NOT EXISTS salt TEXT,
    ADD COLUMN IF NOT EXISTS remember_token TEXT,
    ADD COLUMN IF NOT EXISTS google_id TEXT,
    ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
