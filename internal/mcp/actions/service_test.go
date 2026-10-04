package actions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

func TestErrorMessages(t *testing.T) {
	if Userf("bad %d", 3).Error() != "bad 3" {
		t.Error("Userf")
	}
	c := &ConflictError{Fields: []string{"price_cents", "code"}}
	if c.Error() != "The item was changed elsewhere since this was proposed (price_cents, code). Please propose the change again." || !errors.Is(c, ErrConflict) {
		t.Errorf("conflict: %q", c.Error())
	}
	tests := []struct {
		status changes.Status
		msg    string
		is     error
	}{
		{changes.StatusExpired, "This change has expired. Please propose it again.", ErrExpired},
		{changes.StatusApplied, "This change was already applied.", ErrNotPending},
		{changes.StatusRejected, "This change was already rejected.", ErrNotPending},
		{changes.StatusConflict, "This change can no longer be applied (status: conflict).", ErrNotPending},
		{changes.StatusFailed, "This change can no longer be applied (status: failed).", ErrNotPending},
	}
	for _, tt := range tests {
		e := &StatusError{Status: tt.status}
		if e.Error() != tt.msg || !errors.Is(e, tt.is) {
			t.Errorf("%s: %q is %v", tt.status, e.Error(), errors.Is(e, tt.is))
		}
		if tt.is == ErrExpired && errors.Is(e, ErrNotPending) {
			t.Error("an expired change is not just 'not pending'")
		}
	}
}

