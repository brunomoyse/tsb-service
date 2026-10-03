package tools

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/search"
	"tsb-service/internal/mcp/upstream"
)

// ChangeOut is an audit log entry in tool output.
type ChangeOut struct {
	At             string `json:"at"`
	Source         string `json:"source" jsonschema:"tool or internal endpoint that made the change"`
	Kind           string `json:"kind"`
	Risk           string `json:"risk"`
	Summary        string `json:"summary"`
	Outcome        string `json:"outcome" jsonschema:"applied, rejected, failed, conflict or expired"`
	Undone         bool   `json:"undone"`
	ChangeID       string `json:"change_id,omitempty"`
	RequestContext string `json:"request_context,omitempty"`
}

// ProductRef is a short product reference.
type ProductRef struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	NameZH    string `json:"name_zh,omitempty"`
}

func productRef(p *upstream.Product) ProductRef {
	r := ProductRef{ProductID: p.ID, Name: p.NameIn("fr")}
	for _, t := range p.Translations {
		if t.Language == "zh" {
			r.NameZH = t.Name
		}
	}
	return r
}

func registerStatus(s *mcp.Server, d *Deps) {
	type Empty struct{}
	type StatusOut struct {
		Now                 string          `json:"now"`
		Open                bool            `json:"open" jsonschema:"customers can order online right now"`
		OrderingEnabled     bool            `json:"ordering_enabled" jsonschema:"false when online ordering was paused"`
		ClosedReason        string          `json:"closed_reason,omitempty" jsonschema:"ordering_paused, closed_today, special_hours or outside_opening_hours"`
		ClosedNote          string          `json:"closed_note,omitempty"`
		ReopensAt           string          `json:"reopens_at,omitempty" jsonschema:"next opening time; empty when ordering is paused until reopened manually"`
		TodayHours          DayHours        `json:"today_hours"`
		PreparationMinutes  int             `json:"preparation_minutes"`
		UnavailableProducts []ProductRef    `json:"unavailable_products" jsonschema:"visible products currently marked sold out"`
		Today               DailySummaryOut `json:"today"`
		RecentChanges       []ChangeOut     `json:"recent_changes" jsonschema:"last 10 entries of the assistant's audit log"`
	}
	add(s, d, &mcp.Tool{
		Name: "get_status", Annotations: readOnly,
		Description: describe(`Snapshot of the restaurant right now: open or closed (reason, note, planned reopening), today's hours, preparation time, sold-out products, today's order count and revenue, and the last 10 changes made through the assistant.`,
			`get_status({})`),
	}, func(ctx context.Context, _ Empty) (StatusOut, error) {
		now := d.Now()
		today := d.today()
		cfg, err := d.Up.RestaurantConfig(ctx)
		if err != nil {
			return StatusOut{}, err
		}
		out := StatusOut{Now: d.fmtTime(now), Open: cfg.IsOrderingCurrentlyOpen && cfg.OrderingEnabled, OrderingEnabled: cfg.OrderingEnabled,
			PreparationMinutes: cfg.PreparationMinutes, UnavailableProducts: []ProductRef{}, RecentChanges: []ChangeOut{}}

		week, _ := upstream.ParseWeek(cfg.OpeningHours)
		todayHours := week[strings.ToLower(now.In(d.Loc).Weekday().String())]
		f, _ := upstream.OverrideDate(today)
		ovs, err := d.Up.ScheduleOverrides(ctx, f, f.Add(23*time.Hour))
		if err != nil {
			return StatusOut{}, err
		}
		var override *upstream.ScheduleOverride
		for i := range ovs {
			if ovs[i].DateKey() == today {
				override = &ovs[i]
			}
		}
		out.TodayHours = dayHoursOut(todayHours)
		if override != nil {
			out.TodayHours = overrideOut(*override).Hours
			out.ClosedNote = valueOr(override.Note, "")
		}
		if !out.Open {
			switch {
			case !cfg.OrderingEnabled:
				out.ClosedReason = "ordering_paused"
			case override != nil && override.Closed:
				out.ClosedReason = "closed_today"
			case override != nil:
				out.ClosedReason = "special_hours"
			default:
				out.ClosedReason = "outside_opening_hours"
			}
			if cfg.OrderingEnabled {
				out.ReopensAt = d.fmtTimePtr(cfg.NextOpeningAt)
			}
		}

		products, err := d.Up.Products(ctx)
		if err != nil {
			return StatusOut{}, err
		}
		for i := range products {
			if !products[i].IsAvailable && products[i].IsVisible {
				out.UnavailableProducts = append(out.UnavailableProducts, productRef(&products[i]))
			}
		}

		if out.Today, err = d.dailySummary(ctx, today); err != nil {
			return StatusOut{}, err
		}

		entries, err := d.Svc.Store().RecentAudit(ctx, 10)
		if err != nil {
			return StatusOut{}, err
		}
		for _, e := range entries {
			out.RecentChanges = append(out.RecentChanges, ChangeOut{At: d.fmtTime(e.At), Source: e.Source, Kind: e.Kind, Risk: e.Risk, Summary: e.Summary,
				Outcome: e.Outcome, Undone: e.UndoneBy != nil, ChangeID: e.ChangeID, RequestContext: e.RequestContext})
		}
		return out, nil
	})
}

