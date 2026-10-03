package graphql_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/timezone"
)

// The opening-hours gate of createOrder (ordering switch, ordering/opening hours, schedule
// overrides, the preferred ready time slot and lunch-only products), through the real service and
// database with the gate enforced (the other tests run in dev mode, which skips it).
//
// The resolver reads the wall clock, so the schedules are built around "now" in Brussels. Subtests
// that need a same-day slot skip themselves in the last minutes of the day; none of them depends on
// the weekday except where it says so (lunch-only products are a weekday thing).

type daySchedule struct {
	Open        string `json:"open"`
	Close       string `json:"close"`
	DinnerOpen  string `json:"dinnerOpen,omitempty"`
	DinnerClose string `json:"dinnerClose,omitempty"`
}

var weekdayNames = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

// weekly builds the opening hours JSON: s every day, except today, which gets today (nil = closed).
func weekly(t *testing.T, s daySchedule, today *daySchedule) string {
	t.Helper()
	todayName := weekdayNames[timezone.In(time.Now()).Weekday()]
	hours := map[string]*daySchedule{}
	for _, name := range weekdayNames {
		hours[name] = &s
		if name == todayName {
			hours[name] = today
		}
	}
	raw, err := json.Marshal(hours)
	require.NoError(t, err)
	return string(raw)
}

func allDay() daySchedule { return daySchedule{Open: "00:00", Close: "23:59"} }

// scheduleSetup is the state of restaurant_config and the overrides for one subtest.
type scheduleSetup struct {
	enabled     bool
	opening     string
	ordering    string // "" = not set: ordering follows the opening hours
	override    *scheduleOverride
	preparation int
}

type scheduleOverride struct {
	closed   bool
	schedule *daySchedule
}

func applySchedule(t *testing.T, tc *TestContext, s scheduleSetup) {
	t.Helper()
	prep := s.preparation
	if prep == 0 {
		prep = 15
	}
	var ordering any
	if s.ordering != "" {
		ordering = s.ordering
	}
	_, err := tc.DB.DB.ExecContext(t.Context(), `
		UPDATE restaurant_config
		SET ordering_enabled = $1, opening_hours = $2::jsonb, ordering_hours = $3::jsonb, preparation_minutes = $4, updated_at = NOW()
		WHERE id = TRUE`, s.enabled, s.opening, ordering, prep)
	require.NoError(t, err)
	_, err = tc.DB.DB.ExecContext(t.Context(), `DELETE FROM restaurant_schedule_overrides`)
	require.NoError(t, err)
	if s.override != nil {
		var schedule any
		if s.override.schedule != nil {
			raw, err := json.Marshal(s.override.schedule)
			require.NoError(t, err)
			schedule = string(raw)
		}
		today := timezone.In(time.Now()).Format("2006-01-02")
		_, err = tc.DB.DB.ExecContext(t.Context(),
			`INSERT INTO restaurant_schedule_overrides (date, closed, schedule) VALUES ($1, $2, $3::jsonb)`,
			today, s.override.closed, schedule)
		require.NoError(t, err)
	}
}

// requireDaytime skips the subtest in the minutes around midnight, where "a slot later today" is not
// well defined; requireBefore skips when less than the given time of day is left.
func requireDaytime(t *testing.T, beforeHour, beforeMinute int) {
	t.Helper()
	local := timezone.In(time.Now())
	minutes := local.Hour()*60 + local.Minute()
	if minutes < 2 || minutes >= beforeHour*60+beforeMinute {
		t.Skipf("local time %s leaves no room for a same-day slot", local.Format("15:04"))
	}
}

// roundUpQuarter is the first quarter hour at or after t.
func roundUpQuarter(t time.Time) time.Time {
	t = t.Truncate(time.Minute)
	if rem := t.Minute() % 15; rem != 0 {
		t = t.Add(time.Duration(15-rem) * time.Minute)
	}
	return t
}

func slot(t time.Time) map[string]any {
	return map[string]any{"preferredReadyTime": t.Format(time.RFC3339)}
}

