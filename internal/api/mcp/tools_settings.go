package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
)

type dayScheduleIn struct {
	Open        string  `json:"open" jsonschema:"lunch open time in HH:MM (24h, Europe/Brussels)"`
	Close       string  `json:"close" jsonschema:"lunch close time in HH:MM"`
	DinnerOpen  *string `json:"dinnerOpen,omitempty"`
	DinnerClose *string `json:"dinnerClose,omitempty"`
}

type openingHoursIn struct {
	Monday    *dayScheduleIn `json:"monday,omitempty" jsonschema:"omit or set null to mark the day as closed"`
	Tuesday   *dayScheduleIn `json:"tuesday,omitempty"`
	Wednesday *dayScheduleIn `json:"wednesday,omitempty"`
	Thursday  *dayScheduleIn `json:"thursday,omitempty"`
	Friday    *dayScheduleIn `json:"friday,omitempty"`
	Saturday  *dayScheduleIn `json:"saturday,omitempty"`
	Sunday    *dayScheduleIn `json:"sunday,omitempty"`
}

type setOrderingIn struct {
	Enabled bool `json:"enabled" jsonschema:"true to accept new orders, false to pause"`
}

type setPreparationIn struct {
	Minutes int `json:"minutes" jsonschema:"default order preparation time; must be between 1 and 240"`
}

type setHoursIn struct {
	Hours openingHoursIn `json:"hours" jsonschema:"weekly grid; any day omitted/null is considered closed"`
}

type listOverridesIn struct {
	From string `json:"from" jsonschema:"inclusive start date in YYYY-MM-DD"`
	To   string `json:"to" jsonschema:"inclusive end date in YYYY-MM-DD"`
}

type listOverridesOut struct {
	Overrides []scheduleOverrideOut `json:"overrides"`
}

type upsertOverrideIn struct {
	Date     string         `json:"date" jsonschema:"the date to override in YYYY-MM-DD"`
	Closed   bool           `json:"closed" jsonschema:"true marks the day fully closed regardless of weekly schedule"`
	Schedule *dayScheduleIn `json:"schedule,omitempty" jsonschema:"required when closed=false"`
	Note     *string        `json:"note,omitempty" jsonschema:"free-form admin note (e.g. holiday name)"`
}

type deleteOverrideIn struct {
	Date string `json:"date" jsonschema:"date in YYYY-MM-DD"`
}

type configOut = restaurantConfigOut

