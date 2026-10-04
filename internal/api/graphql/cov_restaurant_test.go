package graphql_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/timezone"
)

// The restaurant's own settings (ordering switch, opening and ordering hours, preparation time,
// day overrides) and the live fields of RestaurantConfig derived from them.

func day(open, closeAt string) *model.DayScheduleInput {
	return &model.DayScheduleInput{Open: open, Close: closeAt}
}

// openAllDay and closedAllWeek are the two weeks the tests can rely on whatever the date is.
func openAllDay() model.OpeningHoursInput {
	d := day("00:00", "23:59")
	return model.OpeningHoursInput{Monday: d, Tuesday: d, Wednesday: d, Thursday: d, Friday: d, Saturday: d, Sunday: d}
}

func closedAllWeek() model.OpeningHoursInput { return model.OpeningHoursInput{} }

func recvConfig(t *testing.T, ch <-chan *model.RestaurantConfig) *model.RestaurantConfig {
	t.Helper()
	select {
	case c := <-ch:
		require.NotNil(t, c)
		return c
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no config was published")
		return nil
	}
}

func TestRestaurantSettings(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	changes, err := r.Subscription().RestaurantConfigUpdated(admin)
	require.NoError(t, err)

	t.Run("the ordering switch is saved and published", func(t *testing.T) {
		got, err := r.Mutation().UpdateOrderingEnabled(admin, false)
		require.NoError(t, err)
		assert.False(t, got.OrderingEnabled)
		assert.False(t, recvConfig(t, changes).OrderingEnabled)
		got, err = r.Mutation().UpdateOrderingEnabled(admin, true)
		require.NoError(t, err)
		assert.True(t, got.OrderingEnabled)
		recvConfig(t, changes)
	})

	t.Run("opening hours are saved per day, a missing day is closed", func(t *testing.T) {
		hours := closedAllWeek()
		hours.Monday = &model.DayScheduleInput{Open: "11:30", Close: "14:30", DinnerOpen: new("17:30"), DinnerClose: new("22:00")}
		hours.Friday = day("12:00", "23:00")
		got, err := r.Mutation().UpdateOpeningHours(admin, hours)
		require.NoError(t, err)
		week, ok := got.OpeningHours.(map[string]any)
		require.True(t, ok, "%T", got.OpeningHours)
		assert.Equal(t, map[string]any{"open": "11:30", "close": "14:30", "dinnerOpen": "17:30", "dinnerClose": "22:00"}, week["monday"])
		assert.Equal(t, map[string]any{"open": "12:00", "close": "23:00"}, week["friday"])
		assert.Nil(t, week["tuesday"])
		recvConfig(t, changes)

		stored, err := r.RestaurantService.GetConfig(t.Context())
		require.NoError(t, err)
		parsed, err := stored.GetOpeningHours()
		require.NoError(t, err)
		require.NotNil(t, parsed["monday"])
		assert.Equal(t, "17:30", parsed["monday"].DinnerOpen)
		assert.Nil(t, parsed["sunday"])
	})

	t.Run("ordering hours are separate from opening hours, and optional", func(t *testing.T) {
		got, err := r.Mutation().UpdateOpeningHours(admin, openAllDay())
		require.NoError(t, err)
		assert.Nil(t, got.OrderingHours, "no ordering hours yet: ordering follows the opening hours")
		recvConfig(t, changes)

		got, err = r.Mutation().UpdateOrderingHours(admin, closedAllWeek())
		require.NoError(t, err)
		assert.NotNil(t, got.OrderingHours)
		recvConfig(t, changes)

		open, err := r.RestaurantConfig().IsCurrentlyOpen(admin, got)
		require.NoError(t, err)
		assert.True(t, open, "the restaurant itself is open all day")
		ordering, err := r.RestaurantConfig().IsOrderingCurrentlyOpen(admin, got)
		require.NoError(t, err)
		assert.False(t, ordering, "but ordering hours say closed")
		slots, err := r.RestaurantConfig().AvailableSlotsToday(admin, got)
		require.NoError(t, err)
		assert.Empty(t, slots)
		next, err := r.RestaurantConfig().NextOpeningAt(admin, got)
		require.NoError(t, err)
		require.NotNil(t, next, "the restaurant opens again at midnight: ordering hours do not move the opening")
		assert.True(t, next.After(time.Now()))
		assert.True(t, next.Before(time.Now().Add(25*time.Hour)))
	})

	t.Run("a customer sees the live fields, a store-review account may order when closed", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "en", `{ restaurantConfig { isCurrentlyOpen isOrderingCurrentlyOpen availableSlotsToday { label } nextOpeningAt preparationMinutes updatedAt } }`, nil)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		var data struct {
			RestaurantConfig struct {
				IsCurrentlyOpen, IsOrderingCurrentlyOpen bool
				AvailableSlotsToday                      []struct{ Label string }
				NextOpeningAt                            *string
			}
		}
		require.NoError(t, json.Unmarshal(resp.Data, &data))
		assert.True(t, data.RestaurantConfig.IsCurrentlyOpen)
		assert.False(t, data.RestaurantConfig.IsOrderingCurrentlyOpen)
		assert.Empty(t, data.RestaurantConfig.AvailableSlotsToday)
	})

	t.Run("ordering hours open all day make ordering open again", func(t *testing.T) {
		got, err := r.Mutation().UpdateOrderingHours(admin, openAllDay())
		require.NoError(t, err)
		recvConfig(t, changes)
		ordering, err := r.RestaurantConfig().IsOrderingCurrentlyOpen(admin, got)
		require.NoError(t, err)
		assert.True(t, ordering)
		slots, err := r.RestaurantConfig().AvailableSlotsToday(admin, got)
		require.NoError(t, err)
		for _, s := range slots {
			assert.True(t, s.Value.After(time.Now()), "a slot offered today is in the future: %s", s.Label)
		}
	})

	t.Run("the preparation time is bounded to 1..240 minutes", func(t *testing.T) {
		for _, minutes := range []int{0, -5, 241} {
			_, err := r.Mutation().UpdatePreparationMinutes(admin, minutes)
			userErr(t, err, "preparation minutes must be between 1 and 240")
		}
		got, err := r.Mutation().UpdatePreparationMinutes(admin, 240)
		require.NoError(t, err)
		assert.Equal(t, 240, got.PreparationMinutes)
		recvConfig(t, changes)
		got, err = r.Mutation().UpdatePreparationMinutes(admin, 1)
		require.NoError(t, err)
		assert.Equal(t, 1, got.PreparationMinutes)
		recvConfig(t, changes)
	})

	t.Run("policy is the ordering policy of the instance", func(t *testing.T) {
		policy, err := r.RestaurantConfig().Policy(admin, nil)
		require.NoError(t, err)
		assert.Equal(t, "25.00", policy.DeliveryMinimum)
		assert.Equal(t, 9.0, policy.DeliveryMaxDistanceKm)
		assert.Equal(t, []string{"4610"}, policy.ExcludedPostcodes)
		require.Len(t, policy.DeliveryFeeTiers, 7)
		assert.Equal(t, 3.0, policy.DeliveryFeeTiers[0].UpToKm)
	})

	t.Run("the settings are for admins", func(t *testing.T) {
		_, token := testhelpers.SeedCustomer(t, env.DB.DB, "curious")
		resp := gqlAs(t, env.TestContext, token, "en", `mutation { updateOrderingEnabled(enabled: false) { orderingEnabled } }`, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
		cfg, err := r.RestaurantService.GetConfig(t.Context())
		require.NoError(t, err)
		assert.True(t, cfg.OrderingEnabled)
	})

	t.Run("a failing store is reported by every settings operation", func(t *testing.T) {
		broken := env.brokenResolver(t)
		_, err := broken.Mutation().UpdateOrderingEnabled(admin, true)
		require.ErrorContains(t, err, "update ordering enabled")
		_, err = broken.Mutation().UpdateOpeningHours(admin, openAllDay())
		require.ErrorContains(t, err, "update opening hours")
		_, err = broken.Mutation().UpdateOrderingHours(admin, openAllDay())
		require.ErrorContains(t, err, "update ordering hours")
		_, err = broken.Mutation().UpdatePreparationMinutes(admin, 20)
		require.ErrorContains(t, err, "update preparation minutes")
		_, err = broken.Query().RestaurantConfig(admin)
		require.ErrorContains(t, err, "get restaurant config")
		_, err = broken.Query().ScheduleOverrides(admin, time.Now(), time.Now())
		require.ErrorContains(t, err, "list schedule overrides")
		_, err = broken.Mutation().UpsertScheduleOverride(admin, model.ScheduleOverrideInput{Date: time.Now(), Closed: true})
		require.ErrorContains(t, err, "upsert override")
		_, err = broken.Mutation().DeleteScheduleOverride(admin, time.Now())
		require.ErrorContains(t, err, "delete override")

		cfg := &model.RestaurantConfig{}
		_, err = broken.RestaurantConfig().IsCurrentlyOpen(admin, cfg)
		require.ErrorContains(t, err, "get restaurant config")
		_, err = broken.RestaurantConfig().IsOrderingCurrentlyOpen(admin, cfg)
		require.ErrorContains(t, err, "get restaurant config")
		_, err = broken.RestaurantConfig().AvailableSlotsToday(admin, cfg)
		require.ErrorContains(t, err, "get restaurant config")
		_, err = broken.RestaurantConfig().NextOpeningAt(admin, cfg)
		require.ErrorContains(t, err, "get restaurant config")
	})
}

