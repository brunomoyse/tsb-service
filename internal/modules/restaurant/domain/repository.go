package domain

import (
	"context"
	"encoding/json"
)

type RestaurantRepository interface {
	GetConfig(ctx context.Context) (*RestaurantConfig, error)
	UpdateOrderingEnabled(ctx context.Context, enabled bool) (*RestaurantConfig, error)
	UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*RestaurantConfig, error)
}
