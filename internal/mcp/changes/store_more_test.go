package changes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func newChange(id string) *Change {
	return &Change{ID: id, Tool: "propose_price_change", Kind: "product.price", EntityType: "product", EntityID: "p1", Params: []byte(`{}`), Before: []byte(`{}`),
		BeforeHash: "h", Summary: "s", Status: StatusPending, CreatedAt: t0, ExpiresAt: t0.Add(10 * time.Minute)}
}

func TestOpenInMemory(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateChange(t.Context(), newChange("mem1")); err != nil {
		t.Fatal(err)
	}
	if c, err := s.GetChange(t.Context(), "mem1"); err != nil || c.ID != "mem1" {
		t.Fatalf("%v %v", c, err)
	}
}

func TestOpenFailures(t *testing.T) {
	dir := t.TempDir()
	// The parent directory does not exist.
	if s, err := Open(filepath.Join(dir, "missing", "x.db")); err == nil || !strings.Contains(err.Error(), "migrate sqlite") {
		if s != nil {
			_ = s.Close()
		}
		t.Errorf("missing directory: %v", err)
	}
	// The parent is a file.
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(filepath.Join(f, "x.db")); err == nil {
		_ = s.Close()
		t.Error("a path below a file must fail")
	}
	// Not a database.
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("this is not sqlite ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(junk); err == nil {
		_ = s.Close()
		t.Error("a file that is not a database must fail")
	}
}

func TestAddColumnsErrors(t *testing.T) {
	s := openTest(t)
	// A missing table cannot be altered.
	if _, err := s.db.Exec("DROP TABLE changes"); err != nil {
		t.Fatal(err)
	}
	if err := addColumns(s.db); err == nil {
		t.Error("addColumns on a missing table must fail")
	}
	// A closed database cannot be inspected.
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := addColumns(db); err == nil {
		t.Error("addColumns on a closed database must fail")
	}
}

func TestEveryOperationReportsABrokenDatabase(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	checks := map[string]func() error{
		"Ping":             func() error { return s.Ping(ctx) },
		"CreateChange":     func() error { return s.CreateChange(ctx, newChange("x")) },
		"GetChange":        func() error { _, err := s.GetChange(ctx, "x"); return err },
		"TransitionChange": func() error { _, err := s.TransitionChange(ctx, "x", StatusPending, StatusApplied, "", t0); return err },
		"AppendAudit":      func() error { _, err := s.AppendAudit(ctx, &AuditEntry{At: t0, Kind: "k"}); return err },
		"MarkUndone":       func() error { return s.MarkUndone(ctx, 1, 2) },
		"RecentAudit":      func() error { _, err := s.RecentAudit(ctx, 5); return err },
		"GetAudit":         func() error { _, err := s.GetAudit(ctx, 1); return err },
		"LastUndoable":     func() error { _, err := s.LastUndoable(ctx, t0); return err },
		"AppliedSince":     func() error { _, err := s.AppliedSince(ctx, "k", t0); return err },
		"PendingUndoFor":   func() error { _, err := s.PendingUndoFor(ctx, 1, t0); return err },
	}
	for name, check := range checks {
		err := check()
		if err == nil {
			t.Errorf("%s on a closed database must fail", name)
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("%s: a broken database is not 'not found'", name)
		}
	}
}

func TestCreateChangeRejectsDuplicateID(t *testing.T) {
	s := openTest(t)
	if err := s.CreateChange(t.Context(), newChange("dup")); err != nil {
		t.Fatal(err)
	}
	err := s.CreateChange(t.Context(), newChange("dup"))
	if err == nil || !strings.Contains(err.Error(), "insert change") {
		t.Errorf("duplicate id: %v", err)
	}
}

