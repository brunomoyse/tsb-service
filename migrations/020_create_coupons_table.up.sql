CREATE TABLE coupons (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            VARCHAR(50) NOT NULL UNIQUE,
    discount_type   VARCHAR(20) NOT NULL CHECK (discount_type IN ('percentage', 'fixed')),
    discount_value  NUMERIC(10,2) NOT NULL CHECK (discount_value > 0),
    min_order_amount NUMERIC(10,2),
    max_uses        INT,
    used_count      INT NOT NULL DEFAULT 0,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    valid_from      TIMESTAMPTZ,
    valid_until     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_coupons_code ON coupons (code);
