-- +goose Up
CREATE TABLE restaurant_config (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),  -- ensures single row
    ordering_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    opening_hours JSONB NOT NULL DEFAULT '{
        "monday":    {"open": "11:00", "close": "14:00", "dinnerOpen": "17:00", "dinnerClose": "22:00"},
        "tuesday":   {"open": "11:00", "close": "14:00", "dinnerOpen": "17:00", "dinnerClose": "22:00"},
        "wednesday": null,
        "thursday":  {"open": "11:00", "close": "14:00", "dinnerOpen": "17:00", "dinnerClose": "22:00"},
        "friday":    {"open": "11:00", "close": "14:00", "dinnerOpen": "17:00", "dinnerClose": "22:00"},
        "saturday":  {"open": "11:00", "close": "14:00", "dinnerOpen": "17:00", "dinnerClose": "22:00"},
        "sunday":    {"open": "11:00", "close": "14:00", "dinnerOpen": "17:00", "dinnerClose": "22:00"}
    }'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Insert default config row
INSERT INTO restaurant_config (id) VALUES (TRUE);

-- +goose Down
DROP TABLE IF EXISTS restaurant_config;
