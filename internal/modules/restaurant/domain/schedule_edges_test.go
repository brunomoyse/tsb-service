package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/brand"
	"tsb-service/pkg/timezone"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func labels(slots []TimeSlot) []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = s.Label
	}
	return out
}

func override(t *testing.T, date string, closed bool, schedule string) map[string]*ScheduleOverride {
	t.Helper()
	ov := &ScheduleOverride{Date: at(t, date, "00:00"), Closed: closed}
	if schedule != "" {
		ov.Schedule = raw(schedule)
	}
	return map[string]*ScheduleOverride{date: ov}
}

func TestConfigHoursParsing(t *testing.T) {
	t.Run("malformed opening hours are an error", func(t *testing.T) {
		_, err := (&RestaurantConfig{OpeningHours: raw(`{"monday":`)}).GetOpeningHours()
		require.ErrorContains(t, err, "parse opening hours")
	})

	t.Run("ordering hours: unset, JSON null and valid", func(t *testing.T) {
		for _, unset := range []json.RawMessage{nil, raw(``), raw(`null`)} {
			h, err := (&RestaurantConfig{OrderingHours: unset}).GetOrderingHours()
			require.NoError(t, err)
			assert.Nil(t, h)
		}
		h, err := (&RestaurantConfig{OrderingHours: raw(`{"monday":{"open":"10:00","close":"12:00"}}`)}).GetOrderingHours()
		require.NoError(t, err)
		assert.Equal(t, "10:00", h["monday"].Open)
	})

	t.Run("malformed ordering hours are an error", func(t *testing.T) {
		_, err := (&RestaurantConfig{OrderingHours: raw(`[1`)}).GetOrderingHours()
		require.ErrorContains(t, err, "parse ordering hours")
	})
}

func TestMalformedConfigFailsClosed(t *testing.T) {
	now := at(t, "2026-04-22", "12:30") // Wednesday lunch
	badOpening := &RestaurantConfig{OrderingEnabled: true, OpeningHours: raw(`oops`)}
	badOrdering := &RestaurantConfig{OrderingEnabled: true, OpeningHours: weeklyHours(t), OrderingHours: raw(`oops`)}

	assert.False(t, badOpening.IsCurrentlyOpen(now, nil))
	assert.False(t, badOpening.IsOrderingCurrentlyOpen(now, nil))
	assert.False(t, badOpening.IsOrderingAllowed(now, nil))
	assert.False(t, badOrdering.IsOrderingCurrentlyOpen(now, nil))
	assert.Nil(t, badOpening.AvailableSlotsToday(now, nil))
	assert.Nil(t, badOrdering.AvailableSlotsToday(now, nil))
	assert.False(t, badOpening.IsLunchOnlyAllowed(now, nil))
	assert.False(t, badOrdering.IsLunchOnlyAllowed(now, nil))
	assert.Nil(t, badOpening.NextOpeningAt(now, nil))
}

func TestIsOrderingAllowed_UsesOrderingHoursWhenConfigured(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	cfg.OrderingHours = raw(`{"wednesday":{"open":"12:00","close":"13:00"}}`)

	assert.True(t, cfg.IsOrderingAllowed(at(t, "2026-04-22", "12:30"), nil))
	assert.False(t, cfg.IsOrderingAllowed(at(t, "2026-04-22", "13:30"), nil), "open per opening hours but outside ordering hours")
	assert.False(t, cfg.IsOrderingAllowed(at(t, "2026-04-23", "12:30"), nil), "day absent from ordering hours")

	cfg.OrderingHours = nil
	assert.True(t, cfg.IsOrderingAllowed(at(t, "2026-04-22", "13:30"), nil), "falls back to opening hours")
}

