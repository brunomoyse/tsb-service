package domain

import (
	"strconv"
	"strings"
	"time"

	"tsb-service/pkg/timezone"
)

// TimeSlot is a single bookable ordering slot.
type TimeSlot struct {
	Label string    // wall-clock "HH:MM" in restaurant timezone
	Value time.Time // exact instant (tz-aware)
}

const slotStepMinutes = 15

// AvailableSlotsToday returns all ordering slots that are still bookable
// for the current local day, honoring overrides, ordering hours (or opening
// hours fallback) and the configured preparation buffer. Slots are rounded
// up to the next quarter-hour.
func (c *RestaurantConfig) AvailableSlotsToday(now time.Time, overrides map[string]*ScheduleOverride) []TimeSlot {
	orderingHours, err := c.GetOrderingHours()
	if err != nil {
		return nil
	}
	var hours OpeningHours
	if orderingHours != nil {
		hours = orderingHours
	} else {
		hours, err = c.GetOpeningHours()
		if err != nil {
			return nil
		}
	}

	schedule, _ := resolveDaySchedule(now, hours, overrides)
	if schedule == nil {
		return nil
	}

	local := timezone.In(now)
	prep := c.PreparationMinutes
	if prep <= 0 {
		prep = 30
	}
	minAllowed := roundUpToNextQuarter(local.Add(time.Duration(prep) * time.Minute))

	intervals := [][2]string{{schedule.Open, schedule.Close}}
	if schedule.DinnerOpen != "" && schedule.DinnerClose != "" {
		intervals = append(intervals, [2]string{schedule.DinnerOpen, schedule.DinnerClose})
	}

	var slots []TimeSlot
	seen := make(map[time.Time]struct{})

	for _, iv := range intervals {
		openMins, okOpen := parseHHMM(iv[0])
		closeMins, okClose := parseHHMM(iv[1])
		if !okOpen || !okClose {
			continue
		}
		// The "openPlusPreparation" rule enforces that the first slot of a
		// service cannot be sooner than (open + prep), even if `now` is much
		// earlier in the day.
		openPlusPrep := atLocalMinutes(local, openMins+prep)
		intervalEnd := atLocalMinutes(local, closeMins)
		if intervalEnd.Before(openPlusPrep) {
			continue
		}

		start := roundUpToNextQuarter(maxTime(openPlusPrep, minAllowed))
		for cur := start; !cur.After(intervalEnd); cur = cur.Add(slotStepMinutes * time.Minute) {
			if _, dup := seen[cur]; dup {
				continue
			}
			seen[cur] = struct{}{}
			slots = append(slots, TimeSlot{
				Label: cur.Format("15:04"),
				Value: cur,
			})
		}
	}

	return slots
}

// NextOpeningAt returns the next instant at which the restaurant opens
// (based on opening hours, honoring overrides). Looks ahead up to 7 days.
// Returns nil when no opening is found in that window.
func (c *RestaurantConfig) NextOpeningAt(now time.Time, overrides map[string]*ScheduleOverride) *time.Time {
	hours, err := c.GetOpeningHours()
	if err != nil {
		return nil
	}

	local := timezone.In(now)

	for offset := 0; offset < 7; offset++ {
		day := local.AddDate(0, 0, offset)
		schedule, _ := resolveDaySchedule(day, hours, overrides)
		if schedule == nil {
			continue
		}

		openings := []string{schedule.Open}
		if schedule.DinnerOpen != "" {
			openings = append(openings, schedule.DinnerOpen)
		}

		for _, o := range openings {
			mins, ok := parseHHMM(o)
			if !ok {
				continue
			}
			candidate := atLocalMinutes(day, mins)
			if !candidate.After(local) {
				continue
			}
			return &candidate
		}
	}

	return nil
}

func parseHHMM(s string) (int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	return h*60 + m, true
}

// atLocalMinutes returns a time.Time at the given minutes-since-midnight,
// on the same date as `base`, in the restaurant timezone.
func atLocalMinutes(base time.Time, minutes int) time.Time {
	local := timezone.In(base)
	return time.Date(local.Year(), local.Month(), local.Day(), minutes/60, minutes%60, 0, 0, local.Location())
}

func roundUpToNextQuarter(t time.Time) time.Time {
	t = t.Truncate(time.Minute)
	m := t.Minute()
	rem := m % slotStepMinutes
	if rem == 0 {
		return t
	}
	return t.Add(time.Duration(slotStepMinutes-rem) * time.Minute)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
