-- +goose Up
CREATE TABLE product_choice_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    min_selections INT NOT NULL DEFAULT 1,
    max_selections INT NOT NULL DEFAULT 1,
    sort_order INT NOT NULL DEFAULT 0,
    CHECK (min_selections >= 0),
    CHECK (max_selections >= 1),
    CHECK (min_selections <= max_selections)
);

CREATE INDEX idx_product_choice_groups_product_id ON product_choice_groups(product_id);

CREATE TABLE product_choice_group_translations (
    product_choice_group_id UUID NOT NULL REFERENCES product_choice_groups(id) ON DELETE CASCADE,
    locale VARCHAR(5) NOT NULL,
    name VARCHAR(255) NOT NULL,
    PRIMARY KEY (product_choice_group_id, locale)
);

ALTER TABLE product_choices
    ADD COLUMN choice_group_id UUID REFERENCES product_choice_groups(id) ON DELETE CASCADE;

WITH products_with_choices AS (
    SELECT DISTINCT product_id
    FROM product_choices
), inserted_groups AS (
    INSERT INTO product_choice_groups (product_id, min_selections, max_selections, sort_order)
    SELECT product_id, 1, 1, 0
    FROM products_with_choices
    RETURNING id, product_id
)
UPDATE product_choices pc
SET choice_group_id = ig.id
FROM inserted_groups ig
WHERE ig.product_id = pc.product_id;

INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name)
SELECT pcg.id, t.locale, t.name
FROM product_choice_groups pcg
CROSS JOIN (
    VALUES
        ('fr', 'Choix'),
        ('en', 'Choice'),
        ('zh', '选择')
) AS t(locale, name);

ALTER TABLE product_choices
    ALTER COLUMN choice_group_id SET NOT NULL;

ALTER TABLE order_product
    ADD COLUMN id UUID DEFAULT gen_random_uuid();

UPDATE order_product
SET id = gen_random_uuid()
WHERE id IS NULL;

ALTER TABLE order_product
    ALTER COLUMN id SET NOT NULL;

ALTER TABLE order_product
    DROP CONSTRAINT IF EXISTS pk_order_product;

ALTER TABLE order_product
    ADD CONSTRAINT pk_order_product PRIMARY KEY (id);

CREATE INDEX idx_order_product_order_id ON order_product(order_id);
CREATE INDEX idx_order_product_product_id ON order_product(product_id);

CREATE TABLE order_product_choices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_product_id UUID NOT NULL REFERENCES order_product(id) ON DELETE CASCADE,
    product_choice_group_id UUID NOT NULL REFERENCES product_choice_groups(id),
    product_choice_id UUID NOT NULL REFERENCES product_choices(id),
    quantity INT NOT NULL,
    CHECK (quantity > 0),
    UNIQUE (order_product_id, product_choice_id)
);

CREATE INDEX idx_order_product_choices_order_product_id ON order_product_choices(order_product_id);

-- +goose Down
DROP TABLE IF EXISTS order_product_choices;

DROP INDEX IF EXISTS idx_order_product_product_id;
DROP INDEX IF EXISTS idx_order_product_order_id;

ALTER TABLE order_product
    DROP CONSTRAINT IF EXISTS pk_order_product;

ALTER TABLE order_product
    ADD CONSTRAINT pk_order_product PRIMARY KEY (order_id, product_id);

ALTER TABLE order_product
    DROP COLUMN IF EXISTS id;

ALTER TABLE product_choices
    DROP COLUMN IF EXISTS choice_group_id;

DROP TABLE IF EXISTS product_choice_group_translations;
DROP TABLE IF EXISTS product_choice_groups;
