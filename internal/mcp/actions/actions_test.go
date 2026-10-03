package actions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/fakeupstream"
	"tsb-service/internal/mcp/upstream"
)

var brussels, _ = time.LoadLocation("Europe/Brussels")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	svc   *Service
	fake  *fakeupstream.Server
	clock *clock
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := fakeupstream.New()
	t.Cleanup(fake.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sa := upstream.ServiceAccount{Issuer: fake.URL, ClientID: "mcp-client", ClientSecret: "mcp-secret", ProjectID: "1"}
	up := upstream.New(fake.URL, sa.TokenSource(context.Background()), log)
	store, err := changes.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Saturday 3 October 2026, 13:00 in Brussels.
	clk := &clock{t: time.Date(2026, 10, 3, 13, 0, 0, 0, brussels)}
	env := &Env{Up: up, Loc: brussels, PriceMaxPct: 50, Now: clk.Now}
	return &fixture{svc: NewService(env, store, 10*time.Minute, log), fake: fake, clock: clk, ctx: context.Background()}
}

func TestPriceBoundsViaPropose(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name    string
		cents   int64
		wantErr string
	}{
		{"within bounds", 500, ""},
		{"+50% exactly", 675, ""},
		{"too high", 676, "between 2.25 EUR and 6.75 EUR"},
		{"too low", 224, "between 2.25 EUR and 6.75 EUR"},
		{"zero", 0, "greater than 0"},
		{"negative", -50, "greater than 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: tt.cents}, nil, "", nil)
			if tt.wantErr == "" {
				if err != nil || p.ChangeID == "" {
					t.Fatalf("want proposal, got %+v %v", p, err)
				}
				if !strings.Contains(p.Summary, "4.50 EUR ->") {
					t.Errorf("summary %q", p.Summary)
				}
				return
			}
			var ue *UserError
			if !errors.As(err, &ue) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want user error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
	if got := f.fake.ProductByID("p-maki-saumon").Price; got != "4.5" {
		t.Errorf("propose must not touch upstream, price is %s", got)
	}
}

func TestApplyPendingLifecycle(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "maki 5 euros", nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "oui")
	if err != nil || c.Status != changes.StatusApplied {
		t.Fatalf("apply: %+v %v", c, err)
	}
	if got := f.fake.ProductByID("p-maki-saumon").Price; got != "5" {
		t.Errorf("price = %s, want 5", got)
	}
	_, err = f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != changes.StatusApplied || !errors.Is(err, ErrNotPending) {
		t.Fatalf("second apply: %v", err)
	}
	if _, err := f.svc.Reject(f.ctx, p.ChangeID, ""); !errors.Is(err, ErrNotPending) {
		t.Errorf("reject after apply: %v", err)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 5)
	if len(audit) != 1 || audit[0].Outcome != changes.OutcomeApplied || audit[0].RequestContext != "oui" || string(audit[0].Before) != `{"price_cents":450}` {
		t.Errorf("audit: %+v", audit)
	}
}

func TestExpiry(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(10*time.Minute + time.Second)
	_, err = f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want expired, got %v", err)
	}
	c, _ := f.svc.GetChange(f.ctx, p.ChangeID)
	if c.Status != changes.StatusExpired {
		t.Errorf("status %s", c.Status)
	}
	if got := f.fake.ProductByID("p-maki-saumon").Price; got != "4.5" {
		t.Errorf("expired change must not apply, price %s", got)
	}
}

func TestConflictDetection(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Edited in the dashboard meanwhile.
	f.fake.Lock()
	f.fake.Products[0].Price = "4.8"
	f.fake.Unlock()

	_, err = f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	var ce *ConflictError
	if !errors.As(err, &ce) || !errors.Is(err, ErrConflict) || ce.Fields[0] != "price_cents" {
		t.Fatalf("want conflict on price_cents, got %v", err)
	}
	if got := f.fake.ProductByID("p-maki-saumon").Price; got != "4.8" {
		t.Errorf("conflicting change must not apply, price %s", got)
	}
	c, _ := f.svc.GetChange(f.ctx, p.ChangeID)
	if c.Status != changes.StatusConflict {
		t.Errorf("status %s", c.Status)
	}
}

