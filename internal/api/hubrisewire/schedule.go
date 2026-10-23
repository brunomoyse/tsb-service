package hubrisewire

import (
	"context"
	"time"

	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/timezone"
)

// orderingHours adapts the restaurant service to the reconciler's Schedule: whether the ordering hours
// (with the overrides of that day) covered an instant. The switch that pauses ordering is left out on
// purpose: an order pushed before ordering was paused still needs somebody to accept it.
type orderingHours struct {
	svc restaurantApplication.RestaurantService
}

func (o orderingHours) OrderingOpenAt(ctx context.Context, at time.Time) (bool, error) {
	if o.svc.IsDevMode() {
		return true, nil
	}
	config, err := o.svc.GetConfig(ctx)
	if err != nil {
		return false, err
	}
	local := timezone.In(at)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	list, err := o.svc.ListOverrides(ctx, day, day)
	if err != nil {
		return false, err
	}
	overrides := make(map[string]*restaurantDomain.ScheduleOverride, len(list))
	for _, ov := range list {
		overrides[ov.DateKey()] = ov
	}
	return config.IsOrderingCurrentlyOpen(at, overrides), nil
}
