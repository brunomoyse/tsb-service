package tools

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/money"
	"tsb-service/internal/mcp/privacy"
	"tsb-service/internal/mcp/upstream"
)

// OrderStatuses are the upstream order statuses.
var OrderStatuses = []string{"PENDING", "CONFIRMED", "PREPARING", "AWAITING_PICK_UP", "PICKED_UP", "OUT_FOR_DELIVERY", "DELIVERED", "CANCELLED", "FAILED"}

// OrderSummaryOut is an order in a list.
type OrderSummaryOut struct {
	OrderID       string `json:"order_id"`
	CreatedAt     string `json:"created_at"`
	Status        string `json:"status"`
	Type          string `json:"type" jsonschema:"DELIVERY or PICKUP"`
	TotalCents    int64  `json:"total_cents"`
	Currency      string `json:"currency"`
	Customer      string `json:"customer" jsonschema:"the customer's first name and last-name initial (Marie D.), or guest. Full last names, phone numbers and emails are only in the dashboard"`
	ItemCount     int    `json:"item_count"`
	OnlinePayment bool   `json:"online_payment"`
	PaymentStatus string `json:"payment_status,omitempty"`
	ReadyTime     string `json:"ready_time,omitempty" jsonschema:"time requested by the customer"`
}

func (d *Deps) orderSummary(o *upstream.Order) OrderSummaryOut {
	out := OrderSummaryOut{OrderID: o.ID, CreatedAt: d.fmtTime(o.CreatedAt), Status: o.Status, Type: o.Type, TotalCents: money.MustCents(o.TotalPrice),
		Currency: money.Currency, Customer: customerName(o), OnlinePayment: o.IsOnlinePayment, ReadyTime: d.fmtTimePtr(o.PreferredReadyTime)}
	for _, it := range o.Items {
		out.ItemCount += it.Quantity
	}
	if o.Payment != nil {
		out.PaymentStatus = o.Payment.Status
	}
	return out
}

// DailySummaryOut is the order summary for one day.
type DailySummaryOut struct {
	Date         string         `json:"date"`
	Orders       int            `json:"orders" jsonschema:"orders counted: not pending, not cancelled, not failed"`
	RevenueCents int64          `json:"revenue_cents"`
	AverageCents int64          `json:"average_cents"`
	Currency     string         `json:"currency"`
	ByStatus     map[string]int `json:"by_status"`
	ByType       map[string]int `json:"by_type"`
	Pending      int            `json:"pending" jsonschema:"orders still waiting for confirmation, not counted in revenue"`
	Cancelled    int            `json:"cancelled" jsonschema:"cancelled or failed orders among the latest 200, not counted"`
}

// dailySummary sums one Brussels day from orderHistory (which already excludes
// cancelled and failed orders), leaving out PENDING orders.
func (d *Deps) dailySummary(ctx context.Context, date string) (DailySummaryOut, error) {
	start, end := d.dayBounds(date)
	endIncl := end.Add(-time.Millisecond)
	out := DailySummaryOut{Date: date, Currency: money.Currency, ByStatus: map[string]int{}, ByType: map[string]int{}}
	for page := 1; page <= 20; page++ {
		h, err := d.Up.OrderHistory(ctx, upstream.OrderHistoryInput{StartDate: &start, EndDate: &endIncl, First: 100, Page: page})
		if err != nil {
			return DailySummaryOut{}, err
		}
		for i := range h.Orders {
			o := &h.Orders[i]
			if o.Status == "PENDING" {
				out.Pending++
				continue
			}
			out.Orders++
			out.RevenueCents += money.MustCents(o.TotalPrice)
			out.ByStatus[o.Status]++
			out.ByType[o.Type]++
		}
		if len(h.Orders) < 100 {
			break
		}
	}
	if out.Orders > 0 {
		out.AverageCents = out.RevenueCents / int64(out.Orders)
	}
	if recent, err := d.Up.Orders(ctx); err == nil {
		for i := range recent {
			o := &recent[i]
			if (o.Status == "CANCELLED" || o.Status == "FAILED") && !o.CreatedAt.Before(start) && o.CreatedAt.Before(end) {
				out.Cancelled++
			}
		}
	}
	return out, nil
}

