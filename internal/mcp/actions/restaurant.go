package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"tsb-service/internal/mcp/upstream"
)

const (
	KindPreparationMinutes = "restaurant.preparation_minutes"
	KindOpeningHours       = "restaurant.opening_hours"
	KindOrderingHours      = "restaurant.ordering_hours"
	KindSchedule           = "restaurant.schedule"
)

// Schedule change origins (stored in params, used to find closures to undo on
// reopening).
const (
	OriginClosure   = "closure"
	OriginReopening = "reopening"
	OriginOverride  = "override"
	OriginUndo      = "undo"
)

// MaxClosureAhead bounds reopen_at.
const MaxClosureAhead = 7 * 24 * time.Hour

// Weekdays in upstream key order.
var Weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

func weekdayKey(t time.Time) string { return strings.ToLower(t.Weekday().String()) }

func parseHHMM(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("invalid time %q, expected HH:MM", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 || (hh == 24 && mm != 0) {
		return 0, fmt.Errorf("invalid time %q, expected HH:MM", s)
	}
	return hh*60 + mm, nil
}

func fmtHHMM(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// ValidateDay checks one day schedule.
func ValidateDay(d *upstream.DaySchedule) error {
	if d == nil {
		return nil
	}
	o, err := parseHHMM(d.Open)
	if err != nil {
		return &UserError{Msg: err.Error()}
	}
	c, err := parseHHMM(d.Close)
	if err != nil {
		return &UserError{Msg: err.Error()}
	}
	if o >= c {
		return Userf("Opening time %s must be before closing time %s.", d.Open, d.Close)
	}
	if (d.DinnerOpen == "") != (d.DinnerClose == "") {
		return Userf("Evening hours need both an opening and a closing time.")
	}
	if d.DinnerOpen != "" {
		do, err := parseHHMM(d.DinnerOpen)
		if err != nil {
			return &UserError{Msg: err.Error()}
		}
		dc, err := parseHHMM(d.DinnerClose)
		if err != nil {
			return &UserError{Msg: err.Error()}
		}
		if do < c {
			return Userf("Evening opening %s must be after the midday closing %s.", d.DinnerOpen, d.Close)
		}
		if do >= dc {
			return Userf("Evening opening %s must be before evening closing %s.", d.DinnerOpen, d.DinnerClose)
		}
	}
	return nil
}

// DescribeDay renders a day schedule, e.g. "11:30-14:30, 18:00-22:00".
func DescribeDay(d *upstream.DaySchedule) string {
	if d == nil {
		return "closed"
	}
	s := d.Open + "-" + d.Close
	if d.DinnerOpen != "" {
		s += ", " + d.DinnerOpen + "-" + d.DinnerClose
	}
	return s
}

func sameDay(a, b *upstream.DaySchedule) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// --- preparation minutes (low) ----------------------------------------------

type PreparationParams struct {
	Minutes int `json:"minutes"`
}

func preparationMinutes() handler {
	return spec[PreparationParams, PreparationParams]{
		Kind: KindPreparationMinutes,
		Risk: RiskLow,
		Prepare: func(ctx context.Context, env *Env, p *PreparationParams) (*prepared, error) {
			if p.Minutes < 1 || p.Minutes > 240 {
				return nil, Userf("Preparation time must be between 1 and 240 minutes.")
			}
			cfg, err := env.Up.RestaurantConfig(ctx)
			if err != nil {
				return nil, err
			}
			pr := &prepared{EntityType: "restaurant", EntityID: "preparation_minutes", Before: PreparationParams{Minutes: cfg.PreparationMinutes}}
			if cfg.PreparationMinutes == p.Minutes {
				pr.NoOp = true
				pr.Summary = fmt.Sprintf("Preparation time is already %d minutes. Nothing changed.", p.Minutes)
				pr.SummaryZh = fmt.Sprintf("备餐时间已经是 %d 分钟，无需更改。", p.Minutes)
				return pr, nil
			}
			pr.Summary = fmt.Sprintf("Preparation time: %d min -> %d min", cfg.PreparationMinutes, p.Minutes)
			pr.SummaryZh = fmt.Sprintf("备餐时间：%d 分钟%s%d 分钟", cfg.PreparationMinutes, arrowZh, p.Minutes)
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p PreparationParams, _ []byte) (any, error) {
			return p, env.Up.UpdatePreparationMinutes(ctx, p.Minutes)
		},
		Inverse: func(_ PreparationParams, b PreparationParams, _ json.RawMessage) (string, any, error) {
			return KindPreparationMinutes, b, nil
		},
	}
}

// --- weekly hours (sensitive) -----------------------------------------------

type HoursParams struct {
	Week upstream.Week `json:"week"`
}

type hoursBefore struct {
	Week upstream.Week `json:"week"`
}

func weeklyHours(kind, label, labelZh string, read func(*upstream.RestaurantConfig) json.RawMessage, write func(*upstream.Client, context.Context, upstream.Week) error) handler {
	return spec[HoursParams, hoursBefore]{
		Kind: kind,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *HoursParams) (*prepared, error) {
			for k, d := range p.Week {
				if !slices.Contains(Weekdays, k) {
					return nil, Userf("Unknown day %q. Use monday to sunday.", k)
				}
				if d == nil {
					delete(p.Week, k)
					continue
				}
				if err := ValidateDay(d); err != nil {
					return nil, Userf("%s: %s", k, err.Error())
				}
			}
			if len(p.Week) == 0 {
				return nil, Userf("The restaurant must be open at least one day a week.")
			}
			cfg, err := env.Up.RestaurantConfig(ctx)
			if err != nil {
				return nil, err
			}
			cur, err := upstream.ParseWeek(read(cfg))
			if err != nil {
				return nil, err
			}
			base := cur
			if base == nil {
				base, _ = upstream.ParseWeek(cfg.OpeningHours)
			}
			var diffs lines
			for _, d := range Weekdays {
				if !sameDay(base[d], p.Week[d]) {
					diffs.add(fmt.Sprintf("%s: %s -> %s", d, DescribeDay(base[d]), DescribeDay(p.Week[d])),
						weekdayZh[d]+"："+describeDayZh(base[d])+arrowZh+describeDayZh(p.Week[d]))
				}
			}
			pr := &prepared{EntityType: "restaurant", EntityID: kind, Before: hoursBefore{Week: cur}}
			if diffs.empty() && cur != nil {
				pr.NoOp = true
				pr.Summary = label + " are already set like this. Nothing changed."
				pr.SummaryZh = labelZh + "已经是这样设置的，无需更改。"
				return pr, nil
			}
			if diffs.empty() {
				diffs.add("same as opening hours, now set explicitly", "与营业时间相同，现在单独设置")
			}
			pr.Summary = label + ": " + diffs.enJoined()
			pr.SummaryZh = labelZh + "：" + diffs.zhJoined()
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p HoursParams, _ []byte) (any, error) {
			return p, write(env.Up, ctx, p.Week)
		},
		Inverse: func(_ HoursParams, b hoursBefore, _ json.RawMessage) (string, any, error) {
			if b.Week == nil {
				return "", nil, Userf("The previous %s followed the opening hours and cannot be restored automatically. Set them again explicitly.", strings.ToLower(label))
			}
			return kind, HoursParams(b), nil
		},
	}
}

// --- date overrides and ordering switch (sensitive) -------------------------

// OverrideSpec sets one date's hours. Schedule nil with Closed=false is
// invalid.
type OverrideSpec struct {
	Date     string                `json:"date"`
	Closed   bool                  `json:"closed"`
	Schedule *upstream.DaySchedule `json:"schedule,omitempty"`
	Note     *string               `json:"note,omitempty"`
}

// ScheduleParams combines the online-ordering switch and date overrides in one
// confirmable change.
type ScheduleParams struct {
	OrderingEnabled *bool          `json:"ordering_enabled,omitempty"`
	Upserts         []OverrideSpec `json:"upserts,omitempty"`
	Deletes         []string       `json:"deletes,omitempty"`
	Origin          string         `json:"origin"`
}

type overrideState struct {
	Closed   bool                  `json:"closed"`
	Schedule *upstream.DaySchedule `json:"schedule,omitempty"`
	Note     *string               `json:"note,omitempty"`
}

type scheduleBefore struct {
	OrderingEnabled *bool                     `json:"ordering_enabled,omitempty"`
	Overrides       map[string]*overrideState `json:"overrides"`
}

func stateOf(o *upstream.ScheduleOverride) *overrideState {
	if o == nil {
		return nil
	}
	st := &overrideState{Closed: o.Closed, Note: o.Note}
	if !o.Closed {
		st.Schedule = o.Schedule
	}
	return st
}

func sameOverride(a *overrideState, b OverrideSpec) bool {
	if a == nil || a.Closed != b.Closed {
		return false
	}
	if !b.Closed && !sameDay(a.Schedule, b.Schedule) {
		return false
	}
	return deref(a.Note) == deref(b.Note)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func describeOverride(st *overrideState, regular *upstream.DaySchedule) string {
	if st == nil {
		return "regular hours (" + DescribeDay(regular) + ")"
	}
	if st.Closed {
		return "closed all day"
	}
	return "special hours " + DescribeDay(st.Schedule)
}

func describeSpec(o OverrideSpec) string {
	s := "special hours " + DescribeDay(o.Schedule)
	if o.Closed {
		s = "closed all day"
	}
	if o.Note != nil && *o.Note != "" {
		s += fmt.Sprintf(" (note: %q)", *o.Note)
	}
	return s
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// fetchOverrides returns the current overrides for the given dates.
func fetchOverrides(ctx context.Context, env *Env, dates []string) (map[string]*upstream.ScheduleOverride, error) {
	out := map[string]*upstream.ScheduleOverride{}
	if len(dates) == 0 {
		return out, nil
	}
	sorted := slices.Sorted(slices.Values(dates))
	from, _ := upstream.OverrideDate(sorted[0])
	to, _ := upstream.OverrideDate(sorted[len(sorted)-1])
	list, err := env.Up.ScheduleOverrides(ctx, from, to.Add(23*time.Hour))
	if err != nil {
		return nil, err
	}
	for i := range list {
		out[list[i].DateKey()] = &list[i]
	}
	return out, nil
}

func schedule() handler {
	return spec[ScheduleParams, scheduleBefore]{
		Kind: KindSchedule,
		Risk: RiskSensitive,
		Prepare: func(ctx context.Context, env *Env, p *ScheduleParams) (*prepared, error) {
			today := env.Now().In(env.Loc).Format(time.DateOnly)
			seen := map[string]bool{}
			var dates []string
			for i := range p.Upserts {
				o := &p.Upserts[i]
				d, err := time.Parse(time.DateOnly, o.Date)
				if err != nil {
					return nil, Userf("Invalid date %q, expected yyyy-mm-dd.", o.Date)
				}
				if o.Date < today {
					return nil, Userf("The date %s is in the past.", o.Date)
				}
				if d.After(env.Now().AddDate(1, 0, 1)) {
					return nil, Userf("The date %s is more than a year ahead.", o.Date)
				}
				if seen[o.Date] {
					return nil, Userf("The date %s appears twice.", o.Date)
				}
				seen[o.Date] = true
				if o.Closed {
					o.Schedule = nil
				} else {
					if o.Schedule == nil {
						return nil, Userf("%s: give the special hours, or mark the day as closed.", o.Date)
					}
					if err := ValidateDay(o.Schedule); err != nil {
						return nil, Userf("%s: %s", o.Date, err.Error())
					}
				}
				if o.Note != nil {
					n := strings.TrimSpace(*o.Note)
					o.Note = &n
					if n == "" {
						o.Note = nil
					}
				}
				dates = append(dates, o.Date)
			}
			for _, d := range p.Deletes {
				if _, err := time.Parse(time.DateOnly, d); err != nil {
					return nil, Userf("Invalid date %q, expected yyyy-mm-dd.", d)
				}
				if seen[d] {
					return nil, Userf("The date %s appears twice.", d)
				}
				seen[d] = true
				dates = append(dates, d)
			}

			cfg, err := env.Up.RestaurantConfig(ctx)
			if err != nil {
				return nil, err
			}
			week, _ := upstream.ParseWeek(cfg.OpeningHours)
			current, err := fetchOverrides(ctx, env, dates)
			if err != nil {
				return nil, err
			}

			before := scheduleBefore{Overrides: map[string]*overrideState{}}
			var changed lines
			if p.OrderingEnabled != nil {
				if *p.OrderingEnabled == cfg.OrderingEnabled {
					p.OrderingEnabled = nil
				} else {
					cur := cfg.OrderingEnabled
					before.OrderingEnabled = &cur
					changed.add(fmt.Sprintf("online ordering: %s -> %s", onOff(cur), onOff(*p.OrderingEnabled)),
						"在线点餐："+onOffZh(cur)+arrowZh+onOffZh(*p.OrderingEnabled))
				}
			}
			var ups []OverrideSpec
			for _, o := range p.Upserts {
				st := stateOf(current[o.Date])
				if sameOverride(st, o) {
					continue
				}
				d, _ := time.Parse(time.DateOnly, o.Date)
				before.Overrides[o.Date] = st
				ups = append(ups, o)
				changed.add(fmt.Sprintf("%s (%s): %s -> %s", o.Date, weekdayKey(d), describeOverride(st, week[weekdayKey(d)]), describeSpec(o)),
					dateZh(o.Date)+"："+describeOverrideZh(st, week[weekdayKey(d)])+arrowZh+describeSpecZh(o))
			}
			var dels []string
			for _, date := range p.Deletes {
				st := stateOf(current[date])
				if st == nil {
					continue
				}
				d, _ := time.Parse(time.DateOnly, date)
				before.Overrides[date] = st
				dels = append(dels, date)
				changed.add(fmt.Sprintf("%s (%s): %s -> regular hours (%s)", date, weekdayKey(d), describeOverride(st, nil), DescribeDay(week[weekdayKey(d)])),
					dateZh(date)+"："+describeOverrideZh(st, nil)+arrowZh+"正常营业时间（"+describeDayZh(week[weekdayKey(d)])+"）")
			}
			p.Upserts, p.Deletes = ups, dels

			pr := &prepared{EntityType: "restaurant", EntityID: "schedule", Before: before}
			if changed.empty() {
				pr.NoOp = true
				pr.Summary = "The schedule is already like this. Nothing changed."
				pr.SummaryZh = "营业安排已经是这样，无需更改。"
				return pr, nil
			}
			pr.Summary = changed.enJoined()
			pr.SummaryZh = changed.zhJoined()
			return pr, nil
		},
		Execute: func(ctx context.Context, env *Env, p ScheduleParams, _ []byte) (any, error) {
			done := 0
			total := len(p.Upserts) + len(p.Deletes)
			if p.OrderingEnabled != nil {
				total++
			}
			partial := func(err error) error {
				if done == 0 {
					return err
				}
				return fmt.Errorf("%w (%d of %d schedule steps were already applied)", err, done, total)
			}
			if p.OrderingEnabled != nil {
				if err := env.Up.UpdateOrderingEnabled(ctx, *p.OrderingEnabled); err != nil {
					return nil, partial(err)
				}
				done++
			}
			for _, o := range p.Upserts {
				date, _ := upstream.OverrideDate(o.Date)
				if err := env.Up.UpsertScheduleOverride(ctx, upstream.ScheduleOverrideInput{Date: date, Closed: o.Closed, Schedule: o.Schedule, Note: o.Note}); err != nil {
					return nil, partial(err)
				}
				done++
			}
			for _, d := range p.Deletes {
				date, _ := upstream.OverrideDate(d)
				if err := env.Up.DeleteScheduleOverride(ctx, date); err != nil {
					return nil, partial(err)
				}
				done++
			}
			return p, nil
		},
		Inverse: func(_ ScheduleParams, b scheduleBefore, _ json.RawMessage) (string, any, error) {
			inv := ScheduleParams{OrderingEnabled: b.OrderingEnabled, Origin: OriginUndo}
			for _, d := range slices.Sorted(maps.Keys(b.Overrides)) {
				st := b.Overrides[d]
				if st == nil {
					inv.Deletes = append(inv.Deletes, d)
					continue
				}
				inv.Upserts = append(inv.Upserts, OverrideSpec{Date: d, Closed: st.Closed, Schedule: st.Schedule, Note: st.Note})
			}
			return KindSchedule, inv, nil
		},
	}
}

// --- closure planning -----------------------------------------------------------

type block struct{ from, to int }

func blocksOf(d *upstream.DaySchedule) []block {
	if d == nil {
		return nil
	}
	var out []block
	add := func(a, b string) {
		x, err1 := parseHHMM(a)
		y, err2 := parseHHMM(b)
		if err1 != nil || err2 != nil {
			return
		}
		if y <= x { // closes after midnight: cap at end of day
			y = 24 * 60
		}
		out = append(out, block{x, y})
	}
	add(d.Open, d.Close)
	if d.DinnerOpen != "" {
		add(d.DinnerOpen, d.DinnerClose)
	}
	return out
}

func subtract(bs []block, cs, ce int) []block {
	var out []block
	for _, b := range bs {
		if ce <= b.from || cs >= b.to {
			out = append(out, b)
			continue
		}
		if cs > b.from {
			out = append(out, block{b.from, cs})
		}
		if ce < b.to {
			out = append(out, block{ce, b.to})
		}
	}
	return out
}

func dayFromBlocks(bs []block) (*upstream.DaySchedule, error) {
	switch len(bs) {
	case 0:
		return nil, nil
	case 1:
		return &upstream.DaySchedule{Open: fmtHHMM(bs[0].from), Close: fmtHHMM(bs[0].to)}, nil
	case 2:
		return &upstream.DaySchedule{Open: fmtHHMM(bs[0].from), Close: fmtHHMM(bs[0].to), DinnerOpen: fmtHHMM(bs[1].from), DinnerClose: fmtHHMM(bs[1].to)}, nil
	default:
		return nil, Userf("This closure would split the day into more than two opening periods, which the restaurant hours cannot express. Close for the whole day or pick different times.")
	}
}

// PlanClosure turns "closed from `from` until `reopen`" into date overrides.
// Each affected day keeps its current hours (override or weekly) minus the
// closed window; a day with nothing left is closed all day.
func PlanClosure(now time.Time, loc *time.Location, week upstream.Week, overrides map[string]*upstream.ScheduleOverride, from time.Time, reopen time.Time, reason *string) (ScheduleParams, error) {
	now = now.In(loc)
	from, reopen = from.In(loc), reopen.In(loc)
	if from.Before(now.Add(-5 * time.Minute)) {
		return ScheduleParams{}, Userf("The closing time %s is in the past.", from.Format("2006-01-02 15:04"))
	}
	if !reopen.After(now) {
		return ScheduleParams{}, Userf("The reopening time %s must be in the future.", reopen.Format("2006-01-02 15:04"))
	}
	if reopen.After(now.Add(MaxClosureAhead)) {
		return ScheduleParams{}, Userf("The reopening time must be within 7 days (latest %s).", now.Add(MaxClosureAhead).Format("2006-01-02 15:04"))
	}
	if !from.Before(reopen) {
		return ScheduleParams{}, Userf("The closing time must be before the reopening time.")
	}

	params := ScheduleParams{Origin: OriginClosure}
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	for ; day.Before(reopen); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		var base *upstream.DaySchedule
		ov := overrides[key]
		switch {
		case ov != nil && ov.Closed:
			base = nil
		case ov != nil:
			base = ov.Schedule
		default:
			base = week[weekdayKey(day)]
		}
		cs, ce := 0, 24*60
		if sameDate(day, from) {
			cs = from.Hour()*60 + from.Minute()
		}
		if sameDate(day, reopen) {
			ce = reopen.Hour()*60 + reopen.Minute()
		}
		remaining := subtract(blocksOf(base), cs, ce)
		if len(remaining) == len(blocksOf(base)) && equalBlocks(remaining, blocksOf(base)) {
			continue // the window does not touch this day's hours
		}
		sched, err := dayFromBlocks(remaining)
		if err != nil {
			return ScheduleParams{}, err
		}
		note := reason
		if note == nil && ov != nil {
			note = ov.Note
		}
		params.Upserts = append(params.Upserts, OverrideSpec{Date: key, Closed: sched == nil, Schedule: sched, Note: note})
	}
	if len(params.Upserts) == 0 {
		return ScheduleParams{}, Userf("The restaurant is not scheduled to be open between %s and %s, so nothing needs to change.",
			from.Format("2006-01-02 15:04"), reopen.Format("2006-01-02 15:04"))
	}
	return params, nil
}

func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func equalBlocks(a, b []block) bool { return slices.Equal(a, b) }

// PlanReopening builds the change that undoes assistant-made closures:
// online ordering back on, and every override set by a closure (today or
// later, still unchanged since) restored to what it was before.
func (s *Service) PlanReopening(ctx context.Context) (ScheduleParams, []string, error) {
	env := s.env
	now := env.Now()
	today := now.In(env.Loc).Format(time.DateOnly)
	cfg, err := env.Up.RestaurantConfig(ctx)
	if err != nil {
		return ScheduleParams{}, nil, err
	}
	params := ScheduleParams{Origin: OriginReopening}
	if !cfg.OrderingEnabled {
		t := true
		params.OrderingEnabled = &t
	}

	entries, err := s.store.AppliedSince(ctx, KindSchedule, now.Add(-MaxClosureAhead-24*time.Hour))
	if err != nil {
		return ScheduleParams{}, nil, err
	}
	// restore[date] = state before the oldest still-relevant closure.
	restore := map[string]*overrideState{}
	expected := map[string]OverrideSpec{}
	for _, e := range slices.Backward(entries) { // oldest first

		if e.UndoneBy != nil {
			continue
		}
		var p ScheduleParams
		var b scheduleBefore
		if json.Unmarshal(e.Params, &p) != nil || json.Unmarshal(e.Before, &b) != nil || p.Origin != OriginClosure {
			continue
		}
		for _, o := range p.Upserts {
			if o.Date < today {
				continue
			}
			if _, ok := expected[o.Date]; !ok {
				restore[o.Date] = b.Overrides[o.Date]
			}
			expected[o.Date] = o
		}
	}

	var notes []string
	dates := slices.Sorted(maps.Keys(expected))
	current, err := fetchOverrides(ctx, env, dates)
	if err != nil {
		return ScheduleParams{}, nil, err
	}
	for _, d := range dates {
		cur := stateOf(current[d])
		if !sameOverride(cur, expected[d]) {
			notes = append(notes, fmt.Sprintf("%s was changed in the dashboard after the closure and is left as is", d))
			continue
		}
		if st := restore[d]; st == nil {
			params.Deletes = append(params.Deletes, d)
		} else {
			params.Upserts = append(params.Upserts, OverrideSpec{Date: d, Closed: st.Closed, Schedule: st.Schedule, Note: st.Note})
		}
	}
	if todays, err := fetchOverrides(ctx, env, []string{today}); err == nil {
		if ov := todays[today]; ov != nil && ov.Closed && !slices.Contains(dates, today) {
			notes = append(notes, fmt.Sprintf("today has a closure set in the dashboard (note: %q); remove it with propose_schedule_override_removal if needed", deref(ov.Note)))
		}
	}
	if params.OrderingEnabled == nil && len(params.Upserts) == 0 && len(params.Deletes) == 0 {
		msg := "Online ordering is already on and there is no closure made by the assistant to cancel."
		if len(notes) > 0 {
			msg += " Note: " + strings.Join(notes, "; ") + "."
		}
		return ScheduleParams{}, notes, &UserError{Msg: msg}
	}
	return params, notes, nil
}