func TestOrderingSchedule(t *testing.T) {
	tc := setupTestContextWith(t, testContextOptions{EnforceOrderingHours: true})
	salmon := tc.Fixtures.SalmonSushi.ID
	items := lines(salmon, 1)

	order := func(t *testing.T, token string, extra map[string]any) (*createdOrder, *orderErr) {
		t.Helper()
		return createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", items, extra))
	}
	// refused checks createOrder's code and that quoteOrder reports the same thing.
	refused := func(t *testing.T, extra map[string]any, code apperr.Code) *orderErr {
		t.Helper()
		_, token := testhelpers.SeedCustomer(t, tc.DB.DB, "schedule")
		got, orderErr := order(t, token, extra)
		e := requireOrderError(t, got, orderErr, code)
		assert.Contains(t, quoteCodes(quoteAs(t, tc, token, "fr", quoteInput("PICKUP", items, extra))), string(code), "the quote shows the same refusal")
		return e
	}
	accepted := func(t *testing.T, extra map[string]any) *createdOrder {
		t.Helper()
		_, token := testhelpers.SeedCustomer(t, tc.DB.DB, "schedule")
		assert.Empty(t, quoteCodes(quoteAs(t, tc, token, "fr", quoteInput("PICKUP", items, extra))), "the quote shows no refusal")
		return mustCreateOrder(t, tc, token, "fr", createOrderInput("PICKUP", items, extra))
	}

	now := func() time.Time { return timezone.In(time.Now()) }
	validSlot := func() time.Time { return roundUpQuarter(now().Add(20 * time.Minute)) }
	open := scheduleSetup{enabled: true, opening: weekly(t, allDay(), &daySchedule{Open: "00:00", Close: "23:59"})}

	t.Run("open: an order without a slot", func(t *testing.T) {
		requireDaytime(t, 23, 55)
		applySchedule(t, tc, open)
		accepted(t, nil)
	})

	t.Run("open: a valid slot is stored", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		applySchedule(t, tc, open)
		want := validSlot()
		got := accepted(t, slot(want))
		stored := loadStoredOrder(t, tc, got.ID)
		require.NotNil(t, stored.PreferredReady)
		parsed, err := time.Parse("2006-01-02 15:04:05-07", *stored.PreferredReady)
		require.NoError(t, err, *stored.PreferredReady)
		assert.True(t, parsed.Equal(want), "stored %s, asked %s", parsed, want)
	})

	t.Run("open: slots that cannot be honoured", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		applySchedule(t, tc, open)
		valid := validSlot()
		cases := []struct {
			name string
			at   time.Time
			code apperr.Code
		}{
			{"inside the preparation time", now().Add(5 * time.Minute), apperr.CodeSlotTooSoon},
			{"right now", now(), apperr.CodeSlotTooSoon},
			{"already past", now().Add(-5 * time.Minute), apperr.CodeSlotTooSoon},
			{"tomorrow", valid.Add(24 * time.Hour), apperr.CodeSlotNotToday},
			{"yesterday", valid.Add(-24 * time.Hour), apperr.CodeSlotNotToday},
			{"not on a quarter hour", valid.Add(7 * time.Minute), apperr.CodeSlotMisaligned},
			{"not on a whole minute", valid.Add(30 * time.Second), apperr.CodeSlotMisaligned},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) { refused(t, slot(c.at), c.code) })
		}
	})

	t.Run("a slot outside the ordering hours", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, daySchedule{Open: "00:00", Close: "00:01"}, &daySchedule{Open: "00:00", Close: "00:01"})})
		refused(t, slot(validSlot()), apperr.CodeSlotOutsideHours)
	})

	t.Run("closed now: a fixed time is required", func(t *testing.T) {
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, allDay(), nil)})
		refused(t, nil, apperr.CodeSlotRequired)
	})

	t.Run("closed today: a slot is refused", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, allDay(), nil)})
		refused(t, slot(validSlot()), apperr.CodeOrderingClosedToday)
	})

	t.Run("ordering switched off", func(t *testing.T) {
		applySchedule(t, tc, scheduleSetup{enabled: false, opening: open.opening})
		refused(t, nil, apperr.CodeOrderingUnavailable)
		if minutes := now().Hour()*60 + now().Minute(); minutes >= 2 && minutes < 23*60+25 {
			refused(t, slot(validSlot()), apperr.CodeOrderingUnavailable)
		}
	})

	t.Run("an override that closes today", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		check := func(t *testing.T) {
			t.Helper()
			resp := gqlAs(t, tc, "", "fr", `query { restaurantConfig { orderingEnabled } }`, nil)
			require.Empty(t, resp.Errors, "restaurantConfig must still load: %+v", resp.Errors)
			refused(t, nil, apperr.CodeSlotRequired)
			refused(t, slot(validSlot()), apperr.CodeOrderingClosedToday)
		}

		t.Run("stored as SQL NULL", func(t *testing.T) {
			applySchedule(t, tc, scheduleSetup{enabled: true, opening: open.opening, override: &scheduleOverride{closed: true}})
			check(t)
		})

		t.Run("written by upsertScheduleOverride", func(t *testing.T) {
			applySchedule(t, tc, scheduleSetup{enabled: true, opening: open.opening})
			resp := gqlAs(t, tc, adminToken(t, tc), "fr",
				`mutation ($input: ScheduleOverrideInput!) { upsertScheduleOverride(input: $input) { closed } }`,
				map[string]any{"input": map[string]any{"date": time.Now().Format(time.RFC3339), "closed": true}})
			require.Empty(t, resp.Errors, "%+v", resp.Errors)
			check(t)
		})
	})

	t.Run("an override that opens a closed day wins over the weekly hours", func(t *testing.T) {
		requireDaytime(t, 23, 55)
		applySchedule(t, tc, scheduleSetup{
			enabled: true, opening: weekly(t, allDay(), nil),
			override: &scheduleOverride{schedule: &daySchedule{Open: "00:00", Close: "23:59"}},
		})
		accepted(t, nil)
	})

	t.Run("an override with short hours moves the slot window", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		applySchedule(t, tc, scheduleSetup{
			enabled: true, opening: open.opening,
			override: &scheduleOverride{schedule: &daySchedule{Open: "00:00", Close: "00:01"}},
		})
		refused(t, nil, apperr.CodeSlotRequired)
		refused(t, slot(validSlot()), apperr.CodeSlotOutsideHours)
	})

	t.Run("ordering hours are separate from the opening hours", func(t *testing.T) {
		requireDaytime(t, 23, 55)
		// Open to visitors but not to online orders.
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: open.opening, ordering: weekly(t, allDay(), nil)})
		refused(t, nil, apperr.CodeSlotRequired)
		// Closed to visitors but taking online orders.
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, allDay(), nil), ordering: open.opening})
		accepted(t, nil)
	})

	t.Run("closed now, opening later today: ordering ahead", func(t *testing.T) {
		requireDaytime(t, 21, 30)
		opens := roundUpQuarter(now().Add(60 * time.Minute))
		schedule := daySchedule{Open: opens.Format("15:04"), Close: "23:59"}
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, schedule, &schedule)})

		refused(t, nil, apperr.CodeSlotRequired)
		// The first slot is half an hour after opening.
		refused(t, slot(opens.Add(15*time.Minute)), apperr.CodeSlotOutsideHours)
		accepted(t, slot(opens.Add(30*time.Minute)))
		accepted(t, slot(opens.Add(45*time.Minute)))
	})

	t.Run("a longer preparation time moves the earliest slot", func(t *testing.T) {
		requireDaytime(t, 22, 0)
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: open.opening, preparation: 45})
		refused(t, slot(roundUpQuarter(now().Add(20*time.Minute))), apperr.CodeSlotTooSoon)
		accepted(t, slot(roundUpQuarter(now().Add(50*time.Minute))))
	})
}

