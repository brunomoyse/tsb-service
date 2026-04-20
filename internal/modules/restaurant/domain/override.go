package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// ScheduleOverride represents a date-specific deviation from the weekly
// opening hours (e.g. holiday closure or special hours).
type ScheduleOverride struct {
	Date      time.Time       `db:"date"`
	Closed    bool            `db:"closed"`
	Schedule  json.RawMessage `db:"schedule"`
	Note      *string         `db:"note"`
	CreatedAt time.Time       `db:"created_at"`
	UpdatedAt time.Time       `db:"updated_at"`
}

// ParsedSchedule returns the override's custom schedule, or nil when
// the override marks the day as fully closed.
func (o *ScheduleOverride) ParsedSchedule() (*DaySchedule, error) {
	if o.Closed || len(o.Schedule) == 0 || string(o.Schedule) == "null" {
		return nil, nil
	}
	var s DaySchedule
	if err := json.Unmarshal(o.Schedule, &s); err != nil {
		return nil, fmt.Errorf("parse override schedule: %w", err)
	}
	return &s, nil
}

// DateKey returns the override date formatted as "2006-01-02" in the
// restaurant's timezone (matches the lookup key used during resolution).
func (o *ScheduleOverride) DateKey() string {
	return o.Date.Format("2006-01-02")
}
