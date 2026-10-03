-- +goose Up
-- Append-only record of privileged actions: every GraphQL mutation run by an
-- admin or the POS device, plus staff security changes (MFA enroll/remove).
CREATE TABLE staff_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_kind     TEXT NOT NULL CHECK (actor_kind IN ('admin', 'pos')),
    actor_id       TEXT NOT NULL, -- app user UUID (admin) or POS device UUID
    zitadel_sub    TEXT,
    action         TEXT NOT NULL, -- e.g. graphql:updateOrder, mfa.totp.enable
    operation_name TEXT,
    variables      JSONB,
    success        BOOLEAN NOT NULL,
    error          TEXT,
    request_id     TEXT,
    ip             TEXT
);

CREATE INDEX staff_audit_log_created_at_idx ON staff_audit_log (created_at DESC);
CREATE INDEX staff_audit_log_actor_idx ON staff_audit_log (actor_id, created_at DESC);

-- Rows are evidence: the application may insert but never rewrite history.
-- +goose StatementBegin
CREATE FUNCTION staff_audit_log_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'staff_audit_log is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER staff_audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON staff_audit_log
    FOR EACH ROW EXECUTE FUNCTION staff_audit_log_immutable();

-- +goose Down
DROP TRIGGER staff_audit_log_no_update_delete ON staff_audit_log;
DROP FUNCTION staff_audit_log_immutable();
DROP TABLE staff_audit_log;