func TestLowRiskIdempotentAndUndo(t *testing.T) {
	f := newFixture(t)
	params := ProductToggleParams{ProductID: "p-maki-saumon", Value: false}
	r, err := f.svc.ApplyNow(f.ctx, "set_product_availability", KindProductAvailability, params, "plus de saumon", nil)
	if err != nil || !r.Applied || r.NoOp {
		t.Fatalf("first call: %+v %v", r, err)
	}
	r, err = f.svc.ApplyNow(f.ctx, "set_product_availability", KindProductAvailability, params, "", nil)
	if err != nil || !r.NoOp || !strings.Contains(r.Summary, "already unavailable") {
		t.Fatalf("repeat call must be a no-op: %+v %v", r, err)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 10)
	if len(audit) != 1 {
		t.Errorf("no-op must not be audited, got %d entries", len(audit))
	}

	u, err := f.svc.Undo(f.ctx, "annule")
	if err != nil || u.Mode != "applied" {
		t.Fatalf("undo: %+v %v", u, err)
	}
	if !f.fake.ProductByID("p-maki-saumon").IsAvailable {
		t.Error("undo did not restore availability")
	}
	if _, err := f.svc.Undo(f.ctx, ""); err == nil || !strings.Contains(err.Error(), "no change") {
		t.Errorf("second undo should find nothing, got %v", err)
	}
}

func TestUndoWindow(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ApplyNow(f.ctx, "set_preparation_minutes", KindPreparationMinutes, PreparationParams{Minutes: 45}, "", nil); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(31 * time.Minute)
	if _, err := f.svc.Undo(f.ctx, ""); err == nil {
		t.Fatal("undo older than 30 minutes must be refused")
	}
}

func TestUndoSensitiveCreatesPending(t *testing.T) {
	f := newFixture(t)
	p, _ := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 675}, nil, "", nil)
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "pending" || u.Proposal == nil {
		t.Fatalf("undo of a sensitive change must be pending: %+v %v", u, err)
	}
	if f.fake.ProductByID("p-maki-saumon").Price != "6.75" {
		t.Error("pending undo must not apply yet")
	}
	again, _ := f.svc.Undo(f.ctx, "")
	if again.Proposal.ChangeID != u.Proposal.ChangeID {
		t.Error("a second undo must return the same pending change")
	}
	if _, err := f.svc.ApplyPending(f.ctx, u.Proposal.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.ProductByID("p-maki-saumon").Price; got != "4.5" {
		t.Errorf("price after undo = %s", got)
	}
	if _, err := f.svc.Undo(f.ctx, ""); err == nil {
		t.Error("after undoing, nothing else should be undoable")
	}
}

