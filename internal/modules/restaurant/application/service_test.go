package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"tsb-service/pkg/timezone/timezonetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/timezone"
)

type fakeRepo struct {
	cfg    *domain.RestaurantConfig
	err    error
	calls  []string
	hours  json.RawMessage
	minute int
	enable bool
}

func (f *fakeRepo) GetConfig(context.Context) (*domain.RestaurantConfig, error) {
	f.calls = append(f.calls, "get")
	return f.cfg, f.err
}
func (f *fakeRepo) UpdateOrderingEnabled(_ context.Context, e bool) (*domain.RestaurantConfig, error) {
	f.enable = e
	return f.cfg, f.err
}
func (f *fakeRepo) UpdateOpeningHours(_ context.Context, h json.RawMessage) (*domain.RestaurantConfig, error) {
	f.calls, f.hours = append(f.calls, "opening"), h
	return f.cfg, f.err
}
func (f *fakeRepo) UpdateOrderingHours(_ context.Context, h json.RawMessage) (*domain.RestaurantConfig, error) {
	f.calls, f.hours = append(f.calls, "ordering"), h
	return f.cfg, f.err
}
func (f *fakeRepo) UpdatePreparationMinutes(_ context.Context, m int) (*domain.RestaurantConfig, error) {
	f.minute = m
	return f.cfg, f.err
}

type fakeOverrides struct {
	list       []*domain.ScheduleOverride
	listErr    error
	from, to   time.Time
	upserted   *domain.ScheduleOverride
	upsertErr  error
	deleted    time.Time
	deleteErr  error
	listCalled bool
}

func (f *fakeOverrides) List(_ context.Context, from, to time.Time) ([]*domain.ScheduleOverride, error) {
	f.listCalled, f.from, f.to = true, from, to
	return f.list, f.listErr
}
func (f *fakeOverrides) ListFromDate(context.Context, time.Time) ([]*domain.ScheduleOverride, error) {
	return nil, nil
}
func (f *fakeOverrides) Get(context.Context, time.Time) (*domain.ScheduleOverride, error) {
	return nil, nil
}
func (f *fakeOverrides) Upsert(_ context.Context, ov *domain.ScheduleOverride) (*domain.ScheduleOverride, error) {
	f.upserted = ov
	return ov, f.upsertErr
}
func (f *fakeOverrides) Delete(_ context.Context, d time.Time) error {
	f.deleted = d
	return f.deleteErr
}

func alwaysOpen(enabled bool) *domain.RestaurantConfig {
	return &domain.RestaurantConfig{
		OrderingEnabled:    enabled,
		OpeningHours:       json.RawMessage(`{"monday":{"open":"00:00","close":"24:00"},"tuesday":{"open":"00:00","close":"24:00"},"wednesday":{"open":"00:00","close":"24:00"},"thursday":{"open":"00:00","close":"24:00"},"friday":{"open":"00:00","close":"24:00"},"saturday":{"open":"00:00","close":"24:00"},"sunday":{"open":"00:00","close":"24:00"}}`),
		PreparationMinutes: 20,
	}
}

func TestGetConfigWithOverrides(t *testing.T) {
	timezonetest.SkipNearMidnight(t, 10*time.Second) // "today" below must be the day the service sees
	today := timezone.In(time.Now())
	midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())

	t.Run("overrides are fetched for today plus a week and keyed by date", func(t *testing.T) {
		ov := &domain.ScheduleOverride{Date: midnight.AddDate(0, 0, 2), Closed: true}
		repo, ovs := &fakeRepo{cfg: alwaysOpen(true)}, &fakeOverrides{list: []*domain.ScheduleOverride{ov}}
		cfg, m, err := NewRestaurantService(repo, ovs, false).GetConfigWithOverrides(t.Context())

		require.NoError(t, err)
		assert.Same(t, repo.cfg, cfg)
		assert.Same(t, ov, m[ov.DateKey()])
		assert.Len(t, m, 1)
		// The window starts at local midnight and spans 7 days (DST-safe: compare instants).
		assert.True(t, ovs.from.Equal(midnight), "from=%s want %s", ovs.from, midnight)
		assert.True(t, ovs.to.Equal(midnight.Add(7*24*time.Hour)))
	})

	t.Run("a config failure is returned without querying overrides", func(t *testing.T) {
		boom := errors.New("db down")
		ovs := &fakeOverrides{}
		_, _, err := NewRestaurantService(&fakeRepo{err: boom}, ovs, false).GetConfigWithOverrides(t.Context())
		assert.ErrorIs(t, err, boom)
		assert.False(t, ovs.listCalled)
	})

	t.Run("an override failure is returned", func(t *testing.T) {
		boom := errors.New("db down")
		_, _, err := NewRestaurantService(&fakeRepo{cfg: alwaysOpen(true)}, &fakeOverrides{listErr: boom}, false).GetConfigWithOverrides(t.Context())
		assert.ErrorIs(t, err, boom)
	})
}

