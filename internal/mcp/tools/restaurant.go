package tools

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/upstream"
)

// DayHours is one day of hours in tool input/output.
type DayHours struct {
	Closed      bool   `json:"closed,omitempty" jsonschema:"true when closed all day"`
	Open        string `json:"open,omitempty" jsonschema:"HH:MM"`
	Close       string `json:"close,omitempty" jsonschema:"HH:MM"`
	DinnerOpen  string `json:"dinner_open,omitempty" jsonschema:"HH:MM, optional second (evening) period"`
	DinnerClose string `json:"dinner_close,omitempty" jsonschema:"HH:MM"`
}

func dayHoursOut(d *upstream.DaySchedule) DayHours {
	if d == nil {
		return DayHours{Closed: true}
	}
	return DayHours{Open: d.Open, Close: d.Close, DinnerOpen: d.DinnerOpen, DinnerClose: d.DinnerClose}
}

func (h DayHours) schedule() *upstream.DaySchedule {
	if h.Closed {
		return nil
	}
	return &upstream.DaySchedule{Open: strings.TrimSpace(h.Open), Close: strings.TrimSpace(h.Close), DinnerOpen: strings.TrimSpace(h.DinnerOpen), DinnerClose: strings.TrimSpace(h.DinnerClose)}
}

func weekOut(w upstream.Week) map[string]DayHours {
	out := map[string]DayHours{}
	for _, d := range actions.Weekdays {
		out[d] = dayHoursOut(w[d])
	}
	return out
}

// RestaurantConfigOut is returned by get_restaurant_config.
type RestaurantConfigOut struct {
	OrderingEnabled    bool                `json:"ordering_enabled" jsonschema:"false when online ordering is paused"`
	OpenNow            bool                `json:"open_now" jsonschema:"inside today's opening hours"`
	OrderingOpenNow    bool                `json:"ordering_open_now" jsonschema:"customers can order right now"`
	NextOpeningAt      string              `json:"next_opening_at,omitempty"`
	PreparationMinutes int                 `json:"preparation_minutes"`
	OpeningHours       map[string]DayHours `json:"opening_hours"`
	OrderingHours      map[string]DayHours `json:"ordering_hours,omitempty" jsonschema:"absent when ordering follows the opening hours"`
}

func (d *Deps) restaurantConfig(ctx context.Context) (RestaurantConfigOut, error) {
	cfg, err := d.Up.RestaurantConfig(ctx)
	if err != nil {
		return RestaurantConfigOut{}, err
	}
	opening, _ := upstream.ParseWeek(cfg.OpeningHours)
	ordering, _ := upstream.ParseWeek(cfg.OrderingHours)
	out := RestaurantConfigOut{OrderingEnabled: cfg.OrderingEnabled, OpenNow: cfg.IsCurrentlyOpen, OrderingOpenNow: cfg.IsOrderingCurrentlyOpen,
		NextOpeningAt: d.fmtTimePtr(cfg.NextOpeningAt), PreparationMinutes: cfg.PreparationMinutes, OpeningHours: weekOut(opening)}
	if ordering != nil {
		out.OrderingHours = weekOut(ordering)
	}
	return out, nil
}

// OverrideOut is a date override in tool output.
type OverrideOut struct {
	Date    string   `json:"date"`
	Weekday string   `json:"weekday"`
	Hours   DayHours `json:"hours"`
	Note    string   `json:"note,omitempty"`
}

func overrideOut(o upstream.ScheduleOverride) OverrideOut {
	out := OverrideOut{Date: o.DateKey(), Weekday: strings.ToLower(o.Date.UTC().Weekday().String()), Hours: DayHours{Closed: true}}
	if !o.Closed {
		out.Hours = dayHoursOut(o.Schedule)
	}
	if o.Note != nil {
		out.Note = *o.Note
	}
	return out
}

