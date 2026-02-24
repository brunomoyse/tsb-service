-- +goose Up
ALTER TABLE public.products ADD COLUMN is_spicy boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE public.products DROP COLUMN is_spicy;
