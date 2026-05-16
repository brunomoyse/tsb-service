-- +goose Up
-- Canonicalise existing user emails to lowercase + trimmed form so they match
-- the normalisation enforced by the application layer. Pre-existing rows from
-- the legacy auth migration or manual Zitadel imports may carry mixed-case
-- addresses, which would prevent the JIT email-fallback in
-- FindOrCreateByZitadelID from matching a freshly-lowercased JWT claim.
UPDATE users
SET email = lower(btrim(email))
WHERE email IS NOT NULL
  AND email <> lower(btrim(email));

-- +goose Down
-- Lowercasing is irreversible — the original capitalisation is not recorded.
-- This down is a no-op so a rollback does not error out.
SELECT 1;
