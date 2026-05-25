-- +goose Up
ALTER TABLE product_categories ADD COLUMN slug text;

UPDATE product_categories SET slug = 'menu-plateau'    WHERE id = '9e1992c3-2e26-4082-8219-06ca8da5d66f';
UPDATE product_categories SET slug = 'menu-bento-box'  WHERE id = '9e1992c3-b51e-4b9d-845c-ec88b640977a';
UPDATE product_categories SET slug = 'menu-lunch'      WHERE id = 'b72ff149-6d3a-45a2-9e52-f824e2c80727';
UPDATE product_categories SET slug = 'sushi'           WHERE id = '9e1992c4-37c7-493e-b69a-0b71cc7af146';
UPDATE product_categories SET slug = 'maki'            WHERE id = '9e1992c4-be65-4adf-8e18-6035219974ee';
UPDATE product_categories SET slug = 'gunkan'          WHERE id = '9e1992c5-4002-4797-ba9d-feb545add16e';
UPDATE product_categories SET slug = 'spring-roll'     WHERE id = '9e1992c5-bf91-406b-88a7-61dc600d3990';
UPDATE product_categories SET slug = 'california-roll' WHERE id = '9e1992c6-4378-48cd-be59-aa585d9f90fc';
UPDATE product_categories SET slug = 'temaki'          WHERE id = '9e1992c6-c71c-4a29-9457-2c0eaecab8c8';
UPDATE product_categories SET slug = 'masago-roll'     WHERE id = '9e1992c7-47c9-44f5-8065-389f83ee6f6a';
UPDATE product_categories SET slug = 'special-roll'    WHERE id = '9e1992c7-c630-47b9-9528-a3ebb9ed4f31';
UPDATE product_categories SET slug = 'chirashi'        WHERE id = '9e1992c8-49d8-4bd1-8811-4ce3978789b7';
UPDATE product_categories SET slug = 'sashimi'         WHERE id = '9e1992c8-c8fd-4974-803d-cd7f9c0c1e6d';
UPDATE product_categories SET slug = 'poke-bowl'       WHERE id = '9e1992c9-4c21-4723-a416-96550589eb7c';
UPDATE product_categories SET slug = 'ramen'           WHERE id = 'a2de2102-7930-4109-b162-ebb03585f6ac';
UPDATE product_categories SET slug = 'tokyo-hot'       WHERE id = '9e1992c9-d637-4dcc-ae10-bd9a445495b8';
UPDATE product_categories SET slug = 'accompagnement'  WHERE id = '9e1992ca-e360-4498-ab6a-62852f8f213b';
UPDATE product_categories SET slug = 'boisson'         WHERE id = '9e1992cb-66d5-4334-b6e6-669f37047773';
UPDATE product_categories SET slug = 'dessert'         WHERE id = 'efbd4ee3-671a-4568-bd50-d00dc38905cc';

ALTER TABLE product_categories ALTER COLUMN slug SET NOT NULL;
ALTER TABLE product_categories ADD CONSTRAINT product_categories_slug_key UNIQUE (slug);

-- +goose Down
ALTER TABLE product_categories DROP CONSTRAINT product_categories_slug_key;
ALTER TABLE product_categories DROP COLUMN slug;
