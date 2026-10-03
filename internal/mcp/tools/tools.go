// Package tools exposes the MCP tools. Each tool maps to one real dashboard
// action (or a read); there is no generic "call any endpoint" tool and no tool
// that confirms a pending change: applying happens only through the internal
// API, called by the agent service once the owner says yes.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/upstream"
)

// Deps are the tool dependencies.
type Deps struct {
	Svc *actions.Service
	Up  *upstream.Client
	Loc *time.Location
	Log *slog.Logger
	Now func() time.Time
	// ImageClient downloads product photos. Nil uses a client that refuses
	// private and loopback addresses.
	ImageClient *http.Client
	// AllowHTTPImages accepts plain http image URLs (tests only).
	AllowHTTPImages bool
}

// Instructions are sent to the client at initialization.
const Instructions = `Tools to run the Tokyo Sushi Bar dashboard for the owner.
- Resolve names to ids first: search_products, list_categories, search_coupons, get_product (choice groups and choices).
- A product is identified by its category and name together: the same name exists in several categories (三文鱼 is a maki, a sushi, a sashimi and a poke bowl). Name products to the owner by their label (category + name). When search_products' note says the query does not name exactly one product, ask the owner which one before changing anything.
- Reads and low-risk tools act immediately. propose_* tools only create a pending change and return change_id, summary and expires_at: show the summary to the owner and ask for a yes/no. The change is applied by the agent service, never by a tool.
- Orders are read only: no tool can create, edit, cancel or refund an order.
- Customers' full last names, phone numbers and emails are never available: tools give the first name and the last-name initial (Marie D.), and mask contact details in notes. The owner finds them in the dashboard.
- Money is integer cents (price_cents) in EUR. Times are ISO 8601; without an offset they are Europe/Brussels. Dates are yyyy-mm-dd.
- Pass the owner's original message as request_context on every write.
- undo_last_change reverts the most recent change from the last 30 minutes.`

// Register adds every tool to the server.
func Register(s *mcp.Server, d *Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.ImageClient == nil {
		d.ImageClient = safeHTTPClient()
	}
	registerStatus(s, d)
	registerProducts(s, d)
	registerChoices(s, d)
	registerRestaurant(s, d)
	registerCoupons(s, d)
	registerOrders(s, d)
	registerCustomers(s, d)
	registerUndo(s, d)
}

// WriteContext is embedded in every write input.
type WriteContext struct {
	RequestContext string `json:"request_context,omitempty" jsonschema:"the owner's original message, stored in the audit log"`
}

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	lowRisk     = &mcp.ToolAnnotations{DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: new(false)}
	proposeOnly = &mcp.ToolAnnotations{DestructiveHint: new(false), IdempotentHint: false, OpenWorldHint: new(false)}
)

const genericError = "Something went wrong in the assistant. Please try again."

// safeErr converts any error into a short message fit for the owner. Details
// are logged, never returned.
func (d *Deps) safeErr(tool string, err error) error {
	if ue, ok := errors.AsType[*actions.UserError](err); ok {
		return errors.New(ue.Msg)
	}
	if ce, ok := errors.AsType[*actions.ConflictError](err); ok {
		return errors.New(ce.Error())
	}
	if up, ok := errors.AsType[*upstream.Error](err); ok {
		return errors.New(up.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &actions.UserError{Msg: "The request took too long. Please try again."}
	}
	d.Log.Error("tool failed", "tool", tool, "error", err)
	return &actions.UserError{Msg: genericError}
}

// add registers a typed tool with error sanitising.
func add[In, Out any](s *mcp.Server, d *Deps, t *mcp.Tool, fn func(ctx context.Context, in In) (Out, error)) {
	mcp.AddTool(s, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		out, err := fn(ctx, in)
		if err != nil {
			d.Log.Info("tool call", "tool", t.Name, "ok", false, "duration", time.Since(start))
			var zero Out
			return nil, zero, d.safeErr(t.Name, err)
		}
		d.Log.Info("tool call", "tool", t.Name, "ok", true, "duration", time.Since(start))
		return nil, out, nil
	})
}

// --- time helpers ---------------------------------------------------------------

// parseTime accepts RFC 3339 (with offset) or a local date-time in TZ_DEFAULT
// ("2026-10-03T18:00", "2026-10-03 18:00", seconds optional).
func (d *Deps) parseTime(field, s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, d.Loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, actions.Userf("%s: invalid time %q, use ISO 8601 such as 2026-10-03T18:00.", field, s)
}

// parseDate accepts yyyy-mm-dd.
func parseDate(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return "", actions.Userf("%s: invalid date %q, use yyyy-mm-dd.", field, s)
	}
	return s, nil
}

// dayBounds returns [start of date, start of next day) in loc.
func (d *Deps) dayBounds(date string) (time.Time, time.Time) {
	t, _ := time.ParseInLocation(time.DateOnly, date, d.Loc)
	return t, t.AddDate(0, 0, 1)
}

func (d *Deps) fmtTime(t time.Time) string { return t.In(d.Loc).Format(time.RFC3339) }

func (d *Deps) fmtTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return d.fmtTime(*t)
}

func (d *Deps) today() string { return d.Now().In(d.Loc).Format(time.DateOnly) }

// toAny turns stored JSON into a schema-free value for structured output.
func toAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func describe(text, example string) string {
	return text + "\n\nExample: " + example
}

func valueOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func limitOr(n, def, max int) int {
	if n <= 0 {
		return def
	}
	return min(n, max)
}