func registerUndo(s *mcp.Server, d *Deps) {
	type UndoIn struct {
		WriteContext
	}
	type UndoOut struct {
		Mode          string            `json:"mode" jsonschema:"applied (reverted now), pending (needs the owner's confirmation) or no_op"`
		Summary       string            `json:"summary"`
		UndoneSummary string            `json:"undone_summary" jsonschema:"the change being reverted"`
		UndoneAt      string            `json:"undone_change_at"`
		Proposal      *actions.Proposal `json:"proposal,omitempty" jsonschema:"set when mode is pending"`
	}
	add(s, d, &mcp.Tool{
		Name: "undo_last_change", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
		Description: describe(`Revert the most recent change made through the assistant in the last 30 minutes, using the state saved before it. A low-risk change (availability, visibility, preparation time, coupon deactivation) is reverted immediately. Reverting a sensitive change (price, hours, closure...) creates a pending change that needs confirmation like any proposal. Calling it again reverts the change before that. Deletions and photos cannot be undone.`,
			`undo_last_change({"request_context": "刚才那个撤销"})`),
	}, func(ctx context.Context, in UndoIn) (UndoOut, error) {
		r, err := d.Svc.Undo(ctx, in.RequestContext)
		if err != nil {
			return UndoOut{}, err
		}
		return UndoOut{Mode: r.Mode, Summary: r.Summary, UndoneSummary: r.Undone, UndoneAt: r.UndoneAt, Proposal: r.Proposal}, nil
	})
}

