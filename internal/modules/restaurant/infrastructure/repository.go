package infrastructure

import (
	"context"
	"encoding/json"
	"tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/db"
)

const configColumns = `ordering_enabled, opening_hours, ordering_hours, preparation_minutes, updated_at`

type RestaurantRepository struct {
	pool *db.DBPool
}

func NewRestaurantRepository(pool *db.DBPool) domain.RestaurantRepository {
	return &RestaurantRepository{pool: pool}
}

func (r *RestaurantRepository) GetConfig(ctx context.Context) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`SELECT `+configColumns+` FROM restaurant_config WHERE id = TRUE`)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config SET ordering_enabled = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING `+configColumns, enabled)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config SET opening_hours = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING `+configColumns, hours)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config SET ordering_hours = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING `+configColumns, hours)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdatePreparationMinutes(ctx context.Context, minutes int) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config SET preparation_minutes = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING `+configColumns, minutes)
	if err != nil {
		return nil, err
	}
	return &config, nil
}
