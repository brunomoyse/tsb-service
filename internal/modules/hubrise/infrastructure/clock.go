package infrastructure

import (
	"time"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/timezone"
)

// SystemClock is the production clock: the current time in Europe/Brussels.
type SystemClock struct{}

var _ domain.Clock = SystemClock{}

// Now returns the current time in the restaurant's time zone.
func (SystemClock) Now() time.Time { return timezone.In(time.Now()) }
