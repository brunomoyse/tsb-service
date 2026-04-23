-- +goose Up
-- Reject any existing or future negative price modifier on product_choices.
-- A negative value would reduce the effective unit price in the order pricing
-- path (internal/api/graphql/resolver/order.go), so the DB-level CHECK is the
-- authoritative gate; application validation is defense in depth.
UPDATE product_choices SET price_modifier = 0 WHERE price_modifier < 0;
ALTER TABLE product_choices
    ADD CONSTRAINT product_choices_price_modifier_nonneg
        CHECK (price_modifier >= 0);

-- +goose Down
ALTER TABLE product_choices
    DROP CONSTRAINT IF EXISTS product_choices_price_modifier_nonneg;