func registerCustomers(s *mcp.Server, d *Deps) {
	type StatsIn struct {
		From      string `json:"from,omitempty" jsonschema:"yyyy-mm-dd, orders from this day"`
		To        string `json:"to,omitempty" jsonschema:"yyyy-mm-dd, orders until this day (included)"`
		Query     string `json:"query,omitempty" jsonschema:"filter customers by name (partial)"`
		MinOrders int    `json:"min_orders,omitempty" jsonschema:"only customers with at least this many orders"`
		OrderType string `json:"order_type,omitempty" jsonschema:"DELIVERY or PICKUP"`
		Limit     int    `json:"limit,omitempty" jsonschema:"default 20, max 100"`
	}
	type CustomerOut struct {
		CustomerID         string `json:"customer_id"`
		FirstName          string `json:"first_name"`
		LastName           string `json:"last_name"`
		Orders             int    `json:"orders"`
		TotalCents         int64  `json:"total_cents"`
		AverageCents       int64  `json:"average_cents"`
		FirstOrder         string `json:"first_order"`
		LastOrder          string `json:"last_order"`
		PreferredOrderType string `json:"preferred_order_type"`
		DeliveryCount      int    `json:"delivery_count"`
		PickupCount        int    `json:"pickup_count"`
	}
	type StatsOut struct {
		Customers     int           `json:"customers"`
		Orders        int           `json:"orders"`
		RevenueCents  int64         `json:"revenue_cents"`
		AverageCents  int64         `json:"average_order_cents"`
		Currency      string        `json:"currency"`
		TopCustomers  []CustomerOut `json:"top_customers" jsonschema:"sorted by number of orders; no contact details"`
		TotalMatching int           `json:"total_matching"`
	}
	add(s, d, &mcp.Tool{
		Name: "get_customer_stats", Annotations: readOnly,
		Description: describe(`Customer statistics for a period: totals and the most frequent customers by name with order counts and amounts (no phone numbers or emails).`,
			`get_customer_stats({"from": "2026-09-01", "to": "2026-09-30", "limit": 10})`),
	}, func(ctx context.Context, in StatsIn) (StatsOut, error) {
		var input upstream.CustomerStatsInput
		if in.From != "" {
			t, err := d.boundary("from", in.From, false)
			if err != nil {
				return StatsOut{}, err
			}
			input.StartDate = &t
		}
		if in.To != "" {
			t, err := d.boundary("to", in.To, true)
			if err != nil {
				return StatsOut{}, err
			}
			input.EndDate = &t
		}
		if in.MinOrders > 0 {
			input.MinOrders = &in.MinOrders
		}
		if ot := strings.ToUpper(strings.TrimSpace(in.OrderType)); ot != "" {
			if ot != "DELIVERY" && ot != "PICKUP" {
				return StatsOut{}, actions.Userf("order_type must be DELIVERY or PICKUP.")
			}
			input.OrderType = &ot
		}
		st, err := d.Up.CustomerStats(ctx, input)
		if err != nil {
			return StatsOut{}, err
		}
		out := StatsOut{Customers: st.Summary.TotalCustomers, Orders: st.Summary.TotalOrders, RevenueCents: centsOf(st.Summary.TotalRevenue),
			AverageCents: centsOf(st.Summary.AverageOrderValue), Currency: "EUR", TopCustomers: []CustomerOut{}}
		list := st.Customers
		if strings.TrimSpace(in.Query) != "" {
			cands := make([]search.Candidate, len(list))
			for i, c := range list {
				cands[i] = search.Candidate{ID: c.UserID, Primary: []string{c.FirstName + " " + c.LastName, c.LastName + " " + c.FirstName}, SortKey: c.LastName}
			}
			keep := map[string]int{}
			for i, r := range search.Rank(in.Query, cands, 0) {
				keep[r.ID] = i
			}
			filtered := list[:0:0]
			for _, c := range list {
				if _, ok := keep[c.UserID]; ok {
					filtered = append(filtered, c)
				}
			}
			list = filtered
		}
		out.TotalMatching = len(list)
		for i := range list {
			if i == limitOr(in.Limit, 20, 100) {
				break
			}
			c := list[i]
			out.TopCustomers = append(out.TopCustomers, CustomerOut{CustomerID: c.UserID, FirstName: c.FirstName, LastName: c.LastName, Orders: c.TotalOrders,
				TotalCents: centsOf(c.TotalAmount), AverageCents: centsOf(c.AverageOrderAmount), FirstOrder: d.fmtTime(c.FirstOrderDate), LastOrder: d.fmtTime(c.LastOrderDate),
				PreferredOrderType: c.PreferredOrderType, DeliveryCount: c.DeliveryCount, PickupCount: c.PickupCount})
		}
		return out, nil
	})
}

func centsOf(s string) int64 { return money.MustCents(s) }
