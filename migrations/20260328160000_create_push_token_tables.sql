-- +goose Up

-- Live Activity push tokens (iOS only, per-order, 12h expiry)
CREATE TABLE live_activity_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    push_token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '12 hours'),
    UNIQUE(order_id, push_token)
);

CREATE INDEX idx_live_activity_tokens_order_id ON live_activity_tokens(order_id);

-- Device push tokens (iOS APNs + Android FCM, per-user)
CREATE TABLE device_push_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_token TEXT NOT NULL,
    platform     VARCHAR(10) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id, device_token)
);

CREATE INDEX idx_device_push_tokens_user_id ON device_push_tokens(user_id);

-- +goose Down
DROP TABLE IF EXISTS device_push_tokens;
DROP TABLE IF EXISTS live_activity_tokens;