func TestChangeRoundTripKeepsEveryField(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	brussels := time.FixedZone("CEST", 2*3600)
	undoOf := int64(42)
	c := newChange("full")
	c.Blob = []byte{0, 1, 2, 255}
	c.SummaryZh, c.RequestContext, c.UndoOf = "中文", "ctx", &undoOf
	c.CreatedAt = time.Date(2026, 10, 3, 14, 0, 0, 123456789, brussels) // 12:00 UTC
	c.ExpiresAt = c.CreatedAt.Add(10 * time.Minute)
	if err := s.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChange(ctx, "full")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tool != c.Tool || got.Kind != c.Kind || got.EntityType != "product" || got.EntityID != "p1" || got.BeforeHash != "h" || got.Summary != "s" || got.SummaryZh != "中文" ||
		got.RequestContext != "ctx" || got.UndoOf == nil || *got.UndoOf != 42 || string(got.Blob) != string(c.Blob) || got.DecidedAt != nil || got.Error != "" {
		t.Errorf("round trip: %+v", got)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) || got.CreatedAt.Nanosecond() != 123456789 || !got.ExpiresAt.Equal(c.ExpiresAt) {
		t.Errorf("timestamps: %v %v", got.CreatedAt, got.ExpiresAt)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "BeforeHash") || strings.Contains(string(b), "Blob") || strings.Contains(string(b), "h\"") && false {
		t.Errorf("the hash and the blob must not be serialised: %s", b)
	}
}