func TestNewServiceDefaults(t *testing.T) {
	fake := newFixture(t)
	store, err := changes.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	env := &Env{Up: fake.svc.env.Up, Loc: brussels, PriceMaxPct: 50}
	svc := NewService(env, store, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if svc.Env() != env || svc.Store() != store {
		t.Error("accessors")
	}
	if env.Now == nil || time.Since(env.Now()) > time.Minute || time.Since(env.Now()) < -time.Minute {
		t.Error("a missing clock defaults to time.Now")
	}
	// Every registered kind is reachable and has a risk.
	kinds := []string{KindProductAvailability, KindProductVisibility, KindProductAvailabilityBulk, KindProductPrice, KindProductVAT, KindProductUpdate, KindProductCreate, KindProductImage,
		KindChoiceGroupUpsert, KindChoiceGroupDelete, KindChoiceUpsert, KindChoiceDelete, KindPreparationMinutes, KindOpeningHours, KindOrderingHours, KindSchedule,
		KindCouponCreate, KindCouponUpdate, KindCouponActivate, KindCouponDeactivate}
	if len(registry()) != len(kinds) {
		t.Errorf("registry has %d handlers, want %d", len(registry()), len(kinds))
	}
	for _, k := range kinds {
		if r := svc.RiskOf(k); r != RiskLow && r != RiskSensitive {
			t.Errorf("%s has no risk", k)
		}
	}
	// Only toggles, preparation time and coupon deactivation apply without confirmation.
	low := []string{KindProductAvailability, KindProductVisibility, KindPreparationMinutes, KindCouponDeactivate}
	for _, k := range kinds {
		if (svc.RiskOf(k) == RiskLow) != slices.Contains(low, k) {
			t.Errorf("%s risk = %s", k, svc.RiskOf(k))
		}
	}
}

func TestUnknownKindAndBadParams(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Propose(f.ctx, "t", "bogus.kind", struct{}{}, nil, "", nil); err == nil {
		t.Error("Propose with an unknown kind")
	}
	if _, err := f.svc.ApplyNow(f.ctx, "t", "bogus.kind", struct{}{}, "", nil); err == nil {
		t.Error("ApplyNow with an unknown kind")
	}
	// Parameters that cannot be marshalled.
	if _, err := f.svc.Propose(f.ctx, "t", KindPreparationMinutes, make(chan int), nil, "", nil); err == nil {
		t.Error("Propose with unmarshalable params")
	}
	if _, err := f.svc.ApplyNow(f.ctx, "t", KindPreparationMinutes, make(chan int), "", nil); err == nil {
		t.Error("ApplyNow with unmarshalable params")
	}
	// Parameters of the wrong shape never reach upstream.
	for _, kind := range []string{KindProductPrice, KindPreparationMinutes} {
		_, err := f.svc.Propose(f.ctx, "t", kind, "not an object", nil, "", nil)
		if err == nil {
			t.Errorf("%s: params of the wrong type must be refused", kind)
		}
	}
	h := f.handler(t, KindProductPrice)
	if _, err := h.execute(f.ctx, f.svc.env, json.RawMessage(`"x"`), nil); err == nil {
		t.Error("execute with bad params")
	}
	if _, _, err := h.inverse(json.RawMessage(`"x"`), json.RawMessage(`{}`), nil); err == nil {
		t.Error("inverse with bad params")
	}
	if _, _, err := h.inverse(json.RawMessage(`{}`), json.RawMessage(`"x"`), nil); err == nil {
		t.Error("inverse with bad before state")
	}
	if len(f.fake.Ops()) != 0 {
		t.Errorf("nothing may reach upstream: %v", f.fake.Ops())
	}
}

func TestProposeStoresChangeDetails(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Propose(f.ctx, "propose_price_change", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, []byte("blob"), "ctx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "pending" || p.ExpiresAt != "2026-10-03T13:10:00+02:00" {
		t.Errorf("proposal: %+v", p)
	}
	c, err := f.svc.GetChange(f.ctx, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tool != "propose_price_change" || c.Kind != KindProductPrice || c.EntityType != "product" || c.EntityID != "p-maki-saumon" || c.RequestContext != "ctx" || string(c.Blob) != "blob" ||
		string(c.Before) != `{"price_cents":450}` || c.BeforeHash != hashOf(c.Before) || c.Status != changes.StatusPending || !c.ExpiresAt.Equal(c.CreatedAt.Add(10*time.Minute)) {
		t.Errorf("stored change: %+v", c)
	}
	if len(p.ChangeID) != len("chg_")+24 || p.ChangeID[:4] != "chg_" {
		t.Errorf("change id %q", p.ChangeID)
	}
	other := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	if other.ChangeID == p.ChangeID {
		t.Error("change ids must be unique")
	}
	if _, err := f.svc.GetChange(f.ctx, "chg_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown change: %v", err)
	}
}

func TestProposeFailures(t *testing.T) {
	f := newFixture(t)
	f.fail("McpProduct")
	_, err := f.svc.Propose(f.ctx, "t", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "", nil)
	wantUpstreamErr(t, err)
	f.unfail("McpProduct")
	_, err = f.svc.Propose(f.ctx, "t", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 5000}, nil, "", nil)
	wantUserErr(t, err, "Price change refused")

	_ = f.svc.Store().Close()
	_, err = f.svc.Propose(f.ctx, "t", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "", nil)
	if err == nil {
		t.Error("a change that cannot be stored must not be reported as proposed")
	}
	if _, err := f.svc.GetChange(f.ctx, "chg_x"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a store failure is not 'not found': %v", err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, "chg_x", ""); err == nil {
		t.Error("ApplyPending on a broken store")
	}
	if _, err := f.svc.Reject(f.ctx, "chg_x", ""); err == nil {
		t.Error("Reject on a broken store")
	}
	if _, err := f.svc.Undo(f.ctx, ""); err == nil || errors.As(err, new(*UserError)) {
		t.Errorf("Undo on a broken store: %v", err)
	}
}

