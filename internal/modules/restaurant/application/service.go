package application

import (
	"context"
	"encoding/json"
	"time"
	"tsb-service/internal/modules/restaurant/domain"
)

type RestaurantService interface {
	GetConfig(ctx context.Context) (*domain.RestaurantConfig, error)
	IsOrderingAllowed(ctx context.Context) (bool, error)
	IsDevMode() bool
	UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error)
	// SetOrderingEnabledBySystem is used by the HubRise circuit
	// breaker. Passing a non-empty reason records WHY ordering was
	// disabled automatically. Passing an empty reason is equivalent
	// to a manual admin clear.
	SetOrderingEnabledBySystem(ctx context.Context, enabled bool, reason string) (*domain.RestaurantConfig, error)
	UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error)
	UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error)
}

type restaurantService struct {
	repo    domain.RestaurantRepository
	devMode bool
}

func NewRestaurantService(repo domain.RestaurantRepository, devMode bool) RestaurantService {
	return &restaurantService{repo: repo, devMode: devMode}
}

func (s *restaurantService) GetConfig(ctx context.Context) (*domain.RestaurantConfig, error) {
	return s.repo.GetConfig(ctx)
}

func (s *restaurantService) IsOrderingAllowed(ctx context.Context) (bool, error) {
	if s.devMode {
		return true, nil
	}
	config, err := s.repo.GetConfig(ctx)
	if err != nil {
		return false, err
	}
	return config.IsOrderingAllowed(time.Now()), nil
}

// IsDevMode returns whether the service is running in development mode.
func (s *restaurantService) IsDevMode() bool {
	return s.devMode
}

func (s *restaurantService) UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOrderingEnabled(ctx, enabled)
}

func (s *restaurantService) SetOrderingEnabledBySystem(
	ctx context.Context, enabled bool, reason string,
) (*domain.RestaurantConfig, error) {
	var reasonPtr *string
	if !enabled && reason != "" {
		reasonPtr = &reason
	}
	return s.repo.UpdateOrderingEnabledWithReason(ctx, enabled, reasonPtr)
}

func (s *restaurantService) UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOpeningHours(ctx, hours)
}

func (s *restaurantService) UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOrderingHours(ctx, hours)
}
