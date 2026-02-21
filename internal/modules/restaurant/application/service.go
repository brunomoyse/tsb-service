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
	UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error)
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

func (s *restaurantService) UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOpeningHours(ctx, hours)
}