func TestReject(t *testing.T) {
	f := newFixture(t)
	p, _ := f.svc.Propose(f.ctx, "propose_coupon_activation", KindCouponActivate, CouponRef{CouponID: "cp-old"}, nil, "", nil)
	c, err := f.svc.Reject(f.ctx, p.ChangeID, "non")
	if err != nil || c.Status != changes.StatusRejected {
		t.Fatalf("reject: %+v %v", c, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); !errors.Is(err, ErrNotPending) {
		t.Errorf("apply after reject: %v", err)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 5)
	if audit[0].Outcome != changes.OutcomeRejected || audit[0].RequestContext != "non" {
		t.Errorf("audit %+v", audit[0])
	}
}

func TestBulkAvailability(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Propose(f.ctx, "propose_bulk_availability", KindProductAvailabilityBulk, BulkAvailabilityParams{Items: []BulkAvailabilityItem{
		{ProductID: "p-maki-saumon", Available: false}, {ProductID: "p-sashimi-saumon", Available: false}, {ProductID: "p-creme", Available: false},
	}}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Summary, "2 products") || !strings.Contains(p.Summary, "already in that state") {
		t.Errorf("summary %q", p.Summary)
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if f.fake.ProductByID("p-sashimi-saumon").IsAvailable || f.fake.ProductByID("p-maki-saumon").IsAvailable {
		t.Error("bulk change not applied")
	}
}

func hm(h, m int) time.Time { return time.Date(2026, 10, 3, h, m, 0, 0, brussels) }

func TestPlanClosure(t *testing.T) {
	week := upstream.Week{
		"saturday": {Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:30"},
		"sunday":   {Open: "17:30", Close: "22:00"},
		"tuesday":  {Open: "11:30", Close: "22:00"},
	}
	now := hm(13, 0)
	reason := "staff shortage"
	tests := []struct {
		name      string
		from      time.Time
		reopen    time.Time
		overrides map[string]*upstream.ScheduleOverride
		want      []OverrideSpec
		wantErr   string
	}{
		{
			name: "close now, back for dinner", from: now, reopen: hm(18, 0),
			want: []OverrideSpec{{Date: "2026-10-03", Schedule: &upstream.DaySchedule{Open: "11:30", Close: "13:00", DinnerOpen: "18:00", DinnerClose: "22:30"}, Note: &reason}},
		},
		{
			name: "close at 15:00 back at 18:00 touches nothing", from: hm(15, 0), reopen: hm(18, 0),
			wantErr: "not scheduled to be open",
		},
		{
			name: "close at 20:00 for the evening only", from: hm(20, 0), reopen: hm(23, 59),
			want: []OverrideSpec{{Date: "2026-10-03", Schedule: &upstream.DaySchedule{Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "20:00"}, Note: &reason}},
		},
		{
			name: "closed until monday covers saturday and sunday", from: now, reopen: time.Date(2026, 10, 5, 12, 0, 0, 0, brussels),
			want: []OverrideSpec{
				{Date: "2026-10-03", Schedule: &upstream.DaySchedule{Open: "11:30", Close: "13:00"}, Note: &reason},
				{Date: "2026-10-04", Closed: true, Note: &reason},
			},
		},
		{
			name: "reopen mid-block on tuesday keeps the rest", from: time.Date(2026, 10, 6, 0, 0, 0, 0, brussels), reopen: time.Date(2026, 10, 6, 15, 0, 0, 0, brussels),
			want: []OverrideSpec{{Date: "2026-10-06", Schedule: &upstream.DaySchedule{Open: "15:00", Close: "22:00"}, Note: &reason}},
		},
		{
			name: "existing special hours are the base", from: now, reopen: hm(19, 0),
			overrides: map[string]*upstream.ScheduleOverride{"2026-10-03": {Schedule: &upstream.DaySchedule{Open: "12:00", Close: "21:00"}}},
			want:      []OverrideSpec{{Date: "2026-10-03", Schedule: &upstream.DaySchedule{Open: "12:00", Close: "13:00", DinnerOpen: "19:00", DinnerClose: "21:00"}, Note: &reason}},
		},
		{
			name: "split into three periods is refused", from: hm(19, 0), reopen: hm(19, 30),
			wantErr: "more than two opening periods",
		},
		{name: "reopen in the past", from: now, reopen: hm(12, 0), wantErr: "must be in the future"},
		{name: "reopen beyond 7 days", from: now, reopen: now.Add(8 * 24 * time.Hour), wantErr: "within 7 days"},
		{name: "from after reopen", from: hm(19, 0), reopen: hm(18, 0), wantErr: "before the reopening"},
		{name: "from in the past", from: hm(10, 0), reopen: hm(18, 0), wantErr: "in the past"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ov := tt.overrides
			if ov == nil {
				ov = map[string]*upstream.ScheduleOverride{}
			}
			got, err := PlanClosure(now, brussels, week, ov, tt.from, tt.reopen, &reason)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error %q, got %v (%+v)", tt.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Upserts) != len(tt.want) {
				t.Fatalf("got %d overrides %+v, want %d", len(got.Upserts), got.Upserts, len(tt.want))
			}
			for i, w := range tt.want {
				g := got.Upserts[i]
				if g.Date != w.Date || g.Closed != w.Closed || !sameDay(g.Schedule, w.Schedule) || deref(g.Note) != deref(w.Note) {
					t.Errorf("override %d = %+v %+v, want %+v %+v", i, g, g.Schedule, w, w.Schedule)
				}
			}
		})
	}
}

func TestClosureAndReopeningRoundTrip(t *testing.T) {
	f := newFixture(t)
	reason := "family event"
	params, err := PlanClosure(f.clock.Now(), brussels, f.fake.Opening, map[string]*upstream.ScheduleOverride{}, f.clock.Now(), time.Date(2026, 10, 4, 23, 0, 0, 0, brussels), &reason)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.Propose(f.ctx, "propose_restaurant_closure", KindSchedule, params, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.Overrides) != 2 || !f.fake.Overrides["2026-10-04"].Closed {
		t.Fatalf("overrides after closure: %+v", f.fake.Overrides)
	}

	reopen, notes, err := f.svc.PlanReopening(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopen.Deletes) != 2 || len(notes) != 0 || reopen.OrderingEnabled != nil {
		t.Fatalf("reopening plan: %+v %v", reopen, notes)
	}
	p2, err := f.svc.Propose(f.ctx, "propose_restaurant_reopening", KindSchedule, reopen, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, p2.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.Overrides) != 0 {
		t.Errorf("reopening must remove the closure overrides: %+v", f.fake.Overrides)
	}
	if _, _, err := f.svc.PlanReopening(f.ctx); err == nil {
		t.Error("nothing left to reopen")
	}
}

func TestReopeningLeavesDashboardEdits(t *testing.T) {
	f := newFixture(t)
	params, _ := PlanClosure(f.clock.Now(), brussels, f.fake.Opening, map[string]*upstream.ScheduleOverride{}, f.clock.Now(), time.Date(2026, 10, 3, 18, 0, 0, 0, brussels), nil)
	p, _ := f.svc.Propose(f.ctx, "propose_restaurant_closure", KindSchedule, params, nil, "", nil)
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	f.fake.Lock()
	f.fake.Overrides["2026-10-03"].Closed = true
	f.fake.Overrides["2026-10-03"].Schedule = nil
	f.fake.Config.OrderingEnabled = false
	f.fake.Unlock()

	reopen, notes, err := f.svc.PlanReopening(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopen.Deletes) != 0 || reopen.OrderingEnabled == nil || len(notes) == 0 {
		t.Fatalf("plan %+v notes %v", reopen, notes)
	}
}

func TestPauseOrdering(t *testing.T) {
	f := newFixture(t)
	off := false
	p, err := f.svc.Propose(f.ctx, "propose_restaurant_closure", KindSchedule, ScheduleParams{OrderingEnabled: &off, Origin: OriginClosure}, nil, "", nil)
	if err != nil || !strings.Contains(p.Summary, "online ordering: on -> off") {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	if f.fake.Config.OrderingEnabled {
		t.Error("ordering still enabled")
	}
	reopen, _, err := f.svc.PlanReopening(f.ctx)
	if err != nil || reopen.OrderingEnabled == nil || !*reopen.OrderingEnabled {
		t.Fatalf("reopen plan %+v %v", reopen, err)
	}
}

func TestOrderingIsNotAKind(t *testing.T) {
	for _, h := range registry() {
		k := strings.ToLower(h.kind())
		if strings.HasPrefix(k, "order.") || strings.Contains(k, "payment") {
			t.Errorf("action kind %s touches orders", h.kind())
		}
	}
}