func TestScheduleOverrides(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	_, err := r.Mutation().UpdateOpeningHours(admin, openAllDay())
	require.NoError(t, err)
	_, err = r.Mutation().UpdateOrderingHours(admin, closedAllWeek())
	require.NoError(t, err)
	// Ordering follows the opening hours again.
	_, err = env.DB.DB.ExecContext(t.Context(), `UPDATE restaurant_config SET ordering_hours = NULL`)
	require.NoError(t, err)

	today := timezone.In(time.Now())
	tomorrow := today.AddDate(0, 0, 1)
	overrides, err := r.Subscription().ScheduleOverridesUpdated(admin)
	require.NoError(t, err)
	changes, err := r.Subscription().RestaurantConfigUpdated(admin)
	require.NoError(t, err)

	t.Run("an override that is not closed needs a schedule", func(t *testing.T) {
		_, err := r.Mutation().UpsertScheduleOverride(admin, model.ScheduleOverrideInput{Date: tomorrow, Closed: false})
		userErr(t, err, "schedule is required when override is not closed")
	})

	t.Run("closing today beats the weekly hours and tells the dashboards", func(t *testing.T) {
		got, err := r.Mutation().UpsertScheduleOverride(admin, model.ScheduleOverrideInput{Date: today, Closed: true, Note: new("holiday")})
		require.NoError(t, err)
		assert.True(t, got.Closed)
		assert.Equal(t, "holiday", *got.Note)
		assert.Nil(t, got.Schedule)

		select {
		case list := <-overrides:
			require.Len(t, list, 1)
			assert.True(t, list[0].Closed)
		case <-time.After(10 * time.Second):
			require.FailNow(t, "the override list was not published")
		}
		recvConfig(t, changes)

		cfg := &model.RestaurantConfig{}
		open, err := r.RestaurantConfig().IsCurrentlyOpen(admin, cfg)
		require.NoError(t, err)
		assert.False(t, open, "today is closed by the override")
		ordering, err := r.RestaurantConfig().IsOrderingCurrentlyOpen(admin, cfg)
		require.NoError(t, err)
		assert.False(t, ordering)
	})

	t.Run("an override with its own hours replaces the weekly ones", func(t *testing.T) {
		got, err := r.Mutation().UpsertScheduleOverride(admin, model.ScheduleOverrideInput{
			Date: tomorrow, Closed: false, Schedule: &model.DayScheduleInput{Open: "10:00", Close: "15:00", DinnerOpen: new("18:00"), DinnerClose: new("21:00")}})
		require.NoError(t, err)
		require.NotNil(t, got.Schedule)
		assert.Equal(t, "10:00", got.Schedule.Open)
		require.NotNil(t, got.Schedule.DinnerOpen)
		assert.Equal(t, "18:00", *got.Schedule.DinnerOpen)
		assert.Equal(t, tomorrow.Format("2006-01-02"), timezone.In(got.Date).Format("2006-01-02"))
		<-overrides
		recvConfig(t, changes)

		// A schedule sent with closed=true is ignored: the day is closed.
		got, err = r.Mutation().UpsertScheduleOverride(admin, model.ScheduleOverrideInput{Date: tomorrow, Closed: true, Schedule: day("10:00", "11:00")})
		require.NoError(t, err)
		assert.True(t, got.Closed)
		assert.Nil(t, got.Schedule)
		<-overrides
		recvConfig(t, changes)
	})

	t.Run("overrides are listed by date range, and deleting one brings the weekly hours back", func(t *testing.T) {
		list, err := r.Query().ScheduleOverrides(admin, today.AddDate(0, 0, -1), today.AddDate(0, 0, 3))
		require.NoError(t, err)
		assert.Len(t, list, 2)
		list, err = r.Query().ScheduleOverrides(admin, today.AddDate(0, 0, 10), today.AddDate(0, 0, 12))
		require.NoError(t, err)
		assert.Empty(t, list)

		ok, err := r.Mutation().DeleteScheduleOverride(admin, today)
		require.NoError(t, err)
		assert.True(t, ok)
		<-overrides
		recvConfig(t, changes)
		open, err := r.RestaurantConfig().IsCurrentlyOpen(admin, &model.RestaurantConfig{})
		require.NoError(t, err)
		assert.True(t, open)
	})

	t.Run("a failing follow-up read does not undo the change", func(t *testing.T) {
		quiet := env.with(func(r *resolver.Resolver) {
			r.RestaurantService = faultyRestaurant{RestaurantService: env.Resolver.RestaurantService, failList: true, failConfig: true}
		})
		got, err := quiet.Mutation().UpsertScheduleOverride(admin, model.ScheduleOverrideInput{Date: tomorrow.AddDate(0, 0, 2), Closed: true})
		require.NoError(t, err)
		assert.True(t, got.Closed)
		ok, err := quiet.Mutation().DeleteScheduleOverride(admin, tomorrow.AddDate(0, 0, 2))
		require.NoError(t, err)
		assert.True(t, ok)
		select {
		case <-overrides:
			t.Fatal("nothing should have been published")
		default:
		}
	})

	t.Run("the override list is for admins", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, "", "en", `{ scheduleOverrides(from: "2026-01-01T00:00:00Z", to: "2026-12-31T00:00:00Z") { date } }`, nil)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	})
}

func TestRestaurantConfigFieldsWithAConfigThatFails(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	admin := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	r := env.with(func(r *resolver.Resolver) {
		r.RestaurantService = faultyRestaurant{RestaurantService: env.Resolver.RestaurantService, failWithOverrides: true}
	})
	cfg := &model.RestaurantConfig{}
	_, err := r.RestaurantConfig().IsCurrentlyOpen(admin, cfg)
	require.ErrorContains(t, err, "get restaurant config")
	_, err = r.RestaurantConfig().IsOrderingCurrentlyOpen(admin, cfg)
	require.ErrorContains(t, err, "get restaurant config")
	_, err = r.RestaurantConfig().AvailableSlotsToday(admin, cfg)
	require.ErrorContains(t, err, "get restaurant config")
	_, err = r.RestaurantConfig().NextOpeningAt(admin, cfg)
	require.ErrorContains(t, err, "get restaurant config")
}
