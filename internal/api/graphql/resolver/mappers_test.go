package resolver

import (
	"encoding/json"
	"testing"
	"time"

	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/timezone"
)

func weeklyOpeningHours(t *testing.T) json.RawMessage {
	t.Helper()
	hours := restaurantDomain.OpeningHours{
		"monday":    {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"tuesday":   nil,
		"wednesday": {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"thursday":  {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"friday":    {Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"},
		"saturday":  {Open: "12:00", Close: "15:00", DinnerOpen: "18:00", DinnerClose: "23:00"},
		"sunday":    {Open: "12:00", Close: "15:00", DinnerOpen: "18:00", DinnerClose: "23:00"},
	}
	raw, err := json.Marshal(hours)
	if err != nil {
		t.Fatalf("marshal hours: %v", err)
	}
	return raw
}

// atBrussels returns the given wall-clock time interpreted in Europe/Brussels.
func atBrussels(t *testing.T, dateStr, timeStr string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02 15:04", dateStr+" "+timeStr, timezone.Location)
	if err != nil {
		t.Fatalf("parse %s %s: %v", dateStr, timeStr, err)
	}
	return ts
}

// TestValidatePreferredReadyTime_ServerInUTCAcceptsBrusselsSlot is the regression
// test for the production bug where a server running in UTC compared slot
// wall-clock minutes (UTC) against schedule hours (Brussels), causing valid
// slots like 12:00 Brussels to be rejected as "outside allowed opening slots"
// because in UTC they read 10:00 — before the 11:00 Brussels lunch open.
func TestValidatePreferredReadyTime_ServerInUTCAcceptsBrusselsSlot(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	// Server clock is UTC (typical container env).
	// Brussels Wed 2026-05-13 11:42 CEST == 09:42 UTC.
	now := time.Date(2026, 5, 13, 9, 42, 0, 0, time.UTC)
	// User picks 12:30 Brussels (a valid lunch slot, open 11:00–14:00).
	preferred := atBrussels(t, "2026-05-13", "12:30")

	err := validatePreferredReadyTime(&preferred, cfg, nil, now, true)
	if err != nil {
		t.Fatalf("expected 12:30 Brussels slot to be accepted, got: %v", err)
	}
}

func TestValidatePreferredReadyTime_RejectsSlotOutsideOpeningHours(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	now := time.Date(2026, 5, 13, 13, 0, 0, 0, time.UTC) // 15:00 Brussels
	// 16:30 Brussels falls between lunch (closes 14:00) and dinner (opens 18:00).
	preferred := atBrussels(t, "2026-05-13", "16:30")

	err := validatePreferredReadyTime(&preferred, cfg, nil, now, false)
	if err == nil {
		t.Fatalf("expected slot 16:30 Brussels to be rejected (between services)")
	}
}

func TestValidatePreferredReadyTime_RejectsSlotInPreparationWindow(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	now := atBrussels(t, "2026-05-13", "12:00")
	// 12:15 Brussels is within the 30-min preparation window.
	preferred := atBrussels(t, "2026-05-13", "12:15")

	err := validatePreferredReadyTime(&preferred, cfg, nil, now, true)
	if err == nil {
		t.Fatalf("expected slot within preparation window to be rejected")
	}
}

func TestValidatePreferredReadyTime_NilWhenOpenIsAllowed(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	now := time.Date(2026, 5, 13, 10, 30, 0, 0, time.UTC) // 12:30 Brussels
	if err := validatePreferredReadyTime(nil, cfg, nil, now, true); err != nil {
		t.Fatalf("expected nil preferred to pass when open, got: %v", err)
	}
}

func TestValidatePreferredReadyTime_NilWhenClosedIsRejected(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	now := time.Date(2026, 5, 13, 14, 30, 0, 0, time.UTC) // 16:30 Brussels (between services)
	if err := validatePreferredReadyTime(nil, cfg, nil, now, false); err == nil {
		t.Fatalf("expected nil preferred to be rejected when closed")
	}
}

func TestValidatePreferredReadyTime_OverrideShiftsAllowedInterval(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	// Override Tue 2026-05-12 (weekly closed) → special 14:00–16:00 service.
	schedule, _ := json.Marshal(map[string]string{"open": "14:00", "close": "16:00"})
	ov := &restaurantDomain.ScheduleOverride{
		Date:     atBrussels(t, "2026-05-12", "00:00"),
		Closed:   false,
		Schedule: schedule,
	}
	overrides := map[string]*restaurantDomain.ScheduleOverride{"2026-05-12": ov}

	// Server is UTC. Brussels Tue 13:00 CEST = 11:00 UTC.
	now := time.Date(2026, 5, 12, 11, 0, 0, 0, time.UTC)
	preferred := atBrussels(t, "2026-05-12", "15:00")

	if err := validatePreferredReadyTime(&preferred, cfg, overrides, now, true); err != nil {
		t.Fatalf("expected override slot 15:00 Brussels to be accepted, got: %v", err)
	}
}

func TestValidatePreferredReadyTime_RejectsSlotNotAlignedToQuarter(t *testing.T) {
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       weeklyOpeningHours(t),
		PreparationMinutes: 30,
	}

	now := atBrussels(t, "2026-05-13", "11:00")
	preferred := atBrussels(t, "2026-05-13", "12:07")

	err := validatePreferredReadyTime(&preferred, cfg, nil, now, true)
	if err == nil {
		t.Fatalf("expected slot 12:07 to be rejected for not being quarter-aligned")
	}
}
