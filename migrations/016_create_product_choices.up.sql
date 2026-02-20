-- Product choices (e.g., sauce selection for tataki)
CREATE TABLE product_choices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    price_modifier DECIMAL(10, 2) NOT NULL DEFAULT 0,
    sort_order INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_product_choices_product_id ON product_choices(product_id);

-- Translations for product choices (follows existing pattern)
CREATE TABLE product_choice_translations (
    product_choice_id UUID NOT NULL REFERENCES product_choices(id) ON DELETE CASCADE,
    locale VARCHAR(5) NOT NULL,
    name VARCHAR(255) NOT NULL,
    PRIMARY KEY (product_choice_id, locale)
);

-- Track which choice was selected in an order
ALTER TABLE order_products ADD COLUMN product_choice_id UUID REFERENCES product_choices(id);
