package infrastructure

import (
	"context"
	"encoding/json"
	"tsb-service/internal/modules/restaurant/domain"

	"github.com/jmoiron/sqlx"
)

type RestaurantRepository struct {
	db *sqlx.DB
}

func NewRestaurantRepository(db *sqlx.DB) domain.RestaurantRepository {
	return &RestaurantRepository{db: db}
}

func (r *RestaurantRepository) GetConfig(ctx context.Context) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.db.GetContext(ctx, &config,
		`SELECT ordering_enabled, opening_hours, updated_at FROM restaurant_config WHERE id = TRUE`)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.db.GetContext(ctx, &config,
		`UPDATE restaurant_config SET ordering_enabled = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING ordering_enabled, opening_hours, updated_at`, enabled)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.db.GetContext(ctx, &config,
		`UPDATE restaurant_config SET opening_hours = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING ordering_enabled, opening_hours, updated_at`, hours)
	if err != nil {
		return nil, err
	}
	return &config, nil
}
