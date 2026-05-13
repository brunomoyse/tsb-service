-- +goose Up
DROP TABLE IF EXISTS live_activity_tokens;

-- +goose Down
CREATE TABLE live_activity_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    push_token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '12 hours'),
    UNIQUE(order_id, push_token)
);

CREATE INDEX idx_live_activity_tokens_order_id ON live_activity_tokens(order_id);
