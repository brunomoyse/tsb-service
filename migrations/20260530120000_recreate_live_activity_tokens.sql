-- +goose Up
-- Re-introduce per-order iOS Live Activity push tokens (dropped in
-- 20260513120000). Used to send ActivityKit "liveactivity" pushes that update
-- the Lock Screen / Dynamic Island while the app is backgrounded.
CREATE TABLE live_activity_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    push_token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '12 hours'),
    UNIQUE(order_id, push_token)
);

CREATE INDEX idx_live_activity_tokens_order_id ON live_activity_tokens(order_id);

-- +goose Down
DROP TABLE IF EXISTS live_activity_tokens;