func TestOverrideSchedules(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	wed := "2026-04-22"

	t.Run("a custom override replaces the weekly hours for that day only", func(t *testing.T) {
		ov := override(t, wed, false, `{"open":"09:00","close":"10:00"}`)
		assert.True(t, cfg.IsCurrentlyOpen(at(t, wed, "09:30"), ov))
		assert.False(t, cfg.IsCurrentlyOpen(at(t, wed, "12:30"), ov))
		assert.True(t, cfg.IsCurrentlyOpen(at(t, "2026-04-23", "12:30"), ov))
	})

	t.Run("an override with an unreadable schedule closes the day", func(t *testing.T) {
		ov := override(t, wed, false, `{"open":`)
		assert.False(t, cfg.IsCurrentlyOpen(at(t, wed, "12:30"), ov))
	})

	t.Run("an open override without a schedule closes the day", func(t *testing.T) {
		assert.False(t, cfg.IsCurrentlyOpen(at(t, wed, "12:30"), override(t, wed, false, `null`)))
		assert.False(t, cfg.IsCurrentlyOpen(at(t, wed, "12:30"), override(t, wed, false, "")))
	})

	t.Run("a nil override entry is ignored", func(t *testing.T) {
		assert.True(t, cfg.IsCurrentlyOpen(at(t, wed, "12:30"), map[string]*ScheduleOverride{wed: nil}))
	})

	t.Run("ParsedSchedule reports malformed JSON and returns nil for closed days", func(t *testing.T) {
		_, err := (&ScheduleOverride{Schedule: raw(`{`)}).ParsedSchedule()
		require.ErrorContains(t, err, "parse override schedule")
		s, err := (&ScheduleOverride{Closed: true, Schedule: raw(`{"open":"1:00","close":"2:00"}`)}).ParsedSchedule()
		require.NoError(t, err)
		assert.Nil(t, s)
		s, err = (&ScheduleOverride{Schedule: raw(`{"open":"10:00","close":"11:00","dinnerOpen":"18:00","dinnerClose":"20:00"}`)}).ParsedSchedule()
		require.NoError(t, err)
		assert.Equal(t, "20:00", s.DinnerClose)
	})

	t.Run("DateKey is the calendar date", func(t *testing.T) {
		assert.Equal(t, wed, (&ScheduleOverride{Date: at(t, wed, "00:00")}).DateKey())
	})
}

