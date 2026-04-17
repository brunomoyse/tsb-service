package infrastructure

import (
	"context"
	"encoding/json"
	"tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/db"
)

// selectAll is the column list returned on every read / RETURNING.
// Kept in one place so adding a new column (e.g. system_disable_reason
// in Phase C) stays a one-line change.
const selectAll = `ordering_enabled, system_disable_reason, opening_hours, ordering_hours, updated_at`

type RestaurantRepository struct {
	pool *db.DBPool
}

func NewRestaurantRepository(pool *db.DBPool) domain.RestaurantRepository {
	return &RestaurantRepository{pool: pool}
}

func (r *RestaurantRepository) GetConfig(ctx context.Context) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`SELECT `+selectAll+` FROM restaurant_config WHERE id = TRUE`)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// UpdateOrderingEnabled is the admin-facing manual toggle. It clears
// any system_disable_reason so that a previously system-disabled
// restaurant comes back fully enabled when the admin flips it on.
func (r *RestaurantRepository) UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config
		 SET ordering_enabled = $1,
		     system_disable_reason = NULL,
		     updated_at = NOW()
		 WHERE id = TRUE
		 RETURNING `+selectAll, enabled)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// UpdateOrderingEnabledWithReason is the system-facing toggle used
// by the HubRise circuit breaker. Passing a non-nil reason records
// WHY the system disabled ordering. Passing nil is equivalent to
// UpdateOrderingEnabled (admin manual clear).
func (r *RestaurantRepository) UpdateOrderingEnabledWithReason(
	ctx context.Context, enabled bool, reason *string,
) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config
		 SET ordering_enabled = $1,
		     system_disable_reason = $2,
		     updated_at = NOW()
		 WHERE id = TRUE
		 RETURNING `+selectAll, enabled, reason)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config SET opening_hours = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING `+selectAll, hours)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *RestaurantRepository) UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	var config domain.RestaurantConfig
	err := r.pool.ForContext(ctx).GetContext(ctx, &config,
		`UPDATE restaurant_config SET ordering_hours = $1, updated_at = NOW() WHERE id = TRUE
		 RETURNING `+selectAll, hours)
	if err != nil {
		return nil, err
	}
	return &config, nil
}
