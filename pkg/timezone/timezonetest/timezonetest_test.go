package timezonetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"tsb-service/pkg/timezone"
)

func TestUntilMidnight(t *testing.T) {
	// Brussels is UTC+2 in July: 21:30 UTC is 23:30 local, half an hour before midnight.
	assert.Equal(t, 30*time.Minute, untilMidnight(time.Date(2026, 7, 1, 21, 30, 0, 0, time.UTC)))
	// Just after local midnight almost a whole day is left.
	assert.Equal(t, 24*time.Hour-time.Minute, untilMidnight(time.Date(2026, 7, 1, 22, 1, 0, 0, time.UTC)))
	// On the spring-forward day (2026-03-29, 02:00 -> 03:00) the day is only 23 hours long.
	start := time.Date(2026, 3, 29, 0, 0, 0, 0, timezone.In(time.Now()).Location())
	assert.Equal(t, 23*time.Hour, untilMidnight(start))
}

func TestSkipNearMidnightRunsTheTestFarFromMidnight(t *testing.T) {
	if untilMidnight(time.Now()) < time.Second {
		t.Skip("too close to midnight to assert anything about this")
	}
	ran := false
	t.Run("inner", func(t *testing.T) {
		SkipNearMidnight(t, 0) // a zero window never skips
		ran = true
	})
	assert.True(t, ran)
}

func TestSkipNearMidnightSkipsWithinTheWindow(t *testing.T) {
	skipped := true
	t.Run("inner", func(t *testing.T) {
		SkipNearMidnight(t, 48*time.Hour) // always within 48 h of the next midnight
		skipped = false
	})
	assert.True(t, skipped, "a window longer than a day always skips")
}
