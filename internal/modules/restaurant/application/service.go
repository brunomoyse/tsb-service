package application

import (
	"context"
	"encoding/json"
	"time"

	"tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/timezone"
)

type RestaurantService interface {
	GetConfig(ctx context.Context) (*domain.RestaurantConfig, error)
	GetConfigWithOverrides(ctx context.Context) (*domain.RestaurantConfig, map[string]*domain.ScheduleOverride, error)
	IsOrderingAllowed(ctx context.Context) (bool, error)
	IsDevMode() bool
	UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error)
	UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error)
	UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error)
	UpdatePreparationMinutes(ctx context.Context, minutes int) (*domain.RestaurantConfig, error)

	ListOverrides(ctx context.Context, from, to time.Time) ([]*domain.ScheduleOverride, error)
	UpsertOverride(ctx context.Context, date time.Time, closed bool, schedule json.RawMessage, note *string) (*domain.ScheduleOverride, error)
	DeleteOverride(ctx context.Context, date time.Time) error
}

type restaurantService struct {
	repo          domain.RestaurantRepository
	overrideRepo  domain.ScheduleOverrideRepository
	devMode       bool
	overrideLookahead time.Duration
}

func NewRestaurantService(repo domain.RestaurantRepository, overrideRepo domain.ScheduleOverrideRepository, devMode bool) RestaurantService {
	return &restaurantService{
		repo:              repo,
		overrideRepo:      overrideRepo,
		devMode:           devMode,
		overrideLookahead: 7 * 24 * time.Hour,
	}
}

func (s *restaurantService) GetConfig(ctx context.Context) (*domain.RestaurantConfig, error) {
	return s.repo.GetConfig(ctx)
}

// GetConfigWithOverrides fetches both the config and overrides relevant
// to "today and the next 7 days" — enough for all current consumers
// (IsCurrentlyOpen, availableSlotsToday, nextOpeningAt).
func (s *restaurantService) GetConfigWithOverrides(ctx context.Context) (*domain.RestaurantConfig, map[string]*domain.ScheduleOverride, error) {
	config, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := timezone.In(time.Now())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := today.Add(s.overrideLookahead)

	overrides, err := s.overrideRepo.List(ctx, today, to)
	if err != nil {
		return nil, nil, err
	}
	m := make(map[string]*domain.ScheduleOverride, len(overrides))
	for _, ov := range overrides {
		m[ov.DateKey()] = ov
	}
	return config, m, nil
}

func (s *restaurantService) IsOrderingAllowed(ctx context.Context) (bool, error) {
	if s.devMode {
		return true, nil
	}
	config, overrides, err := s.GetConfigWithOverrides(ctx)
	if err != nil {
		return false, err
	}
	return config.IsOrderingAllowed(time.Now(), overrides), nil
}

func (s *restaurantService) IsDevMode() bool {
	return s.devMode
}

func (s *restaurantService) UpdateOrderingEnabled(ctx context.Context, enabled bool) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOrderingEnabled(ctx, enabled)
}

func (s *restaurantService) UpdateOpeningHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOpeningHours(ctx, hours)
}

func (s *restaurantService) UpdateOrderingHours(ctx context.Context, hours json.RawMessage) (*domain.RestaurantConfig, error) {
	return s.repo.UpdateOrderingHours(ctx, hours)
}

func (s *restaurantService) UpdatePreparationMinutes(ctx context.Context, minutes int) (*domain.RestaurantConfig, error) {
	return s.repo.UpdatePreparationMinutes(ctx, minutes)
}

func (s *restaurantService) ListOverrides(ctx context.Context, from, to time.Time) ([]*domain.ScheduleOverride, error) {
	return s.overrideRepo.List(ctx, from, to)
}

func (s *restaurantService) UpsertOverride(ctx context.Context, date time.Time, closed bool, schedule json.RawMessage, note *string) (*domain.ScheduleOverride, error) {
	ov := &domain.ScheduleOverride{
		Date:     date,
		Closed:   closed,
		Schedule: schedule,
		Note:     note,
	}
	return s.overrideRepo.Upsert(ctx, ov)
}

func (s *restaurantService) DeleteOverride(ctx context.Context, date time.Time) error {
	return s.overrideRepo.Delete(ctx, date)
}