func TestApplyNowGuards(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.ApplyNow(f.ctx, "t", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, "", nil)
	if err == nil {
		t.Fatal("a sensitive action must never be applied without confirmation")
	}
	if f.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("price changed")
	}
	if ops := f.fake.Ops(); len(ops) != 0 {
		t.Errorf("no upstream call expected, got %v", ops)
	}

	_, err = f.svc.ApplyNow(f.ctx, "t", KindProductAvailability, ProductToggleParams{ProductID: "p-nope"}, "", nil)
	wantUserErr(t, err, "No product")
	r, err := f.svc.ApplyNow(f.ctx, "t", KindProductAvailability, ProductToggleParams{ProductID: "p-maki-saumon", Value: true}, "", nil)
	if err != nil || !r.NoOp || r.Applied || r.AuditID != 0 {
		t.Errorf("no-op: %+v %v", r, err)
	}
}

func TestApplyNowStillReportsSuccessWhenTheAuditWriteFails(t *testing.T) {
	// The change reached upstream, so the owner must be told it was applied
	// even though the audit row could not be written (it is logged).
	f := newFixture(t)
	_ = f.svc.store.Close()
	// Prepare/execute do not need the store; only the audit append does.
	r, err := f.svc.ApplyNow(f.ctx, "t", KindProductAvailability, ProductToggleParams{ProductID: "p-maki-saumon", Value: false}, "", nil)
	if err != nil || !r.Applied || r.AuditID != 0 {
		t.Fatalf("result: %+v %v", r, err)
	}
	if f.fake.ProductByID("p-maki-saumon").IsAvailable {
		t.Error("change not applied upstream")
	}
}

func TestGetChangeExpiresPendingChanges(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	f.clock.Advance(10*time.Minute - time.Second)
	c, _ := f.svc.GetChange(f.ctx, p.ChangeID)
	if c.Status != changes.StatusPending {
		t.Fatalf("one second before expiry: %s", c.Status)
	}
	f.clock.Advance(time.Second) // exactly at the expiry
	c, _ = f.svc.GetChange(f.ctx, p.ChangeID)
	if c.Status != changes.StatusExpired || c.DecidedAt == nil {
		t.Fatalf("at expiry: %+v", c)
	}
	// Expiry is persisted, and Reject sees it.
	_, err := f.svc.Reject(f.ctx, p.ChangeID, "")
	if !errors.Is(err, ErrExpired) {
		t.Errorf("reject expired: %v", err)
	}
}

func TestApplyPendingEdgeCases(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ApplyPending(f.ctx, "chg_missing", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown change: %v", err)
	}

	// Expired: refused and audited once per attempt, never applied.
	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	f.clock.Advance(11 * time.Minute)
	_, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "trop tard")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 5)
	if len(audit) != 1 || audit[0].Outcome != changes.OutcomeExpired || audit[0].Error != "expired" || audit[0].RequestContext != "trop tard" || audit[0].Source != SourceApply {
		t.Errorf("audit: %+v", audit)
	}

	// A change stored with a kind this build does not know cannot be applied.
	now := f.clock.Now().UTC()
	if err := f.svc.Store().CreateChange(f.ctx, &changes.Change{ID: "chg_alien", Tool: "t", Kind: "alien.kind", EntityType: "x", EntityID: "y", Params: json.RawMessage(`{}`), Before: json.RawMessage(`{}`),
		Status: changes.StatusPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPending(f.ctx, "chg_alien", ""); err == nil {
		t.Error("unknown kind")
	}
}

func TestApplyPendingUpstreamDownLeavesChangePending(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	f.fail("McpProduct")
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	wantUpstreamErr(t, err)
	if c == nil || c.Status != changes.StatusPending {
		t.Fatalf("an unreachable upstream must leave the change pending: %+v", c)
	}
	// ... so it can simply be retried.
	f.unfail("McpProduct")
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if f.fake.ProductByID("p-maki-saumon").Price != "5" {
		t.Error("retry did not apply")
	}
}

func TestApplyPendingConflictWhenTargetVanished(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindChoiceDelete, ChoiceRef{ChoiceID: "c-spicy"})
	// Someone deleted the choice in the dashboard first.
	f.fake.Lock()
	g := &f.fake.Products[2].ChoiceGroups[0]
	g.Choices = g.Choices[:1]
	f.fake.Unlock()
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	ce, ok := errors.AsType[*ConflictError](err)
	if !ok || c.Status != changes.StatusConflict {
		t.Fatalf("want conflict, got %v (%v)", err, c)
	}
	if len(ce.Fields) != 1 || !contentsContain(ce.Fields[0], `No choice with id "c-spicy"`) {
		t.Errorf("fields = %v", ce.Fields)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 1)
	if audit[0].Outcome != changes.OutcomeConflict {
		t.Errorf("audit: %+v", audit[0])
	}
	// A conflict is final: the proposal must be made again.
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); !errors.Is(err, ErrNotPending) {
		t.Errorf("second apply: %v", err)
	}
}