func TestAvailableSlotsToday_Edges(t *testing.T) {
	wed := "2026-04-22"

	t.Run("a weekly-closed day has no slots", func(t *testing.T) {
		assert.Empty(t, configWith(t, weeklyHours(t), 30).AvailableSlotsToday(at(t, "2026-04-21", "10:00"), nil))
	})

	t.Run("a non-positive preparation time defaults to 30 minutes", func(t *testing.T) {
		cfg := configWith(t, weeklyHours(t), 0)
		slots := cfg.AvailableSlotsToday(at(t, wed, "10:00"), nil)
		require.NotEmpty(t, slots)
		assert.Equal(t, "11:30", slots[0].Label, "open 11:00 + default 30 minutes")
	})

	t.Run("the last slot is the closing time itself and slots are a quarter hour apart", func(t *testing.T) {
		cfg := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"11:00","close":"12:30"}}`), PreparationMinutes: 30}
		assert.Equal(t, []string{"11:30", "11:45", "12:00", "12:15", "12:30"}, labels(cfg.AvailableSlotsToday(at(t, wed, "08:00"), nil)))
	})

	t.Run("now plus preparation, rounded up, bounds the first slot", func(t *testing.T) {
		cfg := configWith(t, weeklyHours(t), 30)
		slots := cfg.AvailableSlotsToday(at(t, wed, "12:07"), nil)
		require.NotEmpty(t, slots)
		assert.Equal(t, "12:45", slots[0].Label, "12:07 + 30 = 12:37, rounded up to 12:45")
		assert.Equal(t, "13:45", slots[4].Label)
	})

	t.Run("an interval that closes before open plus preparation yields nothing", func(t *testing.T) {
		cfg := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"11:00","close":"11:20"}}`), PreparationMinutes: 30}
		assert.Empty(t, cfg.AvailableSlotsToday(at(t, wed, "08:00"), nil))
	})

	t.Run("intervals with unreadable times are skipped, valid ones kept", func(t *testing.T) {
		cfg := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"soon","close":"14:00","dinnerOpen":"18:00","dinnerClose":"19:00"}}`), PreparationMinutes: 30}
		assert.Equal(t, []string{"18:30", "18:45", "19:00"}, labels(cfg.AvailableSlotsToday(at(t, wed, "08:00"), nil)))
		cfg = &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"11:00","close":"12","dinnerOpen":"x:y","dinnerClose":"19:00"}}`), PreparationMinutes: 30}
		assert.Empty(t, cfg.AvailableSlotsToday(at(t, wed, "08:00"), nil))
	})

	t.Run("overlapping services do not repeat a slot", func(t *testing.T) {
		cfg := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"11:00","close":"12:30","dinnerOpen":"11:30","dinnerClose":"13:00"}}`), PreparationMinutes: 30}
		got := labels(cfg.AvailableSlotsToday(at(t, wed, "08:00"), nil))
		assert.Equal(t, []string{"11:30", "11:45", "12:00", "12:15", "12:30", "12:45", "13:00"}, got)
	})

	t.Run("only the first service on a weekday allows lunch-only products", func(t *testing.T) {
		slots := configWith(t, weeklyHours(t), 30).AvailableSlotsToday(at(t, wed, "08:00"), nil)
		var lunch, dinner int
		for _, s := range slots {
			if s.Label < "15:00" {
				assert.True(t, s.IsLunchOnlyAllowed, s.Label)
				lunch++
			} else {
				assert.False(t, s.IsLunchOnlyAllowed, s.Label)
				dinner++
			}
		}
		assert.Positive(t, lunch)
		assert.Positive(t, dinner)

		sat := configWith(t, weeklyHours(t), 30).AvailableSlotsToday(at(t, "2026-04-25", "08:00"), nil)
		require.NotEmpty(t, sat)
		for _, s := range sat {
			assert.False(t, s.IsLunchOnlyAllowed, "weekends never allow lunch-only: %s", s.Label)
		}
	})

	t.Run("ordering hours win over opening hours", func(t *testing.T) {
		cfg := configWith(t, weeklyHours(t), 30)
		cfg.OrderingHours = raw(`{"wednesday":{"open":"12:00","close":"12:45"}}`)
		assert.Equal(t, []string{"12:30", "12:45"}, labels(cfg.AvailableSlotsToday(at(t, wed, "08:00"), nil)))
	})

	t.Run("slot values are exact instants matching their label", func(t *testing.T) {
		slots := configWith(t, weeklyHours(t), 30).AvailableSlotsToday(at(t, wed, "08:00"), nil)
		for _, s := range slots {
			assert.Equal(t, s.Label, timezone.In(s.Value).Format("15:04"))
		}
	})
}

func TestIsLunchOnlyAllowed_Edges(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)

	assert.True(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-22", "12:00"), nil), "weekday lunch")
	assert.True(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-22", "14:00"), nil), "closing minute is inclusive")
	assert.False(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-22", "19:00"), nil), "dinner service")
	assert.False(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-22", "10:59"), nil), "before opening")
	assert.False(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-25", "12:30"), nil), "Saturday")
	assert.False(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-26", "12:30"), nil), "Sunday")
	assert.False(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-21", "12:30"), nil), "closed Tuesday")
	assert.False(t, cfg.IsLunchOnlyAllowed(at(t, "2026-04-22", "12:30"), override(t, "2026-04-22", true, "")), "closed by override")

	bad := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"noon","close":"14:00"}}`)}
	assert.False(t, bad.IsLunchOnlyAllowed(at(t, "2026-04-22", "12:30"), nil), "unreadable hours")
	bad = &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"11:00","close":"later"}}`)}
	assert.False(t, bad.IsLunchOnlyAllowed(at(t, "2026-04-22", "12:30"), nil))

	ordering := configWith(t, weeklyHours(t), 30)
	ordering.OrderingHours = raw(`{"wednesday":{"open":"16:00","close":"18:00"}}`)
	assert.False(t, ordering.IsLunchOnlyAllowed(at(t, "2026-04-22", "12:30"), nil), "ordering hours replace the lunch window")
	assert.True(t, ordering.IsLunchOnlyAllowed(at(t, "2026-04-22", "17:00"), nil))
}

func TestNextOpeningAt_Edges(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	fmtAt := func(p *time.Time) string {
		require.NotNil(t, p)
		return timezone.In(*p).Format("2006-01-02 15:04")
	}

	assert.Equal(t, "2026-04-22 18:00", fmtAt(cfg.NextOpeningAt(at(t, "2026-04-22", "15:00"), nil)), "dinner opening later today")
	assert.Equal(t, "2026-04-22 11:00", fmtAt(cfg.NextOpeningAt(at(t, "2026-04-22", "08:00"), nil)))
	assert.Equal(t, "2026-04-22 18:00", fmtAt(cfg.NextOpeningAt(at(t, "2026-04-22", "11:00"), nil)), "an opening exactly now is not in the future")
	assert.Equal(t, "2026-04-22 11:00", fmtAt(cfg.NextOpeningAt(at(t, "2026-04-20", "23:00"), nil)), "Tuesday is closed")

	t.Run("an unreadable opening time is skipped for the next one", func(t *testing.T) {
		bad := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"x","close":"14:00","dinnerOpen":"18:00","dinnerClose":"22:00"}}`)}
		assert.Equal(t, "2026-04-22 18:00", fmtAt(bad.NextOpeningAt(at(t, "2026-04-22", "08:00"), nil)))
	})

	t.Run("nothing opens within a week", func(t *testing.T) {
		never := &RestaurantConfig{OpeningHours: raw(`{}`)}
		assert.Nil(t, never.NextOpeningAt(at(t, "2026-04-22", "08:00"), nil))
	})

	t.Run("a special-hours override opens at its own time", func(t *testing.T) {
		ov := override(t, "2026-04-22", false, `{"open":"09:30","close":"10:30"}`)
		assert.Equal(t, "2026-04-22 09:30", fmtAt(cfg.NextOpeningAt(at(t, "2026-04-22", "08:00"), ov)))
	})
}