func registerOrders(s *mcp.Server, d *Deps) {
	type ListIn struct {
		From   string `json:"from,omitempty" jsonschema:"yyyy-mm-dd or ISO 8601 start (inclusive)"`
		To     string `json:"to,omitempty" jsonschema:"yyyy-mm-dd (whole day included) or ISO 8601 end"`
		Status string `json:"status,omitempty" jsonschema:"PENDING, CONFIRMED, PREPARING, AWAITING_PICK_UP, PICKED_UP, OUT_FOR_DELIVERY, DELIVERED, CANCELLED or FAILED"`
		Limit  int    `json:"limit,omitempty" jsonschema:"default 20, max 100"`
	}
	type ListOut struct {
		Orders []OrderSummaryOut `json:"orders"`
		Source string            `json:"source" jsonschema:"history (all orders in the period, cancelled/failed excluded) or recent (latest 200 orders)"`
		Note   string            `json:"note,omitempty"`
	}
	add(s, d, &mcp.Tool{
		Name: "list_orders", Annotations: readOnly,
		Description: describe(`List orders, newest first (read only). With from/to: all orders in the period except cancelled and failed. Without dates, or for status CANCELLED/FAILED: the latest 200 orders. No tool can change an order.`,
			`list_orders({"from": "2026-10-03", "to": "2026-10-03", "status": "DELIVERED", "limit": 20})`),
	}, func(ctx context.Context, in ListIn) (ListOut, error) {
		limit := limitOr(in.Limit, 20, 100)
		status := strings.ToUpper(strings.TrimSpace(in.Status))
		if status != "" && !slices.Contains(OrderStatuses, status) {
			return ListOut{}, actions.Userf("Unknown status %q. Use one of: %s.", in.Status, strings.Join(OrderStatuses, ", "))
		}
		var from, to *time.Time
		if in.From != "" {
			t, err := d.boundary("from", in.From, false)
			if err != nil {
				return ListOut{}, err
			}
			from = &t
		}
		if in.To != "" {
			t, err := d.boundary("to", in.To, true)
			if err != nil {
				return ListOut{}, err
			}
			to = &t
		}
		out := ListOut{Orders: []OrderSummaryOut{}}
		if (from != nil || to != nil) && status != "CANCELLED" && status != "FAILED" {
			in := upstream.OrderHistoryInput{StartDate: from, EndDate: to, First: limit, Page: 1}
			if status != "" {
				in.Status = &status
			}
			h, err := d.Up.OrderHistory(ctx, in)
			if err != nil {
				return ListOut{}, err
			}
			for i := range h.Orders {
				out.Orders = append(out.Orders, d.orderSummary(&h.Orders[i]))
			}
			out.Source = "history"
			if h.Summary.TotalOrders > len(out.Orders) {
				out.Note = "showing the newest " + itoa(len(out.Orders)) + " of " + itoa(h.Summary.TotalOrders) + " orders"
			}
			return out, nil
		}
		recent, err := d.Up.Orders(ctx)
		if err != nil {
			return ListOut{}, err
		}
		slices.SortFunc(recent, func(a, b upstream.Order) int { return b.CreatedAt.Compare(a.CreatedAt) })
		for i := range recent {
			o := &recent[i]
			if status != "" && o.Status != status {
				continue
			}
			if from != nil && o.CreatedAt.Before(*from) || to != nil && o.CreatedAt.After(*to) {
				continue
			}
			if len(out.Orders) == limit {
				break
			}
			out.Orders = append(out.Orders, d.orderSummary(o))
		}
		out.Source, out.Note = "recent", "searched the latest 200 orders only"
		return out, nil
	})

	type GetIn struct {
		OrderID string `json:"order_id"`
	}
	type ItemOut struct {
		Product string `json:"product" jsonschema:"French product name"`
		// ProductLabels name the product as category + name, like
		// ProductOut.Labels.
		ProductLabels map[string]string `json:"product_labels,omitempty" jsonschema:"category + name per language; use it to name the product"`
		Quantity      int               `json:"quantity"`
		UnitCents     int64             `json:"unit_cents"`
		TotalCents    int64             `json:"total_cents"`
		Options       []string          `json:"options"`
	}
	type HistoryOut struct {
		Status string `json:"status"`
		At     string `json:"at"`
	}
	type GetOut struct {
		OrderSummaryOut
		DiscountCents      int64        `json:"discount_cents"`
		DeliveryFeeCents   int64        `json:"delivery_fee_cents"`
		CouponCode         string       `json:"coupon_code,omitempty"`
		EstimatedReadyTime string       `json:"estimated_ready_time,omitempty"`
		Address            string       `json:"address,omitempty"`
		AddressExtra       string       `json:"address_extra,omitempty"`
		Note               string       `json:"note,omitempty"`
		CancellationReason string       `json:"cancellation_reason,omitempty"`
		Items              []ItemOut    `json:"items"`
		StatusHistory      []HistoryOut `json:"status_history"`
	}
	add(s, d, &mcp.Tool{
		Name: "get_order", Annotations: readOnly,
		Description: describe(`Full details of one order (read only): items with options, totals, the customer's first name and last-name initial, address, note, payment and status history. Customers' full last names, phone numbers and emails are never available here: the owner finds them in the dashboard.`,
			`get_order({"order_id": "9a8b7c6d-5e4f-3a2b-1c0d-ef9876543210"})`),
	}, func(ctx context.Context, in GetIn) (GetOut, error) {
		o, err := d.Up.Order(ctx, in.OrderID)
		if upstream.IsNotFound(err) {
			return GetOut{}, actions.Userf("No order with id %q. Use list_orders to find it.", in.OrderID)
		}
		if err != nil {
			return GetOut{}, err
		}
		out := GetOut{OrderSummaryOut: d.orderSummary(o), DiscountCents: money.MustCents(o.DiscountAmount), EstimatedReadyTime: d.fmtTimePtr(o.EstimatedReadyTime),
			Address: o.DisplayAddress, Items: []ItemOut{}, StatusHistory: []HistoryOut{}}
		if o.DeliveryFee != nil {
			out.DeliveryFeeCents = money.MustCents(*o.DeliveryFee)
		}
		out.CouponCode = valueOr(o.CouponCode, "")
		// Free text from customers can contain a phone number or email.
		out.AddressExtra = privacy.Scrub(valueOr(o.AddressExtra, ""))
		out.Note = privacy.Scrub(valueOr(o.OrderNote, ""))
		out.CancellationReason = privacy.Scrub(valueOr(o.CancellationReason, ""))
		for _, it := range o.Items {
			io := ItemOut{Quantity: it.Quantity, UnitCents: money.MustCents(it.UnitPrice), TotalCents: money.MustCents(it.TotalPrice), Options: []string{}}
			if it.Product != nil {
				io.Product = it.Product.Name
				io.ProductLabels = productOut(&upstream.Product{ID: it.Product.ID, Name: it.Product.Name, Category: it.Product.Category, Translations: it.Product.Translations, Price: "0"}).Labels
			}
			if it.Choice != nil {
				io.Options = append(io.Options, it.Choice.Name)
			}
			for _, sel := range it.Selections {
				opt := sel.Choice.Name
				if sel.Quantity > 1 {
					opt += " x" + itoa(sel.Quantity)
				}
				io.Options = append(io.Options, opt)
			}
			out.Items = append(out.Items, io)
		}
		for _, h := range o.StatusHistory {
			out.StatusHistory = append(out.StatusHistory, HistoryOut{Status: h.Status, At: d.fmtTime(h.ChangedAt)})
		}
		return out, nil
	})

	type SummaryIn struct {
		Date string `json:"date,omitempty" jsonschema:"yyyy-mm-dd, default today"`
	}
	add(s, d, &mcp.Tool{
		Name: "get_daily_summary", Annotations: readOnly,
		Description: describe(`Order count and revenue for one day (Europe/Brussels). Pending, cancelled and failed orders are not counted in revenue; pending and cancelled counts are reported separately.`,
			`get_daily_summary({"date": "2026-10-03"})`),
	}, func(ctx context.Context, in SummaryIn) (DailySummaryOut, error) {
		date := d.today()
		if in.Date != "" {
			var err error
			if date, err = parseDate("date", in.Date); err != nil {
				return DailySummaryOut{}, err
			}
		}
		return d.dailySummary(ctx, date)
	})
}

// boundary parses a list_orders bound: a date (start of day, or end of day
// when end is true) or a full timestamp.
func (d *Deps) boundary(field, s string, end bool) (time.Time, error) {
	if date, err := parseDate(field, s); err == nil {
		start, next := d.dayBounds(date)
		if end {
			return next.Add(-time.Millisecond), nil
		}
		return start, nil
	}
	return d.parseTime(field, s)
}

func itoa(n int) string { return strconv.Itoa(n) }

// customerName is the customer's first name and last-name initial, or
// "guest".
func customerName(o *upstream.Order) string {
	if o.Customer != nil {
		if n := upstream.Name(o.Customer.FirstName, o.Customer.LastInitial); n != "" {
			return n
		}
	}
	return "guest"
}