func contentsContain(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestApplyPendingNeverAppliesTwice(t *testing.T) {
	f := newFixture(t)
	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, op := range f.fake.Ops() {
		if op == "McpUpdateProduct" {
			updates++
		}
	}
	for range 3 {
		if _, err := f.svc.ApplyPending(f.ctx, p.ChangeID, ""); !errors.Is(err, ErrNotPending) {
			t.Fatalf("repeat apply: %v", err)
		}
	}
	after := 0
	for _, op := range f.fake.Ops() {
		if op == "McpUpdateProduct" {
			after++
		}
	}
	if updates != 1 || after != 1 {
		t.Errorf("McpUpdateProduct calls: %d then %d, want exactly 1", updates, after)
	}
}

func TestRejectEdgeCases(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Reject(f.ctx, "chg_missing", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown change: %v", err)
	}
	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	c, err := f.svc.Reject(f.ctx, p.ChangeID, "")
	if err != nil || c.Status != changes.StatusRejected || c.DecidedAt == nil {
		t.Fatalf("reject: %+v %v", c, err)
	}
	// Rejecting twice reports the status instead of auditing again.
	_, err = f.svc.Reject(f.ctx, p.ChangeID, "")
	if se, ok := errors.AsType[*StatusError](err); !ok || se.Status != changes.StatusRejected {
		t.Errorf("second reject: %v", err)
	}
	audit, _ := f.svc.Store().RecentAudit(f.ctx, 5)
	if len(audit) != 1 || audit[0].Source != SourceReject {
		t.Errorf("audit: %+v", audit)
	}
	if f.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("rejected change must not apply")
	}
	// The request context falls back to the one given at proposal time.
	p2, _ := f.svc.Propose(f.ctx, "t", KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500}, nil, "proposé", nil)
	_, _ = f.svc.Reject(f.ctx, p2.ChangeID, "")
	audit, _ = f.svc.Store().RecentAudit(f.ctx, 1)
	if audit[0].RequestContext != "proposé" {
		t.Errorf("request context = %q", audit[0].RequestContext)
	}
}

