package domain

import (
	"context"
	"encoding/json"
	"time"
)

type RestaurantRepository interface {
	GetConfig(ctx context.Context) (*RestaurantConfig, error)
	UpdateOrderingEnabled(ctx context.Context, enabled bool) (*RestaurantConfig, error)
	UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*RestaurantConfig, error)
	UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*RestaurantConfig, error)
	UpdatePreparationMinutes(ctx context.Context, minutes int) (*RestaurantConfig, error)
}

type ScheduleOverrideRepository interface {
	List(ctx context.Context, from, to time.Time) ([]*ScheduleOverride, error)
	ListFromDate(ctx context.Context, from time.Time) ([]*ScheduleOverride, error)
	Get(ctx context.Context, date time.Time) (*ScheduleOverride, error)
	Upsert(ctx context.Context, override *ScheduleOverride) (*ScheduleOverride, error)
	Delete(ctx context.Context, date time.Time) error
}
