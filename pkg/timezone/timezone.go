// Package timezone exposes the restaurant's wall-clock timezone so user-facing
// timestamps (emails, push notifications, invoices, opening-hours checks) are
// rendered in the local time customers actually see, not the UTC the server
// stores.
package timezone

import (
	"time"

	"go.uber.org/zap"
)

// RestaurantTZ is the IANA timezone of the restaurant. All customer-facing
// times are rendered in this zone.
const RestaurantTZ = "Europe/Brussels"

// Location is the resolved [*time.Location] for [RestaurantTZ]. Falls back to
// UTC with a warning log if the zoneinfo database is unavailable (should never
// happen on a glibc/alpine image with tzdata installed).
var Location *time.Location

func init() {
	loc, err := time.LoadLocation(RestaurantTZ)
	if err != nil {
		zap.L().Warn("failed to load restaurant timezone, falling back to UTC",
			zap.String("tz", RestaurantTZ),
			zap.Error(err))
		Location = time.UTC
		return
	}
	Location = loc
}

// In returns t converted to the restaurant's local timezone. The underlying
// instant is unchanged; only the wall-clock representation is adjusted.
func In(t time.Time) time.Time {
	return t.In(Location)
}
