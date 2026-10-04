// Package timezonetest helps tests whose outcome depends on the restaurant's calendar day.
package timezonetest

import (
	"testing"
	"time"

	"tsb-service/pkg/timezone"
)

// SkipNearMidnight skips the test when the restaurant's local midnight is less than window away, so
// a test that reads "today" more than once cannot straddle two calendar days. Pick a window longer
// than the test.
func SkipNearMidnight(t *testing.T, window time.Duration) {
	t.Helper()
	if left := untilMidnight(time.Now()); left < window {
		t.Skipf("local midnight is %s away, closer than the %s this test needs", left.Round(time.Second), window)
	}
}

// untilMidnight is the time from now to the start of the next local calendar day.
func untilMidnight(now time.Time) time.Duration {
	local := timezone.In(now)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, local.Location())
	return next.Sub(now)
}
