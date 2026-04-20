package domain

import (
	"encoding/json"
	"testing"
	"time"

	"tsb-service/pkg/timezone"
)

// helper: a standard "always open 11-14 and 18-22" weekly grid
func weeklyHours(t *testing.T) json.RawMessage {
	t.Helper()
	hours := OpeningHours{
		"monday":    {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"tuesday":   nil, // closed
		"wednesday": {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"thursday":  {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"friday":    {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"saturday":  {Open: "12:00", Close: "15:00", DinnerOpen: "18:00", DinnerClose: "23:00"},
		"sunday":    {Open: "12:00", Close: "15:00", DinnerOpen: "18:00", DinnerClose: "23:00"},
	}
	raw, err := json.Marshal(hours)
	if err != nil {
		t.Fatalf("marshal weekly hours: %v", err)
	}
	return raw
}

func configWith(t *testing.T, opening json.RawMessage, prep int) *RestaurantConfig {
	t.Helper()
	return &RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       opening,
		PreparationMinutes: prep,
	}
}

// at returns a time.Time at the given wall-clock in the restaurant tz.
func at(t *testing.T, dateStr, timeStr string) time.Time {
	t.Helper()
	layout := "2006-01-02 15:04"
	ts, err := time.ParseInLocation(layout, dateStr+" "+timeStr, timezone.Location)
	if err != nil {
		t.Fatalf("parse %s %s: %v", dateStr, timeStr, err)
	}
	return ts
}

func TestIsCurrentlyOpen_WeeklyGrid(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)

	// Wed 2026-04-22 at 12:30 → lunch open
	if !cfg.IsCurrentlyOpen(at(t, "2026-04-22", "12:30"), nil) {
		t.Errorf("expected open at Wed 12:30")
	}
	// Wed 2026-04-22 at 16:00 → between services
	if cfg.IsCurrentlyOpen(at(t, "2026-04-22", "16:00"), nil) {
		t.Errorf("expected closed at Wed 16:00")
	}
	// Tue 2026-04-21 at 12:30 → weekly closed
	if cfg.IsCurrentlyOpen(at(t, "2026-04-21", "12:30"), nil) {
		t.Errorf("expected closed on Tuesday")
	}
}

func TestIsCurrentlyOpen_OverrideClosed(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)

	// Override Wed 2026-04-22 as closed
	ov := &ScheduleOverride{
		Date:   at(t, "2026-04-22", "00:00"),
		Closed: true,
	}
	overrides := map[string]*ScheduleOverride{"2026-04-22": ov}

	if cfg.IsCurrentlyOpen(at(t, "2026-04-22", "12:30"), overrides) {
		t.Errorf("expected closed by override at Wed 12:30")
	}
	// Next day unaffected
	if !cfg.IsCurrentlyOpen(at(t, "2026-04-23", "12:30"), overrides) {
		t.Errorf("expected still open Thursday 12:30")
	}
}

func TestIsCurrentlyOpen_OverrideSpecialHours(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)

	// Override Tuesday (normally closed) with afternoon-only hours
	schedule, _ := json.Marshal(map[string]string{"open": "14:00", "close": "16:00"})
	ov := &ScheduleOverride{
		Date:     at(t, "2026-04-21", "00:00"),
		Closed:   false,
		Schedule: schedule,
	}
	overrides := map[string]*ScheduleOverride{"2026-04-21": ov}

	if !cfg.IsCurrentlyOpen(at(t, "2026-04-21", "15:00"), overrides) {
		t.Errorf("expected open by override at Tue 15:00")
	}
	if cfg.IsCurrentlyOpen(at(t, "2026-04-21", "12:30"), overrides) {
		t.Errorf("expected closed by override at Tue 12:30")
	}
}