func TestParseHHMM(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "09:05": 545, "23:59": 1439, "7:30": 450} {
		got, ok := parseHHMM(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "0930", "ab:cd", "09:xx", "xx:30", ":"} {
		_, ok := parseHHMM(bad)
		assert.False(t, ok, bad)
	}
}

func TestOrderingPolicy_DeliveryFeeWithoutCoveringTier(t *testing.T) {
	p := DefaultOrderingPolicy()
	p.DeliveryFeeTiers = nil
	fee, ok := p.DeliveryFee(1000)
	assert.False(t, ok, "inside the radius but no tier covers it: not deliverable")
	assert.True(t, fee.Equal(decimal.Zero))
}

func TestIsCurrentlyOpen_DinnerServiceAndBoundaries(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	assert.True(t, cfg.IsCurrentlyOpen(at(t, "2026-04-22", "19:00"), nil), "dinner service")
	assert.True(t, cfg.IsCurrentlyOpen(at(t, "2026-04-22", "18:00"), nil), "opening minute is inclusive")
	assert.False(t, cfg.IsCurrentlyOpen(at(t, "2026-04-22", "22:00"), nil), "closing minute is exclusive")
	assert.False(t, cfg.IsCurrentlyOpen(at(t, "2026-04-22", "14:00"), nil))

	noDinner := &RestaurantConfig{OpeningHours: raw(`{"wednesday":{"open":"11:00","close":"14:00","dinnerOpen":"18:00"}}`)}
	assert.False(t, noDinner.IsCurrentlyOpen(at(t, "2026-04-22", "19:00"), nil), "a dinner service needs both ends")
}

func TestCurrentPolicyFollowsTheDeliverySwitch(t *testing.T) {
	t.Cleanup(func() { brand.Load() }) // runs after t.Setenv restored the environment
	t.Setenv("RESTAURANT_DELIVERY_ENABLED", "false")
	brand.Load()
	assert.False(t, CurrentPolicy().DeliveryEnabled)

	t.Setenv("RESTAURANT_DELIVERY_ENABLED", "true")
	brand.Load()
	p := CurrentPolicy()
	assert.True(t, p.DeliveryEnabled)
	assert.Equal(t, DefaultOrderingPolicy().DeliveryMaxMeters, p.DeliveryMaxMeters, "everything else is the shared default")
}
