package changes

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestChangeLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	c := &Change{ID: "ch1", Tool: "propose_price_change", Kind: "product.price", EntityType: "product", EntityID: "p1",
		Params: []byte(`{"product_id":"p1"}`), Before: []byte(`{"price_cents":450}`), BeforeHash: "h", Blob: []byte{1, 2},
		Summary: "s", RequestContext: "maki 5 euros", Status: StatusPending, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if err := s.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChange(ctx, "ch1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPending || string(got.Before) != `{"price_cents":450}` || !got.ExpiresAt.Equal(now.Add(10*time.Minute)) || len(got.Blob) != 2 || got.RequestContext != "maki 5 euros" {
		t.Errorf("round trip mismatch: %+v", got)
	}

	ok, err := s.TransitionChange(ctx, "ch1", StatusPending, StatusApplied, "", now)
	if err != nil || !ok {
		t.Fatalf("transition: %v %v", ok, err)
	}
	ok, _ = s.TransitionChange(ctx, "ch1", StatusPending, StatusRejected, "", now)
	if ok {
		t.Error("second transition from pending must fail")
	}
	if _, err := s.GetChange(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestAuditUndoSelection(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	add := func(min int, outcome string, undoOf *int64) int64 {
		id, err := s.AppendAudit(ctx, &AuditEntry{At: base.Add(time.Duration(min) * time.Minute), Source: "t", Kind: "k", Risk: "low",
			EntityType: "product", EntityID: "p", Summary: "s", Outcome: outcome, UndoOf: undoOf})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := add(0, OutcomeApplied, nil)
	a := add(20, OutcomeApplied, nil)
	b := add(25, OutcomeApplied, nil)
	add(26, OutcomeRejected, nil)

	since := base.Add(10 * time.Minute)
	last, err := s.LastUndoable(ctx, since)
	if err != nil || last.ID != b {
		t.Fatalf("want %d, got %+v %v", b, last, err)
	}
	undo := add(27, OutcomeApplied, &b)
	if err := s.MarkUndone(ctx, b, undo); err != nil {
		t.Fatal(err)
	}
	last, _ = s.LastUndoable(ctx, since)
	if last.ID != a {
		t.Fatalf("undo must skip undone and undo entries: want %d, got %d", a, last.ID)
	}
	if err := s.MarkUndone(ctx, a, undo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastUndoable(ctx, since); !errors.Is(err, ErrNotFound) {
		t.Errorf("entry %d is older than the window, want ErrNotFound, got %v", old, err)
	}

	recent, _ := s.RecentAudit(ctx, 2)
	if len(recent) != 2 || recent[0].ID != undo {
		t.Errorf("recent audit order wrong: %+v", recent)
	}
}

func TestPendingUndoFor(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	aid := int64(7)
	_ = s.CreateChange(ctx, &Change{ID: "u1", Tool: "undo_last_change", Kind: "k", EntityType: "e", EntityID: "1", Params: []byte(`{}`), Before: []byte(`{}`),
		Status: StatusPending, UndoOf: &aid, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	id, err := s.PendingUndoFor(ctx, aid, now)
	if err != nil || id != "u1" {
		t.Fatalf("got %q %v", id, err)
	}
	id, _ = s.PendingUndoFor(ctx, aid, now.Add(2*time.Minute))
	if id != "" {
		t.Errorf("expired pending undo must not count, got %q", id)
	}
}
