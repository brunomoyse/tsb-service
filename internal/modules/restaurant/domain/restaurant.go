package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
	OrderingEnabled bool            `db:"ordering_enabled" json:"orderingEnabled"`
	// SystemDisableReason is populated when ordering was disabled
	// automatically by the system (e.g. HubRise circuit breaker).
	// NULL means either ordering is enabled, or the admin manually
	// disabled it. The distinction lets tsb-core render a different
	// message ("rappelez-nous" for system, generic otherwise) and
	// lets admin manual toggles override automatic re-enables.
	SystemDisableReason *string         `db:"system_disable_reason" json:"systemDisableReason"`
	OpeningHours        json.RawMessage `db:"opening_hours" json:"openingHours"`
	OrderingHours       json.RawMessage `db:"ordering_hours" json:"orderingHours"`
	UpdatedAt           time.Time       `db:"updated_at" json:"updatedAt"`
}

// GetOpeningHours parses the JSONB opening_hours into a typed map.
func (c *RestaurantConfig) GetOpeningHours() (OpeningHours, error) {
	var hours OpeningHours
	if err := json.Unmarshal(c.OpeningHours, &hours); err != nil {
		return nil, fmt.Errorf("parse opening hours: %w", err)
	}
	return hours, nil
}

// IsCurrentlyOpen checks if the restaurant is open at the given time,
// based on the opening hours schedule.
func (c *RestaurantConfig) IsCurrentlyOpen(now time.Time) bool {
	hours, err := c.GetOpeningHours()
	if err != nil {
		return false
	}
	return isWithinSchedule(hours, now)
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

// IsOrderingCurrentlyOpen checks if ordering is within scheduled hours.
// Falls back to opening hours if ordering hours are not configured.
func (c *RestaurantConfig) IsOrderingCurrentlyOpen(now time.Time) bool {
	orderingHours, err := c.GetOrderingHours()
	if err != nil || orderingHours == nil {
		return c.IsCurrentlyOpen(now)
	}
	return isWithinSchedule(orderingHours, now)
}

// isWithinSchedule checks if the given time falls within any period of the schedule for that day.
func isWithinSchedule(hours OpeningHours, now time.Time) bool {
	dayName := strings.ToLower(now.Weekday().String())
	schedule, exists := hours[dayName]
	if !exists || schedule == nil {
		return false
	}

	currentTime := now.Format("15:04")

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

// IsOrderingAllowed returns true if ordering is enabled AND within ordering schedule.
func (c *RestaurantConfig) IsOrderingAllowed(now time.Time) bool {
	if !c.OrderingEnabled {
		return false
	}
	return c.IsOrderingCurrentlyOpen(now)
}
