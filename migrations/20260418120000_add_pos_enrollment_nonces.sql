-- +goose Up
-- +goose StatementBegin
CREATE TABLE pos_enrollment_nonces (
    nonce      TEXT        PRIMARY KEY,
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_pos_enrollment_nonces_expires
    ON pos_enrollment_nonces (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS pos_enrollment_nonces;
-- +goose StatementEnd