func registerRestaurant(s *mcp.Server, d *Deps) {
	type Empty struct{}
	add(s, d, &mcp.Tool{
		Name: "get_restaurant_config", Annotations: readOnly,
		Description: describe(`Online ordering switch, whether the restaurant and ordering are open right now, next opening time, preparation time, and weekly opening and ordering hours.`,
			`get_restaurant_config({})`),
	}, func(ctx context.Context, _ Empty) (RestaurantConfigOut, error) {
		return d.restaurantConfig(ctx)
	})

	type OverridesIn struct {
		From string `json:"from,omitempty" jsonschema:"yyyy-mm-dd, default today"`
		To   string `json:"to,omitempty" jsonschema:"yyyy-mm-dd, default 60 days after from"`
	}
	type OverridesOut struct {
		Overrides []OverrideOut `json:"overrides"`
	}
	add(s, d, &mcp.Tool{
		Name: "list_schedule_overrides", Annotations: readOnly,
		Description: describe(`List date-specific closures and special hours (holidays, exceptional closures) between two dates.`,
			`list_schedule_overrides({"from": "2026-12-20", "to": "2027-01-05"})`),
	}, func(ctx context.Context, in OverridesIn) (OverridesOut, error) {
		from, to := d.today(), ""
		var err error
		if in.From != "" {
			if from, err = parseDate("from", in.From); err != nil {
				return OverridesOut{}, err
			}
		}
		f, _ := upstream.OverrideDate(from)
		t := f.AddDate(0, 0, 60)
		if in.To != "" {
			if to, err = parseDate("to", in.To); err != nil {
				return OverridesOut{}, err
			}
			t, _ = upstream.OverrideDate(to)
		}
		list, err := d.Up.ScheduleOverrides(ctx, f, t.Add(23*time.Hour))
		if err != nil {
			return OverridesOut{}, err
		}
		out := OverridesOut{Overrides: []OverrideOut{}}
		for _, o := range list {
			out.Overrides = append(out.Overrides, overrideOut(o))
		}
		return out, nil
	})

	type PrepIn struct {
		Minutes int `json:"minutes" jsonschema:"1 to 240"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "set_preparation_minutes", Annotations: lowRisk,
		Description: describe(`Set the kitchen preparation time used for customer pickup and delivery slots (1-240 minutes). Applied immediately and logged; reversible with undo_last_change.`,
			`set_preparation_minutes({"minutes": 45, "request_context": "今天很忙，准备时间45分钟"})`),
	}, func(ctx context.Context, in PrepIn) (ApplyOut, error) {
		return d.applyNow(ctx, "set_preparation_minutes", actions.KindPreparationMinutes, actions.PreparationParams{Minutes: in.Minutes}, in.RequestContext)
	})

	type ClosureIn struct {
		From     string `json:"from,omitempty" jsonschema:"when the closure starts, ISO 8601; default now. Only used with reopen_at or for a closure later today"`
		ReopenAt string `json:"reopen_at,omitempty" jsonschema:"when the restaurant opens again, ISO 8601, within 7 days. Omit to pause online ordering until reopened manually"`
		Reason   string `json:"reason,omitempty" jsonschema:"short note shown in the dashboard"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_restaurant_closure", Annotations: proposeOnly,
		Description: describe(`Propose closing the restaurant. Without reopen_at (and without from): pauses online ordering now until propose_restaurant_reopening. With reopen_at: sets special hours for each affected day so the restaurant is closed from "from" (default now) until reopen_at and opens again automatically (e.g. "close at 15:00, back at 18:00"). Creates a pending change only.`,
			`propose_restaurant_closure({"from": "2026-10-03T15:00", "reopen_at": "2026-10-03T18:00", "reason": "staff meeting", "request_context": "下午三点关门，六点再开"})`),
	}, func(ctx context.Context, in ClosureIn) (actions.Proposal, error) {
		var reason *string
		if r := strings.TrimSpace(in.Reason); r != "" {
			reason = &r
		}
		now := d.Now()
		if in.ReopenAt == "" && in.From == "" {
			off := false
			return d.propose(ctx, "propose_restaurant_closure", actions.KindSchedule, actions.ScheduleParams{OrderingEnabled: &off, Origin: actions.OriginClosure}, nil, in.RequestContext)
		}
		from := now
		if in.From != "" {
			t, err := d.parseTime("from", in.From)
			if err != nil {
				return actions.Proposal{}, err
			}
			from = t
		}
		var reopen time.Time
		if in.ReopenAt != "" {
			t, err := d.parseTime("reopen_at", in.ReopenAt)
			if err != nil {
				return actions.Proposal{}, err
			}
			reopen = t
		} else {
			fl := from.In(d.Loc)
			reopen = time.Date(fl.Year(), fl.Month(), fl.Day(), 0, 0, 0, 0, d.Loc).AddDate(0, 0, 1)
		}
		cfg, err := d.Up.RestaurantConfig(ctx)
		if err != nil {
			return actions.Proposal{}, err
		}
		week, _ := upstream.ParseWeek(cfg.OpeningHours)
		start := from.In(d.Loc).Format(time.DateOnly)
		f, _ := upstream.OverrideDate(start)
		e, _ := upstream.OverrideDate(reopen.In(d.Loc).Format(time.DateOnly))
		list, err := d.Up.ScheduleOverrides(ctx, f, e.Add(23*time.Hour))
		if err != nil {
			return actions.Proposal{}, err
		}
		overrides := map[string]*upstream.ScheduleOverride{}
		for i := range list {
			overrides[list[i].DateKey()] = &list[i]
		}
		params, err := actions.PlanClosure(now, d.Loc, week, overrides, from, reopen, reason)
		if err != nil {
			return actions.Proposal{}, err
		}
		return d.propose(ctx, "propose_restaurant_closure", actions.KindSchedule, params, nil, in.RequestContext)
	})

	type ReopenIn struct {
		WriteContext
	}
	type ReopenOut struct {
		actions.Proposal
		Notes []string `json:"notes"`
	}
	add(s, d, &mcp.Tool{
		Name: "propose_restaurant_reopening", Annotations: proposeOnly,
		Description: describe(`Propose reopening: switches online ordering back on if paused, and cancels closures made through propose_restaurant_closure (today and later) that were not changed in the dashboard since. Closures set in the dashboard are left alone and listed in notes. Creates a pending change only.`,
			`propose_restaurant_reopening({"request_context": "现在开门了"})`),
	}, func(ctx context.Context, in ReopenIn) (ReopenOut, error) {
		params, notes, err := d.Svc.PlanReopening(ctx)
		if err != nil {
			return ReopenOut{}, err
		}
		p, err := d.propose(ctx, "propose_restaurant_reopening", actions.KindSchedule, params, nil, in.RequestContext)
		if err != nil {
			return ReopenOut{}, err
		}
		if notes == nil {
			notes = []string{}
		}
		return ReopenOut{Proposal: p, Notes: notes}, nil
	})

	type HoursIn struct {
		Days map[string]DayHours `json:"days" jsonschema:"days to change, keyed monday..sunday; other days keep their current hours"`
		WriteContext
	}
	hoursTool := func(name, kind, label string, current func(*upstream.RestaurantConfig) []byte) {
		add(s, d, &mcp.Tool{
			Name: name, Annotations: proposeOnly,
			Description: describe(`Propose new weekly `+label+` for some days (permanent, every week). Only the given days change. For a single date (holiday, one-off closure) use propose_schedule_override instead. Creates a pending change only.`,
				name+`({"days": {"monday": {"closed": true}, "tuesday": {"open": "11:30", "close": "14:30", "dinner_open": "18:00", "dinner_close": "22:00"}}, "request_context": "以后星期一休息"})`),
		}, func(ctx context.Context, in HoursIn) (actions.Proposal, error) {
			if len(in.Days) == 0 {
				return actions.Proposal{}, actions.Userf("Give at least one day to change.")
			}
			cfg, err := d.Up.RestaurantConfig(ctx)
			if err != nil {
				return actions.Proposal{}, err
			}
			week, _ := upstream.ParseWeek(current(cfg))
			if week == nil { // ordering hours follow opening hours until set
				week, _ = upstream.ParseWeek(cfg.OpeningHours)
			}
			if week == nil {
				week = upstream.Week{}
			}
			for day, h := range in.Days {
				day = strings.ToLower(strings.TrimSpace(day))
				week[day] = h.schedule()
			}
			return d.propose(ctx, name, kind, actions.HoursParams{Week: week}, nil, in.RequestContext)
		})
	}
	hoursTool("propose_opening_hours", actions.KindOpeningHours, "opening hours", func(c *upstream.RestaurantConfig) []byte { return c.OpeningHours })
	hoursTool("propose_ordering_hours", actions.KindOrderingHours, "online ordering hours", func(c *upstream.RestaurantConfig) []byte { return c.OrderingHours })

	type OverrideIn struct {
		Date    string `json:"date" jsonschema:"yyyy-mm-dd"`
		EndDate string `json:"end_date,omitempty" jsonschema:"yyyy-mm-dd, to apply the same hours to a range of up to 31 days"`
		DayHours
		Note string `json:"note,omitempty" jsonschema:"short note shown in the dashboard, e.g. Christmas"`
		WriteContext
	}
	dateRange := func(start, end string) ([]string, error) {
		s, err := parseDate("date", start)
		if err != nil {
			return nil, err
		}
		if end == "" {
			return []string{s}, nil
		}
		e, err := parseDate("end_date", end)
		if err != nil {
			return nil, err
		}
		if e < s {
			return nil, actions.Userf("end_date must not be before date.")
		}
		var out []string
		t, _ := time.Parse(time.DateOnly, s)
		for k := t.Format(time.DateOnly); k <= e; t, k = t.AddDate(0, 0, 1), t.AddDate(0, 0, 1).Format(time.DateOnly) {
			out = append(out, k)
			if len(out) > 31 {
				return nil, actions.Userf("A range can cover at most 31 days.")
			}
		}
		return out, nil
	}
	add(s, d, &mcp.Tool{
		Name: "propose_schedule_override", Annotations: proposeOnly,
		Description: describe(`Propose special hours or a full-day closure for one date or a date range (holidays, events). Replaces both opening and ordering hours on those dates. Creates a pending change only.`,
			`propose_schedule_override({"date": "2026-12-25", "end_date": "2026-12-26", "closed": true, "note": "Noël", "request_context": "圣诞节两天休息"})`),
	}, func(ctx context.Context, in OverrideIn) (actions.Proposal, error) {
		dates, err := dateRange(in.Date, in.EndDate)
		if err != nil {
			return actions.Proposal{}, err
		}
		if !in.Closed && in.Open == "" {
			return actions.Proposal{}, actions.Userf("Give open and close times, or set closed to true.")
		}
		p := actions.ScheduleParams{Origin: actions.OriginOverride}
		var note *string
		if n := strings.TrimSpace(in.Note); n != "" {
			note = &n
		}
		for _, dt := range dates {
			p.Upserts = append(p.Upserts, actions.OverrideSpec{Date: dt, Closed: in.Closed, Schedule: in.schedule(), Note: note})
		}
		return d.propose(ctx, "propose_schedule_override", actions.KindSchedule, p, nil, in.RequestContext)
	})

	type OverrideRemoveIn struct {
		Date    string `json:"date" jsonschema:"yyyy-mm-dd"`
		EndDate string `json:"end_date,omitempty" jsonschema:"yyyy-mm-dd, to remove a range of up to 31 days"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_schedule_override_removal", Annotations: proposeOnly,
		Description: describe(`Propose removing date-specific closures or special hours so those dates use the regular weekly hours again. Creates a pending change only.`,
			`propose_schedule_override_removal({"date": "2026-12-25", "request_context": "圣诞节还是开门"})`),
	}, func(ctx context.Context, in OverrideRemoveIn) (actions.Proposal, error) {
		dates, err := dateRange(in.Date, in.EndDate)
		if err != nil {
			return actions.Proposal{}, err
		}
		return d.propose(ctx, "propose_schedule_override_removal", actions.KindSchedule, actions.ScheduleParams{Deletes: dates, Origin: actions.OriginOverride}, nil, in.RequestContext)
	})
}
