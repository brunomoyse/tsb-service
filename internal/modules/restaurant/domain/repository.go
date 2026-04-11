package domain

import (
	"context"
	"encoding/json"
)

type RestaurantRepository interface {
	GetConfig(ctx context.Context) (*RestaurantConfig, error)
	UpdateOrderingEnabled(ctx context.Context, enabled bool) (*RestaurantConfig, error)
	// UpdateOrderingEnabledWithReason is used by the Phase C circuit
	// breaker: it records why the system disabled ordering, so
	// tsb-core can render a targeted "contact us" message and
	// tsb-dashboard can surface the reason in the health banner.
	UpdateOrderingEnabledWithReason(ctx context.Context, enabled bool, reason *string) (*RestaurantConfig, error)
	UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*RestaurantConfig, error)
	UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*RestaurantConfig, error)
}