func (f *fixture) addAudit(t *testing.T, e changes.AuditEntry) int64 {
	t.Helper()
	if e.At.IsZero() {
		e.At = f.clock.Now().UTC()
	}
	if e.Outcome == "" {
		e.Outcome = changes.OutcomeApplied
	}
	e.Source, e.EntityType, e.EntityID, e.Summary = "test", "x", "y", "an old change"
	id, err := f.svc.Store().AppendAudit(f.ctx, &e)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUndoEdgeCases(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Undo(f.ctx, "")
	wantUserErr(t, err, "There is no change from the last 30 minutes to undo.")

	f.addAudit(t, changes.AuditEntry{Kind: "alien.kind", Risk: "low", Params: json.RawMessage(`{}`), Before: json.RawMessage(`{}`), After: json.RawMessage(`{}`)})
	if _, err := f.svc.Undo(f.ctx, ""); err == nil || errors.As(err, new(*UserError)) {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestUndoUnreadableBeforeState(t *testing.T) {
	f := newFixture(t)
	f.addAudit(t, changes.AuditEntry{Kind: KindProductPrice, Risk: "sensitive", Params: json.RawMessage(`{"product_id":"p-maki-saumon","new_price_cents":500}`), Before: json.RawMessage(`"corrupt"`), After: json.RawMessage(`{}`)})
	_, err := f.svc.Undo(f.ctx, "")
	if err == nil || errors.As(err, new(*UserError)) || !contentsContain(err.Error(), "compute undo") {
		t.Errorf("want an internal 'compute undo' error, got %v", err)
	}
}

func TestUndoLowRiskFailureAndNoOp(t *testing.T) {
	f := newFixture(t)
	params := ProductToggleParams{ProductID: "p-maki-saumon", Value: false}
	if _, err := f.svc.ApplyNow(f.ctx, "t", KindProductAvailability, params, "", nil); err != nil {
		t.Fatal(err)
	}
	original, err := f.svc.Store().LastUndoable(f.ctx, f.clock.Now().Add(-UndoWindow))
	if err != nil {
		t.Fatal(err)
	}
	f.fail("McpUpdateProduct")
	if _, err := f.svc.Undo(f.ctx, ""); err == nil {
		t.Fatal("undo must report the upstream failure")
	}
	f.unfail("McpUpdateProduct")

	// The failed undo was audited as failed, pointing at the change it tried to revert, and the
	// original is neither marked undone nor out of the undo list.
	recent, err := f.svc.Store().RecentAudit(f.ctx, 5)
	if err != nil || len(recent) != 2 {
		t.Fatalf("audit: %v %v", recent, err)
	}
	failed := recent[0]
	if failed.Outcome != changes.OutcomeFailed || failed.Source != "undo_last_change" || failed.UndoOf == nil || *failed.UndoOf != original.ID || failed.Error == "" {
		t.Errorf("failed undo audit row = %+v", failed)
	}
	if got, _ := f.svc.Store().GetAudit(f.ctx, original.ID); got == nil || got.UndoneBy != nil {
		t.Errorf("a failed undo must not mark the original undone: %+v", got)
	}

	// Meanwhile the product was switched back on in the dashboard: undoing is a no-op.
	f.editProduct("p-maki-saumon", func(p *upstream.Product) { p.IsAvailable = true })
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "no_op" || u.Applied != nil {
		t.Fatalf("undo: %+v %v", u, err)
	}
	contains(t, "summary", u.Summary, "Nothing to undo:", "already available")
	contains(t, "summary_zh", u.SummaryZh, "无需撤销")
}

func TestUndoSensitiveProposalNoOpAndFailure(t *testing.T) {
	f := newFixture(t)
	f.applyChange(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	// The price was put back by hand: the undo has nothing to do.
	f.editProduct("p-maki-saumon", func(p *upstream.Product) { p.Price = "4.5" })
	u, err := f.svc.Undo(f.ctx, "")
	if err != nil || u.Mode != "no_op" || u.Proposal != nil {
		t.Fatalf("undo: %+v %v", u, err)
	}
	contains(t, "summary", u.Summary, "Nothing to undo:", "already costs 4.50 EUR")

	f.fail("McpProduct")
	_, err = f.svc.Undo(f.ctx, "")
	wantUpstreamErr(t, err)
}

func TestUndoNeverTouchesUndoEntriesAndWalksBack(t *testing.T) {
	f := newFixture(t)
	for _, v := range []bool{false, true} {
		if _, err := f.svc.ApplyNow(f.ctx, "t", KindProductVisibility, ProductToggleParams{ProductID: "p-maki-saumon", Value: v}, "", nil); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(time.Minute)
	}
	if _, err := f.svc.ApplyNow(f.ctx, "t", KindPreparationMinutes, PreparationParams{Minutes: 50}, "", nil); err != nil {
		t.Fatal(err)
	}
	// First undo reverts the preparation time (the newest), the next ones walk
	// back: they never "redo" the undo.
	for i, check := range []func() bool{
		func() bool { return f.fake.Config.PreparationMinutes == 30 },
		func() bool { return f.fake.ProductByID("p-maki-saumon").IsVisible == false },
		func() bool { return f.fake.ProductByID("p-maki-saumon").IsVisible == true },
	} {
		if _, err := f.svc.Undo(f.ctx, ""); err != nil {
			t.Fatalf("undo %d: %v", i, err)
		}
		if !check() {
			t.Fatalf("undo %d did not restore the expected state", i)
		}
	}
	if _, err := f.svc.Undo(f.ctx, ""); err == nil {
		t.Error("nothing left to undo")
	}
}

func TestUndoWindowBoundary(t *testing.T) {
	apply := func(t *testing.T) *fixture {
		f := newFixture(t)
		if _, err := f.svc.ApplyNow(f.ctx, "t", KindPreparationMinutes, PreparationParams{Minutes: 45}, "", nil); err != nil {
			t.Fatal(err)
		}
		if f.fake.Config.PreparationMinutes != 45 {
			t.Fatalf("preparation = %d, want 45 after the change", f.fake.Config.PreparationMinutes)
		}
		return f
	}

	t.Run("exactly at the window the change is still undone and the old value is back", func(t *testing.T) {
		f := apply(t)
		f.clock.Advance(UndoWindow)
		u, err := f.svc.Undo(f.ctx, "")
		if err != nil || u.Mode != "applied" {
			t.Fatalf("undo: %+v %v", u, err)
		}
		if f.fake.Config.PreparationMinutes != 30 {
			t.Errorf("preparation = %d, want the original 30", f.fake.Config.PreparationMinutes)
		}
	})

	t.Run("one nanosecond past the window it is refused and nothing changes", func(t *testing.T) {
		f := apply(t)
		f.clock.Advance(UndoWindow + time.Nanosecond)
		_, err := f.svc.Undo(f.ctx, "")
		wantUserErr(t, err, "There is no change from the last 30 minutes to undo.")
		if f.fake.Config.PreparationMinutes != 45 {
			t.Errorf("preparation = %d, a refused undo must leave the change in place", f.fake.Config.PreparationMinutes)
		}
	})
}

// The undo itself is audited: it points at the change it reverted (UndoOf), says where it came
// from (Source), and the original is linked to it (UndoneBy) so it is not undone twice.
func TestUndoAuditTrail(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ApplyNow(f.ctx, "t", KindPreparationMinutes, PreparationParams{Minutes: 45}, "", nil); err != nil {
		t.Fatal(err)
	}
	original, err := f.svc.Store().LastUndoable(f.ctx, f.clock.Now().Add(-UndoWindow))
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.svc.Undo(f.ctx, "oops")
	if err != nil || u.Applied == nil {
		t.Fatalf("undo: %+v %v", u, err)
	}

	undoRow, err := f.svc.Store().GetAudit(f.ctx, u.Applied.AuditID)
	if err != nil {
		t.Fatal(err)
	}
	if undoRow.UndoOf == nil || *undoRow.UndoOf != original.ID {
		t.Errorf("UndoOf = %v, want %d", undoRow.UndoOf, original.ID)
	}
	if undoRow.Source != "undo_last_change" || undoRow.Outcome != changes.OutcomeApplied || undoRow.RequestContext != "oops" {
		t.Errorf("undo row = %+v", undoRow)
	}
	reverted, err := f.svc.Store().GetAudit(f.ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reverted.UndoneBy == nil || *reverted.UndoneBy != undoRow.ID {
		t.Errorf("UndoneBy = %v, want %d", reverted.UndoneBy, undoRow.ID)
	}
	if _, err := f.svc.Undo(f.ctx, ""); err == nil {
		t.Error("the undone change must not be undoable again, nor the undo itself")
	}
}

func TestDiffFields(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want []string
	}{
		{"changed value", `{"a":1,"b":2}`, `{"a":1,"b":3}`, []string{"b"}},
		{"key added", `{"a":1}`, `{"a":1,"c":9}`, []string{"c"}},
		{"key removed", `{"a":1,"z":0}`, `{"a":1}`, []string{"z"}},
		{"several, sorted", `{"b":1,"a":1}`, `{"b":2,"a":2,"c":1}`, []string{"a", "b", "c"}},
		{"identical", `{"a":1}`, `{"a":1}`, []string{"state"}},
		{"not objects", `[1]`, `[2]`, []string{"state"}},
		{"first not an object", `1`, `{"a":1}`, []string{"state"}},
	}
	for _, tt := range tests {
		got := diffFields(json.RawMessage(tt.a), json.RawMessage(tt.b))
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestZhHelpers(t *testing.T) {
	if dateZh("2026-10-05") != "10月5日（周一）" || dateZh("not a date") != "not a date" {
		t.Error("dateZh")
	}
	if timeZh(nil, brussels) != "无" {
		t.Error("timeZh nil")
	}
	if got := categoryNameZh(&upstream.Category{Name: "Boxes"}); got != "Boxes" {
		t.Errorf("category without zh = %s", got)
	}
	if got := categoryNameZh(&upstream.Category{Name: "Boxes", Translations: []upstream.Translation{{Language: "zh", Name: ""}}}); got != "Boxes" {
		t.Errorf("category with empty zh = %s", got)
	}
	if intPtrZh(nil) != "无" || intPtrZh(ip(4)) != "4" {
		t.Error("intPtrZh")
	}
	if nameZh(map[string]string{"zh": "汤"}, "Soupe") != "汤" || nameZh(map[string]string{}, "Soupe") != "Soupe" {
		t.Error("nameZh")
	}
}

// hookParams lets tests build handlers whose params, before-state or execute
// step misbehave in ways no real action can.
type hookParams struct {
	// F makes json.Marshal fail once Prepare sets it to NaN.
	F float64 `json:"f,omitempty"`
}

type hookSpec struct {
	risk    Risk
	prepare func(p *hookParams) (*prepared, error)
	execute func() (any, error)
}

func (f *fixture) addHook(kind string, h hookSpec) {
	f.svc.handlers[kind] = spec[hookParams, struct{}]{
		Kind: kind,
		Risk: h.risk,
		Prepare: func(_ context.Context, _ *Env, p *hookParams) (*prepared, error) {
			return h.prepare(p)
		},
		Execute: func(context.Context, *Env, hookParams, []byte) (any, error) {
			return h.execute()
		},
	}
}

func okPrepare(*hookParams) (*prepared, error) {
	return &prepared{EntityType: "hook", EntityID: "h", Before: map[string]int{"n": 1}, Summary: "hook", SummaryZh: "钩子"}, nil
}

func TestUnmarshalableStatesAreReported(t *testing.T) {
	f := newFixture(t)
	f.addHook("hook.badparams", hookSpec{risk: RiskLow, prepare: func(p *hookParams) (*prepared, error) {
		p.F = math.NaN()
		return okPrepare(p)
	}, execute: func() (any, error) { return nil, nil }})
	f.addHook("hook.badbefore", hookSpec{risk: RiskLow, prepare: func(*hookParams) (*prepared, error) {
		return &prepared{EntityType: "hook", EntityID: "h", Before: make(chan int)}, nil
	}, execute: func() (any, error) { return nil, nil }})

	for _, kind := range []string{"hook.badparams", "hook.badbefore"} {
		if _, err := f.svc.Propose(f.ctx, "t", kind, hookParams{}, nil, "", nil); err == nil {
			t.Errorf("Propose %s must fail", kind)
		}
		if _, err := f.svc.ApplyNow(f.ctx, "t", kind, hookParams{}, "", nil); err == nil {
			t.Errorf("ApplyNow %s must fail", kind)
		}
	}
	if audit, _ := f.svc.Store().RecentAudit(f.ctx, 5); len(audit) != 0 {
		t.Errorf("nothing was executed, nothing to audit: %+v", audit)
	}
}

func TestStoreFailuresAfterUpstreamSuccessAreNotFatal(t *testing.T) {
	// The upstream write already happened, so the caller is told it succeeded
	// even if bookkeeping fails afterwards; the failures are logged.
	f := newFixture(t)
	f.addHook("hook.closestore", hookSpec{risk: RiskSensitive, prepare: okPrepare, execute: func() (any, error) {
		_ = f.svc.Store().Close()
		return map[string]bool{"done": true}, nil
	}})
	undoOf := int64(7)

	// ApplyNow of an undo: audit append and mark-undone both fail.
	r, err := f.svc.ApplyNow(f.ctx, "undo", "hook.closestore", hookParams{}, "", &undoOf)
	if err != nil || !r.Applied || r.AuditID != 0 {
		t.Fatalf("ApplyNow: %+v %v", r, err)
	}
}

func TestApplyPendingBookkeepingFailuresAfterUpstreamSuccess(t *testing.T) {
	f := newFixture(t)
	f.addHook("hook.closestore", hookSpec{risk: RiskSensitive, prepare: okPrepare, execute: func() (any, error) {
		_ = f.svc.Store().Close()
		return nil, nil
	}})
	undoOf := int64(7)
	p, err := f.svc.Propose(f.ctx, "t", "hook.closestore", hookParams{}, nil, "", &undoOf)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	if err != nil || c.Status != changes.StatusApplied {
		t.Fatalf("a change applied upstream is reported as applied: %+v %v", c, err)
	}
}

func TestApplyPendingLosesTheStatusRace(t *testing.T) {
	f := newFixture(t)
	var id string
	f.addHook("hook.race", hookSpec{risk: RiskSensitive, prepare: okPrepare, execute: func() (any, error) {
		// Someone rejects the change while it is being applied.
		ok, err := f.svc.Store().TransitionChange(f.ctx, id, changes.StatusPending, changes.StatusRejected, "", f.clock.Now())
		if !ok || err != nil {
			t.Errorf("race setup: %v %v", ok, err)
		}
		return nil, nil
	}})
	p, _ := f.svc.Propose(f.ctx, "t", "hook.race", hookParams{}, nil, "", nil)
	id = p.ChangeID
	c, err := f.svc.ApplyPending(f.ctx, id, "")
	if err != nil || c.Status != changes.StatusApplied {
		t.Fatalf("%+v %v", c, err)
	}
	stored, _ := f.svc.GetChange(f.ctx, id)
	if stored.Status != changes.StatusRejected {
		t.Errorf("the other decision must be kept, got %s", stored.Status)
	}
}

func TestRejectStoreFailureAndRace(t *testing.T) {
	// Now() is called after the status check in Reject: use it to interleave.
	f := newFixture(t)
	p := f.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	calls := 0
	f.svc.env.Now = func() time.Time {
		calls++
		if calls == 2 { // GetChange is the first call
			_ = f.svc.Store().Close()
		}
		return f.clock.Now()
	}
	if _, err := f.svc.Reject(f.ctx, p.ChangeID, ""); err == nil {
		t.Error("a store failure while rejecting must be reported")
	}

	f2 := newFixture(t)
	p2 := f2.propose(t, KindProductPrice, PriceParams{ProductID: "p-maki-saumon", NewPriceCents: 500})
	calls = 0
	f2.svc.env.Now = func() time.Time {
		calls++
		if calls == 2 { // someone applied it in the meantime
			_, _ = f2.svc.Store().TransitionChange(f2.ctx, p2.ChangeID, changes.StatusPending, changes.StatusApplied, "", f2.clock.Now())
		}
		return f2.clock.Now()
	}
	c, err := f2.svc.Reject(f2.ctx, p2.ChangeID, "")
	if se, ok := errors.AsType[*StatusError](err); !ok || se.Status != changes.StatusApplied || c.Status != changes.StatusApplied {
		t.Errorf("lost race: %+v %v", c, err)
	}
}
