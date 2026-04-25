-- +goose Up
ALTER TABLE order_product
    ADD COLUMN vat_rate_applied NUMERIC(5,2);

UPDATE order_product op
SET vat_rate_applied = CASE
    WHEN p.vat_category = 'beverage' THEN 21.00
    WHEN p.vat_category = 'food' THEN 6.00
    ELSE 0.00
END
FROM products p
WHERE p.id = op.product_id;

ALTER TABLE order_product
    ALTER COLUMN vat_rate_applied SET NOT NULL;

-- +goose Down
ALTER TABLE order_product
    DROP COLUMN IF EXISTS vat_rate_applied;