func TestTransitionChange(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	if err := s.CreateChange(ctx, newChange("c1")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TransitionChange(ctx, "missing", StatusPending, StatusApplied, "", t0); ok || err != nil {
		t.Errorf("unknown id: %v %v", ok, err)
	}
	if ok, err := s.TransitionChange(ctx, "c1", StatusApplied, StatusRejected, "", t0); ok || err != nil {
		t.Errorf("wrong source status: %v %v", ok, err)
	}
	at := t0.Add(3 * time.Minute)
	if ok, err := s.TransitionChange(ctx, "c1", StatusPending, StatusFailed, "boom", at); !ok || err != nil {
		t.Fatalf("transition: %v %v", ok, err)
	}
	got, _ := s.GetChange(ctx, "c1")
	if got.Status != StatusFailed || got.Error != "boom" || got.DecidedAt == nil || !got.DecidedAt.Equal(at) {
		t.Errorf("after transition: %+v", got)
	}
	// Only one of two concurrent decisions can win.
	if err := s.CreateChange(ctx, newChange("c2")); err != nil {
		t.Fatal(err)
	}
	wins := 0
	for _, to := range []Status{StatusApplied, StatusRejected, StatusExpired} {
		if ok, _ := s.TransitionChange(ctx, "c2", StatusPending, to, "", t0); ok {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d decisions won", wins)
	}
}

func TestAuditRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	undoOf := int64(5)
	id, err := s.AppendAudit(ctx, &AuditEntry{At: t0, Source: "POST /x", ChangeID: "chg_1", Kind: "product.price", Risk: "sensitive", EntityType: "product", EntityID: "p1",
		Params: json.RawMessage(`{"a":1}`), Before: json.RawMessage(`{"b":2}`), After: json.RawMessage(`{"c":3}`), Summary: "s", SummaryZh: "中", RequestContext: "ctx", Outcome: OutcomeFailed, Error: "boom", UndoOf: &undoOf})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	got, err := s.GetAudit(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Source != "POST /x" || got.ChangeID != "chg_1" || got.Risk != "sensitive" || string(got.Params) != `{"a":1}` || string(got.Before) != `{"b":2}` || string(got.After) != `{"c":3}` ||
		got.SummaryZh != "中" || got.RequestContext != "ctx" || got.Outcome != OutcomeFailed || got.Error != "boom" || got.UndoOf == nil || *got.UndoOf != 5 || got.UndoneBy != nil || !got.At.Equal(t0) {
		t.Errorf("audit: %+v", got)
	}
	// Empty JSON is stored as NULL and comes back empty.
	id2, _ := s.AppendAudit(ctx, &AuditEntry{At: t0, Source: "s", Kind: "k", Risk: "low", EntityType: "e", EntityID: "1", Summary: "s", Outcome: OutcomeApplied})
	bare, err := s.GetAudit(ctx, id2)
	if err != nil || bare.Params != nil || bare.Before != nil || bare.After != nil || bare.UndoOf != nil {
		t.Errorf("bare audit: %+v %v", bare, err)
	}
	if _, err := s.GetAudit(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown audit id: %v", err)
	}
	// MarkUndone fills undone_by once and never overwrites it.
	if err := s.MarkUndone(ctx, id2, id); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUndone(ctx, id2, 777); err != nil {
		t.Fatal(err)
	}
	bare, _ = s.GetAudit(ctx, id2)
	if bare.UndoneBy == nil || *bare.UndoneBy != id {
		t.Errorf("undone_by = %v, want %d", bare.UndoneBy, id)
	}
}

func TestLastUndoableWindowAndOutcomes(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	add := func(at time.Time, outcome string) int64 {
		id, err := s.AppendAudit(ctx, &AuditEntry{At: at, Source: "s", Kind: "k", Risk: "low", EntityType: "e", EntityID: "1", Summary: "s", Outcome: outcome})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err := s.LastUndoable(ctx, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty log: %v", err)
	}
	// Failed, conflicted and expired entries are never undoable.
	for _, o := range []string{OutcomeFailed, OutcomeConflict, OutcomeExpired, OutcomeRejected} {
		add(t0.Add(time.Minute), o)
	}
	if _, err := s.LastUndoable(ctx, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("only non-applied entries: %v", err)
	}
	id := add(t0, OutcomeApplied)
	if got, err := s.LastUndoable(ctx, t0); err != nil || got.ID != id {
		t.Errorf("an entry exactly at the window start is included: %v %v", got, err)
	}
}

func TestAppliedSince(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	add := func(min int, kind, outcome string) int64 {
		id, err := s.AppendAudit(ctx, &AuditEntry{At: t0.Add(time.Duration(min) * time.Minute), Source: "s", Kind: kind, Risk: "sensitive", EntityType: "e", EntityID: "1", Summary: "s", Outcome: outcome})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add(-10, "restaurant.schedule", OutcomeApplied) // too old
	a := add(0, "restaurant.schedule", OutcomeApplied)
	add(1, "restaurant.schedule", OutcomeFailed)
	add(2, "product.price", OutcomeApplied)
	b := add(3, "restaurant.schedule", OutcomeApplied)
	got, err := s.AppliedSince(ctx, "restaurant.schedule", t0)
	if err != nil || len(got) != 2 || got[0].ID != b || got[1].ID != a {
		t.Fatalf("applied since: %+v %v", got, err)
	}
	if got, _ := s.AppliedSince(ctx, "nothing", t0); len(got) != 0 {
		t.Errorf("unknown kind: %+v", got)
	}
}

func TestPendingUndoForIgnoresDecidedChanges(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	aid := int64(9)
	c := newChange("u1")
	c.UndoOf = &aid
	if err := s.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.PendingUndoFor(ctx, aid, t0); id != "u1" {
		t.Fatalf("pending undo: %q", id)
	}
	if id, _ := s.PendingUndoFor(ctx, aid+1, t0); id != "" {
		t.Errorf("another entry: %q", id)
	}
	// Exactly at the expiry the undo no longer counts.
	if id, _ := s.PendingUndoFor(ctx, aid, c.ExpiresAt); id != "" {
		t.Errorf("at expiry: %q", id)
	}
	_, _ = s.TransitionChange(ctx, "u1", StatusPending, StatusRejected, "", t0)
	if id, _ := s.PendingUndoFor(ctx, aid, t0); id != "" {
		t.Errorf("a rejected undo must not block a new one: %q", id)
	}
}

func TestRecentAuditLimit(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	for i := range 5 {
		if _, err := s.AppendAudit(ctx, &AuditEntry{At: t0.Add(time.Duration(i) * time.Second), Source: "s", Kind: "k", Risk: "low", EntityType: "e", EntityID: "1", Summary: "s", Outcome: OutcomeApplied}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RecentAudit(ctx, 3)
	if err != nil || len(got) != 3 || got[0].ID != 5 || got[2].ID != 3 {
		t.Errorf("recent: %+v %v", got, err)
	}
	if got, _ := s.RecentAudit(ctx, 0); len(got) != 0 {
		t.Errorf("limit 0: %+v", got)
	}
}

func TestCorruptRowsAreReportedNotSkipped(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	// SQLite is dynamically typed: a text value in an integer column is storable
	// but cannot be scanned.
	if _, err := s.db.Exec(`INSERT INTO audit_log (at, source, kind, risk, entity_type, entity_id, summary, outcome, undo_of)
		VALUES (?, 's', 'k', 'low', 'e', '1', 's', 'applied', 'not-a-number')`, ts(t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentAudit(ctx, 5); err == nil || !strings.Contains(err.Error(), "scan audit") {
		t.Errorf("RecentAudit over a corrupt row: %v", err)
	}
	if _, err := s.GetAudit(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetAudit over a corrupt row: %v", err)
	}
}