func TestOrderingLunchOnlyProducts(t *testing.T) {
	tc := setupTestContextWith(t, testContextOptions{EnforceOrderingHours: true})
	cat := testhelpers.SeedCategory(t, tc.DB.DB, 30, shape("Lunch", "fr", "en"))
	lunch := testhelpers.SeedProduct(t, tc.DB.DB, testhelpers.ProductSpec{
		CategoryID: cat, Code: "LUNCHBOX", Price: "9.00", Names: shape("Lunch box", "fr", "en"), LunchOnly: true,
	})
	salmon := tc.Fixtures.SalmonSushi.ID
	weekday := func() bool {
		d := timezone.In(time.Now()).Weekday()
		return d >= time.Monday && d <= time.Friday
	}

	check := func(t *testing.T, items []map[string]any, extra map[string]any, wantRefused bool) {
		t.Helper()
		_, token := testhelpers.SeedCustomer(t, tc.DB.DB, "lunch")
		quote := quoteAs(t, tc, token, "fr", quoteInput("PICKUP", items, extra))
		got, orderErr := createOrderAs(t, tc, token, "fr", createOrderInput("PICKUP", items, extra))
		if !wantRefused {
			require.Nil(t, orderErr, "%+v", orderErr)
			assert.Empty(t, quoteCodes(quote))
			assert.NotEmpty(t, got.ID)
			return
		}
		e := requireOrderError(t, got, orderErr, apperr.CodeLunchSlotRequired)
		assert.Equal(t, lunch.String(), e.Extensions["productId"])
		assert.Contains(t, quoteCodes(quote), "LUNCH_SLOT_REQUIRED")
	}

	lunchBasket := lines(salmon, 1, lunch, 1)
	// The first service of the day is the lunch service: here it covers the whole day, dinner is
	// not configured.
	t.Run("in the lunch service a lunch-only product is a weekday thing", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, allDay(), &daySchedule{Open: "00:00", Close: "23:59"})})
		check(t, lunchBasket, nil, !weekday())
		check(t, lunchBasket, slot(roundUpQuarter(time.Now().Add(20*time.Minute))), !weekday())
	})

	// Lunch is a one-minute window at midnight, the dinner service takes the rest of the day.
	t.Run("a dinner slot is never a lunch slot", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		both := daySchedule{Open: "00:00", Close: "00:01", DinnerOpen: "00:00", DinnerClose: "23:59"}
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, both, &both)})
		check(t, lunchBasket, slot(roundUpQuarter(time.Now().Add(20*time.Minute))), true)
		check(t, lunchBasket, nil, true)
		// The rest of the basket is fine without the lunch product.
		check(t, lines(salmon, 1), slot(roundUpQuarter(time.Now().Add(20*time.Minute))), false)
		check(t, lines(salmon, 1), nil, false)
	})

	t.Run("a lunch-only product on a closed lunch refuses before anything is saved", func(t *testing.T) {
		requireDaytime(t, 23, 25)
		both := daySchedule{Open: "00:00", Close: "00:01", DinnerOpen: "00:00", DinnerClose: "23:59"}
		applySchedule(t, tc, scheduleSetup{enabled: true, opening: weekly(t, both, &both)})
		before := countRows(t, tc, `SELECT count(*) FROM orders`)
		check(t, lunchBasket, nil, true)
		assert.Equal(t, before, countRows(t, tc, `SELECT count(*) FROM orders`))
	})
}
