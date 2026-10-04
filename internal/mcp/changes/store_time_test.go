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
