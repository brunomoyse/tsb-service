-- +goose Up

-- +goose StatementBegin
-- Canonicalise existing user emails to lowercase + trimmed form so they match
-- the normalisation enforced by the application layer. The update is split
-- into two passes:
--
--   1. Detect collisions where two rows differ only in casing — the unique
--      constraint users_email_unique would reject the UPDATE otherwise, and
--      goose would roll the release migration back. Emit a NOTICE so
--      operators see the affected rows and can merge them out-of-band.
--   2. Rewrite the rows that will not collide.
--
-- Rows left mixed-case after this migration are exactly the colliding ones,
-- which is the only safe default for an automated migration to take.
DO $$
DECLARE
    collision_rows TEXT;
    collision_count INT;
BEGIN
    SELECT count(*),
           string_agg(u1.id::text || ' (' || u1.email || ' vs ' || u2.email || ')', E'\n  ')
      INTO collision_count, collision_rows
    FROM users u1
    JOIN users u2
      ON u2.id <> u1.id
     AND u2.email = lower(btrim(u1.email))
    WHERE u1.email IS NOT NULL
      AND u1.email <> lower(btrim(u1.email));

    IF collision_count > 0 THEN
        RAISE NOTICE
            'lowercase email migration: skipping % rows because a lowercase duplicate already exists. Resolve manually:%  %',
            collision_count, E'\n', collision_rows;
    END IF;
END $$;
-- +goose StatementEnd

UPDATE users
SET email = lower(btrim(email))
WHERE email IS NOT NULL
  AND email <> lower(btrim(email))
  AND NOT EXISTS (
      SELECT 1
      FROM users dup
      WHERE dup.id <> users.id
        AND dup.email = lower(btrim(users.email))
  );

-- +goose Down
-- Lowercasing is irreversible — the original capitalisation is not recorded.
-- This down is a no-op so a rollback does not error out.
SELECT 1;
