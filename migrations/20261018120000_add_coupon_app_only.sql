-- +goose Up
-- An app-only coupon (e.g. a launch promo for the iOS/Android app) is refused on
-- orders placed from the website. The channel comes from the OIDC client the
-- customer's access token was issued to, never from the request body.
ALTER TABLE coupons ADD COLUMN app_only BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE coupons DROP COLUMN app_only;
