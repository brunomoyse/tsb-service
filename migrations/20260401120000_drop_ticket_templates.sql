-- +goose Up
ALTER TABLE restaurant_config DROP COLUMN IF EXISTS ticket_templates;

-- +goose Down
ALTER TABLE restaurant_config
ADD COLUMN ticket_templates JSONB NOT NULL DEFAULT '{}'::jsonb;