func TestAvailableSlotsToday_UsesPreparationMinutes(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 60) // 1h prep

	// Wed 2026-04-22 at 17:00 → first dinner slot should be 18:30 (open 18 + 30 prep)? No:
	// min(now+prep rounded up, open+prep rounded up) → max of (18:00, 18:00) = 18:00 rounded up
	// Actually: openPlusPrep = 19:00 (18:00 + 60). minAllowed = 18:00 (17+1h). Start = max(19:00, 18:00) = 19:00 rounded up = 19:00.
	now := at(t, "2026-04-22", "17:00")
	slots := cfg.AvailableSlotsToday(now, nil)
	if len(slots) == 0 {
		t.Fatalf("expected some slots")
	}
	if slots[0].Label != "19:00" {
		t.Errorf("expected first dinner slot 19:00 with 60min prep, got %s", slots[0].Label)
	}
}

func TestAvailableSlotsToday_OverrideClosedReturnsEmpty(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	ov := &ScheduleOverride{
		Date:   at(t, "2026-04-22", "00:00"),
		Closed: true,
	}
	overrides := map[string]*ScheduleOverride{"2026-04-22": ov}

	slots := cfg.AvailableSlotsToday(at(t, "2026-04-22", "10:00"), overrides)
	if len(slots) != 0 {
		t.Errorf("expected no slots when override closes the day, got %d", len(slots))
	}
}

func TestAvailableSlotsToday_OverrideSpecialHours(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	// Tuesday (weekly closed) forced to 14:00-16:00
	schedule, _ := json.Marshal(map[string]string{"open": "14:00", "close": "16:00"})
	ov := &ScheduleOverride{
		Date:     at(t, "2026-04-21", "00:00"),
		Closed:   false,
		Schedule: schedule,
	}
	overrides := map[string]*ScheduleOverride{"2026-04-21": ov}

	// now = Tue 13:00 → openPlusPrep = 14:30, minAllowed = 13:30 → start = 14:30.
	// Last slot ≤ 16:00 → 14:30, 14:45, 15:00, 15:15, 15:30, 15:45, 16:00.
	slots := cfg.AvailableSlotsToday(at(t, "2026-04-21", "13:00"), overrides)
	if len(slots) == 0 {
		t.Fatalf("expected slots")
	}
	if slots[0].Label != "14:30" {
		t.Errorf("expected first slot 14:30, got %s", slots[0].Label)
	}
	last := slots[len(slots)-1].Label
	if last != "16:00" {
		t.Errorf("expected last slot 16:00, got %s", last)
	}
}

func TestNextOpeningAt_SkipsOverrideClosedDay(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)

	// Wed 23:30 → next opening should be Thu 11:00
	next := cfg.NextOpeningAt(at(t, "2026-04-22", "23:30"), nil)
	if next == nil {
		t.Fatalf("expected next opening")
	}
	got := timezone.In(*next).Format("2006-01-02 15:04")
	if got != "2026-04-23 11:00" {
		t.Errorf("expected next opening Thu 2026-04-23 11:00, got %s", got)
	}

	// With Thursday override-closed, next should skip to Friday
	ov := &ScheduleOverride{
		Date:   at(t, "2026-04-23", "00:00"),
		Closed: true,
	}
	overrides := map[string]*ScheduleOverride{"2026-04-23": ov}

	next = cfg.NextOpeningAt(at(t, "2026-04-22", "23:30"), overrides)
	if next == nil {
		t.Fatalf("expected next opening")
	}
	got = timezone.In(*next).Format("2006-01-02 15:04")
	if got != "2026-04-24 11:00" {
		t.Errorf("expected Fri 11:00 after skipping Thu closure, got %s", got)
	}
}

func TestIsOrderingAllowed_OrderingDisabledOverridesEverything(t *testing.T) {
	cfg := configWith(t, weeklyHours(t), 30)
	cfg.OrderingEnabled = false

	if cfg.IsOrderingAllowed(at(t, "2026-04-22", "12:30"), nil) {
		t.Errorf("expected ordering disallowed when OrderingEnabled=false even during open hours")
	}
}