func registerSettingsTools(s *mcpsdk.Server, deps Deps) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_restaurant_config",
			Description: "Get the current restaurant configuration: ordering on/off, weekly opening hours, ordering hours, default preparation time.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, configOut, error) {
			cfg, err := deps.Restaurant.GetConfig(ctx)
			if err != nil {
				return errorResult(fmt.Sprintf("get config: %v", err)), configOut{}, nil
			}
			return nil, toRestaurantConfigOut(cfg), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "toggle_ordering",
			Description: "Globally enable or pause online ordering. Use this to stop accepting orders during a rush, equipment failure, or closure.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args setOrderingIn) (*mcpsdk.CallToolResult, configOut, error) {
			cfg, err := deps.Restaurant.UpdateOrderingEnabled(ctx, args.Enabled)
			if err != nil {
				return errorResult(fmt.Sprintf("toggle ordering: %v", err)), configOut{}, nil
			}
			return nil, toRestaurantConfigOut(cfg), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "set_preparation_minutes",
			Description: "Set the default order preparation time in minutes. Valid range: 1-240.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args setPreparationIn) (*mcpsdk.CallToolResult, configOut, error) {
			if args.Minutes < 1 || args.Minutes > 240 {
				return errorResult("minutes must be between 1 and 240"), configOut{}, nil
			}
			cfg, err := deps.Restaurant.UpdatePreparationMinutes(ctx, args.Minutes)
			if err != nil {
				return errorResult(fmt.Sprintf("set preparation: %v", err)), configOut{}, nil
			}
			return nil, toRestaurantConfigOut(cfg), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "set_opening_hours",
			Description: "Replace the weekly opening hours grid (dine-in). Any day omitted or set to null becomes closed.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args setHoursIn) (*mcpsdk.CallToolResult, configOut, error) {
			raw, err := marshalHours(args.Hours)
			if err != nil {
				return errorResult(err.Error()), configOut{}, nil
			}
			cfg, err := deps.Restaurant.UpdateOpeningHours(ctx, raw)
			if err != nil {
				return errorResult(fmt.Sprintf("update opening hours: %v", err)), configOut{}, nil
			}
			return nil, toRestaurantConfigOut(cfg), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "set_ordering_hours",
			Description: "Replace the weekly ordering hours (when customers can place online orders). Independent from dine-in opening hours.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args setHoursIn) (*mcpsdk.CallToolResult, configOut, error) {
			raw, err := marshalHours(args.Hours)
			if err != nil {
				return errorResult(err.Error()), configOut{}, nil
			}
			cfg, err := deps.Restaurant.UpdateOrderingHours(ctx, raw)
			if err != nil {
				return errorResult(fmt.Sprintf("update ordering hours: %v", err)), configOut{}, nil
			}
			return nil, toRestaurantConfigOut(cfg), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_schedule_overrides",
			Description: "List date-specific schedule overrides (closures, special hours) within an inclusive date range.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args listOverridesIn) (*mcpsdk.CallToolResult, listOverridesOut, error) {
			from, err := parseDate(args.From)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid from date: %v", err)), listOverridesOut{}, nil
			}
			to, err := parseDate(args.To)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid to date: %v", err)), listOverridesOut{}, nil
			}
			overrides, err := deps.Restaurant.ListOverrides(ctx, from, to)
			if err != nil {
				return errorResult(fmt.Sprintf("list overrides: %v", err)), listOverridesOut{}, nil
			}
			out := listOverridesOut{Overrides: make([]scheduleOverrideOut, len(overrides))}
			for i, ov := range overrides {
				out.Overrides[i] = toScheduleOverrideOut(ov)
			}
			return nil, out, nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "upsert_schedule_override",
			Description: "Create or update a date-specific override. Set closed=true for a full-day closure (e.g. holiday), or provide a schedule for special hours.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args upsertOverrideIn) (*mcpsdk.CallToolResult, scheduleOverrideOut, error) {
			date, err := parseDate(args.Date)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid date: %v", err)), scheduleOverrideOut{}, nil
			}
			if !args.Closed && args.Schedule == nil {
				return errorResult("schedule is required when closed=false"), scheduleOverrideOut{}, nil
			}
			var scheduleJSON json.RawMessage
			if !args.Closed && args.Schedule != nil {
				ds := toDayScheduleDomain(args.Schedule)
				b, err := json.Marshal(ds)
				if err != nil {
					return errorResult(fmt.Sprintf("marshal schedule: %v", err)), scheduleOverrideOut{}, nil
				}
				scheduleJSON = b
			}
			ov, err := deps.Restaurant.UpsertOverride(ctx, date, args.Closed, scheduleJSON, args.Note)
			if err != nil {
				return errorResult(fmt.Sprintf("upsert override: %v", err)), scheduleOverrideOut{}, nil
			}
			return nil, toScheduleOverrideOut(ov), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "delete_schedule_override",
			Description: "Remove a date-specific override. The weekly schedule resumes for that date.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args deleteOverrideIn) (*mcpsdk.CallToolResult, struct{ Deleted bool `json:"deleted"` }, error) {
			date, err := parseDate(args.Date)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid date: %v", err)), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			if err := deps.Restaurant.DeleteOverride(ctx, date); err != nil {
				return errorResult(fmt.Sprintf("delete override: %v", err)), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			return nil, struct{ Deleted bool `json:"deleted"` }{Deleted: true}, nil
		},
	)
}

func marshalHours(h openingHoursIn) (json.RawMessage, error) {
	hours := restaurantDomain.OpeningHours{
		"monday":    toDayScheduleDomain(h.Monday),
		"tuesday":   toDayScheduleDomain(h.Tuesday),
		"wednesday": toDayScheduleDomain(h.Wednesday),
		"thursday":  toDayScheduleDomain(h.Thursday),
		"friday":    toDayScheduleDomain(h.Friday),
		"saturday":  toDayScheduleDomain(h.Saturday),
		"sunday":    toDayScheduleDomain(h.Sunday),
	}
	b, err := json.Marshal(hours)
	if err != nil {
		return nil, fmt.Errorf("marshal hours: %w", err)
	}
	return b, nil
}

func toDayScheduleDomain(d *dayScheduleIn) *restaurantDomain.DaySchedule {
	if d == nil {
		return nil
	}
	ds := &restaurantDomain.DaySchedule{Open: d.Open, Close: d.Close}
	if d.DinnerOpen != nil {
		ds.DinnerOpen = *d.DinnerOpen
	}
	if d.DinnerClose != nil {
		ds.DinnerClose = *d.DinnerClose
	}
	return ds
}

func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}
