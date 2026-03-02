-- +goose Up
ALTER TABLE coupons ADD COLUMN max_uses_per_user INT;

CREATE TABLE coupon_users (
    coupon_id  UUID NOT NULL REFERENCES coupons(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    used_count INT NOT NULL DEFAULT 0,
    PRIMARY KEY (coupon_id, user_id)
);

-- +goose Down
DROP TABLE IF EXISTS coupon_users;
ALTER TABLE coupons DROP COLUMN IF EXISTS max_uses_per_user;
