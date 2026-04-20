package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tsb-service/pkg/timezone"
)

// DaySchedule represents opening hours for a single day.
// nil means closed that day.
type DaySchedule struct {
	Open        string `json:"open"`
	Close       string `json:"close"`
	DinnerOpen  string `json:"dinnerOpen,omitempty"`
	DinnerClose string `json:"dinnerClose,omitempty"`
}

// OpeningHours maps day names to their schedules.
type OpeningHours map[string]*DaySchedule

// RestaurantConfig represents the single-row restaurant configuration.
type RestaurantConfig struct {
	OrderingEnabled    bool            `db:"ordering_enabled" json:"orderingEnabled"`
	OpeningHours       json.RawMessage `db:"opening_hours" json:"openingHours"`
	OrderingHours      json.RawMessage `db:"ordering_hours" json:"orderingHours"`
	PreparationMinutes int             `db:"preparation_minutes" json:"preparationMinutes"`
	UpdatedAt          time.Time       `db:"updated_at" json:"updatedAt"`
}

// GetOpeningHours parses the JSONB opening_hours into a typed map.
func (c *RestaurantConfig) GetOpeningHours() (OpeningHours, error) {
	var hours OpeningHours
	if err := json.Unmarshal(c.OpeningHours, &hours); err != nil {
		return nil, fmt.Errorf("parse opening hours: %w", err)
	}
	return hours, nil
}

// GetOrderingHours parses the JSONB ordering_hours into a typed map.
// Returns nil if ordering_hours is not set.
func (c *RestaurantConfig) GetOrderingHours() (OpeningHours, error) {
	if len(c.OrderingHours) == 0 || string(c.OrderingHours) == "null" {
		return nil, nil
	}
	var hours OpeningHours
	if err := json.Unmarshal(c.OrderingHours, &hours); err != nil {
		return nil, fmt.Errorf("parse ordering hours: %w", err)
	}
	return hours, nil
}

// IsCurrentlyOpen checks if the restaurant is open at the given time,
// honoring any date-specific override for that day.
func (c *RestaurantConfig) IsCurrentlyOpen(now time.Time, overrides map[string]*ScheduleOverride) bool {
	hours, err := c.GetOpeningHours()
	if err != nil {
		return false
	}
	schedule, _ := resolveDaySchedule(now, hours, overrides)
	return isWithinSchedule(schedule, now)
}

// IsOrderingCurrentlyOpen checks if ordering is within scheduled hours.
// Falls back to opening hours if ordering hours are not configured.
func (c *RestaurantConfig) IsOrderingCurrentlyOpen(now time.Time, overrides map[string]*ScheduleOverride) bool {
	orderingHours, err := c.GetOrderingHours()
	if err != nil {
		return false
	}
	if orderingHours == nil {
		return c.IsCurrentlyOpen(now, overrides)
	}
	schedule, _ := resolveDaySchedule(now, orderingHours, overrides)
	return isWithinSchedule(schedule, now)
}

// IsOrderingAllowed returns true if ordering is enabled AND within ordering schedule.
func (c *RestaurantConfig) IsOrderingAllowed(now time.Time, overrides map[string]*ScheduleOverride) bool {
	if !c.OrderingEnabled {
		return false
	}
	return c.IsOrderingCurrentlyOpen(now, overrides)
}

// resolveDaySchedule returns the effective schedule for `now`:
// an override for that date wins over the weekly fallback. When the
// override explicitly closes the day, returns (nil, true).
func resolveDaySchedule(now time.Time, hours OpeningHours, overrides map[string]*ScheduleOverride) (*DaySchedule, bool) {
	local := timezone.In(now)
	dateKey := local.Format("2006-01-02")

	if ov, ok := overrides[dateKey]; ok && ov != nil {
		if ov.Closed {
			return nil, true
		}
		s, err := ov.ParsedSchedule()
		if err != nil || s == nil {
			return nil, true
		}
		return s, false
	}

	dayName := strings.ToLower(local.Weekday().String())
	schedule, exists := hours[dayName]
	if !exists || schedule == nil {
		return nil, false
	}
	return schedule, false
}

// isWithinSchedule checks if the given time falls within any period of
// the resolved schedule. Schedules are wall-clock strings ("HH:MM") in
// the restaurant's timezone, so [now] is converted before comparing.
func isWithinSchedule(schedule *DaySchedule, now time.Time) bool {
	if schedule == nil {
		return false
	}
	local := timezone.In(now)
	currentTime := local.Format("15:04")

	if currentTime >= schedule.Open && currentTime < schedule.Close {
		return true
	}

	if schedule.DinnerOpen != "" && schedule.DinnerClose != "" {
		if currentTime >= schedule.DinnerOpen && currentTime < schedule.DinnerClose {
			return true
		}
	}

	return false
}
