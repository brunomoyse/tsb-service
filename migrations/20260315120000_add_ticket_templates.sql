-- +goose Up
ALTER TABLE restaurant_config
ADD COLUMN ticket_templates JSONB NOT NULL DEFAULT '{
    "delivery": {
        "sectionOrder": ["header", "address", "customer", "timing", "payment", "items", "extras", "notes"],
        "sections": {
            "header":   { "enabled": true, "restaurantName": "Tokyo Sushi Bar" },
            "address":  { "enabled": true },
            "customer": { "enabled": true },
            "timing":   { "enabled": true },
            "payment":  { "enabled": true },
            "items":    { "enabled": true },
            "extras":   { "enabled": true },
            "notes":    { "enabled": true }
        }
    },
    "kitchen": {
        "sectionOrder": ["header", "orderInfo", "items", "extras", "notes"],
        "sections": {
            "header":    { "enabled": true, "title": "*** CUISINE ***" },
            "orderInfo": { "enabled": true },
            "items":     { "enabled": true },
            "extras":    { "enabled": true },
            "notes":     { "enabled": true }
        }
    }
}'::jsonb;

-- +goose Down
ALTER TABLE restaurant_config DROP COLUMN IF EXISTS ticket_templates;
