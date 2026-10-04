package changes

import (
	"errors"
	"testing"
	"time"
)

// Timestamps are stored as text and compared by SQLite as text, so the stored
// format must sort chronologically: an instant on a whole second must not sort
// after an instant a few nanoseconds later.
func TestTimestampsSortChronologically(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	whole := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	if _, err := s.AppendAudit(ctx, &AuditEntry{At: whole, Source: "s", Kind: "k", Risk: "low", EntityType: "e", EntityID: "1", Summary: "s", Outcome: OutcomeApplied}); err != nil {
		t.Fatal(err)
	}
	// The undo window starts a nanosecond after the entry: it is outside.
	if _, err := s.LastUndoable(ctx, whole.Add(time.Nanosecond)); !errors.Is(err, ErrNotFound) {
		t.Errorf("an entry older than the window start must not be undoable: %v", err)
	}
	// Half a second before the entry the window includes it.
	if _, err := s.LastUndoable(ctx, whole.Add(-500*time.Millisecond)); err != nil {
		t.Errorf("an entry inside the window must be undoable: %v", err)
	}

	// The same for the expiry of a pending undo.
	aid := int64(1)
	c := newChange("u1")
	c.UndoOf = &aid
	c.ExpiresAt = whole
	if err := s.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.PendingUndoFor(ctx, aid, whole.Add(time.Nanosecond)); id != "" {
		t.Errorf("a pending undo expired a nanosecond ago must not count: %q", id)
	}
	if id, _ := s.PendingUndoFor(ctx, aid, whole.Add(-time.Nanosecond)); id != "u1" {
		t.Errorf("a pending undo expiring in a nanosecond must count: %q", id)
	}

	// Ordering of the log relies on ids, but AppliedSince filters by time.
	if got, _ := s.AppliedSince(ctx, "k", whole.Add(time.Nanosecond)); len(got) != 0 {
		t.Errorf("AppliedSince included an older entry: %+v", got)
	}
	if got, _ := s.AppliedSince(ctx, "k", whole); len(got) != 1 {
		t.Errorf("AppliedSince must include an entry exactly at the start: %+v", got)
	}
}

// Rows written before the fixed-width layout carry timestamps like "…:00Z" and "…:00.5Z". They must
// still be read back as the same instants through GetAudit, GetChange and the list queries.
func TestRowsInTheOldTimestampLayoutStillRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	whole := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	half := whole.Add(500 * time.Millisecond)

	var ids []int64
	for _, at := range []time.Time{whole, half} {
		id, err := s.AppendAudit(ctx, &AuditEntry{At: at, Source: "s", Kind: "k", Risk: "low", EntityType: "e", EntityID: "1", Summary: "s", Outcome: OutcomeApplied})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Rewrite them the way an older release stored them: RFC3339Nano, trailing zeros trimmed.
	for i, at := range []time.Time{whole, half} {
		if _, err := s.db.ExecContext(ctx, `UPDATE audit_log SET at = ? WHERE id = ?`, at.Format(time.RFC3339Nano), ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	var raw []string
	rows, err := s.db.QueryContext(ctx, `SELECT at FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		raw = append(raw, at)
	}
	_ = rows.Close()
	if len(raw) != 2 || raw[0] != "2026-10-03T12:00:00Z" || raw[1] != "2026-10-03T12:00:00.5Z" {
		t.Fatalf("test setup: stored layouts = %q", raw)
	}

	for i, want := range []time.Time{whole, half} {
		got, err := s.GetAudit(ctx, ids[i])
		if err != nil || !got.At.Equal(want) {
			t.Errorf("GetAudit(%d).At = %v (%v), want %v", ids[i], got, err, want)
		}
	}
	recent, err := s.RecentAudit(ctx, 5)
	if err != nil || len(recent) != 2 || !recent[0].At.Equal(half) || !recent[1].At.Equal(whole) {
		t.Errorf("RecentAudit = %+v (%v)", recent, err)
	}
	// Queries against them still work for windows well away from the boundary.
	if got, err := s.LastUndoable(ctx, whole.Add(-time.Minute)); err != nil || got.ID != ids[1] {
		t.Errorf("LastUndoable over old rows = %+v (%v)", got, err)
	}

	// The same for a change.
	c := newChange("old1")
	c.CreatedAt, c.ExpiresAt = whole, half
	if err := s.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE changes SET created_at = ?, expires_at = ?, decided_at = ? WHERE id = 'old1'`,
		whole.Format(time.RFC3339Nano), half.Format(time.RFC3339Nano), half.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChange(ctx, "old1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(whole) || !got.ExpiresAt.Equal(half) || got.DecidedAt == nil || !got.DecidedAt.Equal(half) {
		t.Errorf("GetChange times = %v %v %v", got.CreatedAt, got.ExpiresAt, got.DecidedAt)
	}
}