func TestIsOrderingAllowed(t *testing.T) {
	t.Run("dev mode always allows ordering without touching the database", func(t *testing.T) {
		repo := &fakeRepo{err: errors.New("must not be called")}
		svc := NewRestaurantService(repo, &fakeOverrides{}, true)
		ok, err := svc.IsOrderingAllowed(t.Context())
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, svc.IsDevMode())
		assert.Empty(t, repo.calls)
	})

	t.Run("enabled and inside hours", func(t *testing.T) {
		svc := NewRestaurantService(&fakeRepo{cfg: alwaysOpen(true)}, &fakeOverrides{}, false)
		ok, err := svc.IsOrderingAllowed(t.Context())
		require.NoError(t, err)
		assert.True(t, ok)
		assert.False(t, svc.IsDevMode())
	})

	t.Run("ordering switched off", func(t *testing.T) {
		ok, err := NewRestaurantService(&fakeRepo{cfg: alwaysOpen(false)}, &fakeOverrides{}, false).IsOrderingAllowed(t.Context())
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("outside hours", func(t *testing.T) {
		cfg := alwaysOpen(true)
		cfg.OpeningHours = json.RawMessage(`{}`)
		ok, err := NewRestaurantService(&fakeRepo{cfg: cfg}, &fakeOverrides{}, false).IsOrderingAllowed(t.Context())
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("a lookup failure is an error and not a silent yes", func(t *testing.T) {
		boom := errors.New("db down")
		ok, err := NewRestaurantService(&fakeRepo{err: boom}, &fakeOverrides{}, false).IsOrderingAllowed(t.Context())
		assert.ErrorIs(t, err, boom)
		assert.False(t, ok)
	})
}

func TestRestaurantServiceDelegation(t *testing.T) {
	ctx := t.Context()
	cfg := alwaysOpen(true)
	repo, ovs := &fakeRepo{cfg: cfg}, &fakeOverrides{}
	svc := NewRestaurantService(repo, ovs, false)

	got, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	assert.Same(t, cfg, got)

	_, err = svc.UpdateOrderingEnabled(ctx, true)
	require.NoError(t, err)
	assert.True(t, repo.enable)
	_, err = svc.UpdateOpeningHours(ctx, json.RawMessage(`{"a":1}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(repo.hours))
	_, err = svc.UpdateOrderingHours(ctx, json.RawMessage(`{"b":2}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"b":2}`, string(repo.hours))
	assert.Equal(t, []string{"get", "opening", "ordering"}, repo.calls)
	_, err = svc.UpdatePreparationMinutes(ctx, 45)
	require.NoError(t, err)
	assert.Equal(t, 45, repo.minute)

	boom := errors.New("db down")
	repo.err = boom
	_, err = svc.UpdateOrderingEnabled(ctx, false)
	assert.ErrorIs(t, err, boom)
	_, err = svc.UpdateOpeningHours(ctx, nil)
	assert.ErrorIs(t, err, boom)
	_, err = svc.UpdateOrderingHours(ctx, nil)
	assert.ErrorIs(t, err, boom)
	_, err = svc.UpdatePreparationMinutes(ctx, 1)
	assert.ErrorIs(t, err, boom)
	_, err = svc.GetConfig(ctx)
	assert.ErrorIs(t, err, boom)
}

func TestOverrideManagement(t *testing.T) {
	ctx := t.Context()
	date := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	note := "Christmas"
	ovs := &fakeOverrides{list: []*domain.ScheduleOverride{{Closed: true}}}
	svc := NewRestaurantService(&fakeRepo{}, ovs, false)

	listed, err := svc.ListOverrides(ctx, date, date.AddDate(0, 1, 0))
	require.NoError(t, err)
	assert.Len(t, listed, 1)
	assert.True(t, ovs.from.Equal(date))
	assert.True(t, ovs.to.Equal(date.AddDate(0, 1, 0)))

	saved, err := svc.UpsertOverride(ctx, date, false, json.RawMessage(`{"open":"10:00","close":"12:00"}`), &note)
	require.NoError(t, err)
	assert.True(t, saved.Date.Equal(date))
	assert.False(t, saved.Closed)
	assert.JSONEq(t, `{"open":"10:00","close":"12:00"}`, string(saved.Schedule))
	assert.Equal(t, "Christmas", *saved.Note)

	require.NoError(t, svc.DeleteOverride(ctx, date))
	assert.True(t, ovs.deleted.Equal(date))

	boom := errors.New("db down")
	ovs.upsertErr, ovs.deleteErr, ovs.listErr = boom, boom, boom
	_, err = svc.UpsertOverride(ctx, date, true, nil, nil)
	assert.ErrorIs(t, err, boom)
	assert.ErrorIs(t, svc.DeleteOverride(ctx, date), boom)
	_, err = svc.ListOverrides(ctx, date, date)
	assert.ErrorIs(t, err, boom)
}
