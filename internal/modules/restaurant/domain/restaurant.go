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
	OpeningHours    json.RawMessage `db:"opening_hours" json:"openingHours"`
	UpdatedAt       time.Time       `db:"updated_at" json:"updatedAt"`
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

	dayName := strings.ToLower(now.Weekday().String())
	schedule, exists := hours[dayName]
	if !exists || schedule == nil {
		return false
	}

	currentTime := now.Format("15:04")

	// Check lunch period
	if currentTime >= schedule.Open && currentTime < schedule.Close {
		return true
	}

	// Check dinner period if defined
	if schedule.DinnerOpen != "" && schedule.DinnerClose != "" {
		if currentTime >= schedule.DinnerOpen && currentTime < schedule.DinnerClose {
			return true
		}
	}

	return false
}

// IsOrderingAllowed returns true if ordering is enabled AND the restaurant is currently open.
func (c *RestaurantConfig) IsOrderingAllowed(now time.Time) bool {
	if !c.OrderingEnabled {
		return false
	}
	return c.IsCurrentlyOpen(now)
}
