package hubrisewire

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/timezone"
)

// fakeRestaurant answers the three calls the schedule adapter makes.
type fakeRestaurant struct {
	restaurantApplication.RestaurantService
	dev       bool
	config    *restaurantDomain.RestaurantConfig
	configErr error
	overrides []*restaurantDomain.ScheduleOverride
	listErr   error
	from, to  time.Time
}

func (f *fakeRestaurant) IsDevMode() bool { return f.dev }
func (f *fakeRestaurant) GetConfig(context.Context) (*restaurantDomain.RestaurantConfig, error) {
	return f.config, f.configErr
}
func (f *fakeRestaurant) ListOverrides(_ context.Context, from, to time.Time) ([]*restaurantDomain.ScheduleOverride, error) {
	f.from, f.to = from, to
	return f.overrides, f.listErr
}

func TestOrderingHoursCoverTheInstantNotTheSwitch(t *testing.T) {
	day := time.Date(2026, 10, 26, 0, 0, 0, 0, timezone.Location) // a Monday
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	hours := json.RawMessage(`{"monday":{"open":"11:00","close":"22:00"}}`)
	// Ordering is paused (OrderingEnabled false): the hours still decide.
	paused := &restaurantDomain.RestaurantConfig{OrderingEnabled: false, OpeningHours: hours}

	t.Run("inside and outside the hours", func(t *testing.T) {
		svc := &fakeRestaurant{config: paused}
		o := orderingHours{svc}
		open, err := o.OrderingOpenAt(t.Context(), at(21, 58))
		require.NoError(t, err)
		assert.True(t, open, "pushed just before closing")
		open, err = o.OrderingOpenAt(t.Context(), at(22, 3))
		require.NoError(t, err)
		assert.False(t, open)
		assert.Equal(t, day, svc.from, "the overrides of the day of the instant")
		assert.Equal(t, day, svc.to)
	})
	t.Run("a closed day override wins over the weekly hours", func(t *testing.T) {
		svc := &fakeRestaurant{config: paused, overrides: []*restaurantDomain.ScheduleOverride{{Date: day, Closed: true}}}
		open, err := orderingHours{svc}.OrderingOpenAt(t.Context(), at(12, 0))
		require.NoError(t, err)
		assert.False(t, open)
	})
	t.Run("ordering hours of their own replace the opening hours", func(t *testing.T) {
		cfg := &restaurantDomain.RestaurantConfig{OpeningHours: hours, OrderingHours: json.RawMessage(`{"monday":{"open":"18:00","close":"21:00"}}`)}
		open, err := orderingHours{&fakeRestaurant{config: cfg}}.OrderingOpenAt(t.Context(), at(12, 0))
		require.NoError(t, err)
		assert.False(t, open)
	})
	t.Run("a development server is always open", func(t *testing.T) {
		open, err := orderingHours{&fakeRestaurant{dev: true}}.OrderingOpenAt(t.Context(), at(3, 0))
		require.NoError(t, err)
		assert.True(t, open)
	})
	t.Run("failures are reported", func(t *testing.T) {
		boom := errors.New("db down")
		_, err := orderingHours{&fakeRestaurant{configErr: boom}}.OrderingOpenAt(t.Context(), at(12, 0))
		assert.ErrorIs(t, err, boom)
		_, err = orderingHours{&fakeRestaurant{config: paused, listErr: boom}}.OrderingOpenAt(t.Context(), at(12, 0))
		assert.ErrorIs(t, err, boom)
	})
}

func TestInstallGivesTheReconcilerTheOrderingHours(t *testing.T) {
	r, _, _ := testResolver()
	r.RestaurantService = &fakeRestaurant{}
	rt, err := Install(Options{Module: testModule(t), Resolver: r, APIBaseURL: "https://shop.example.test/api/v1", Images: noImages{}})
	require.NoError(t, err)
	require.NotNil(t, rt)
	t.Cleanup(rt.Stop)
}
