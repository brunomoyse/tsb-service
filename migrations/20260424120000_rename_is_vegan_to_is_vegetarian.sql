-- +goose Up
ALTER TABLE public.products
    RENAME COLUMN is_vegan TO is_vegetarian;

-- +goose Down
ALTER TABLE public.products
    RENAME COLUMN is_vegetarian TO is_vegan;
