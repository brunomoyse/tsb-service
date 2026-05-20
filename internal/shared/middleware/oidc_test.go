package middleware

import (
	"context"
	"errors"
	"testing"
)

type stubUserLookup struct {
	appID string
	err   error
	calls int
}

func (s *stubUserLookup) ResolveZitadelID(_ context.Context, _, _, _, _ string) (string, error) {
	s.calls++
	return s.appID, s.err
}

func TestResolveAppUserID(t *testing.T) {
	const sub = "373762126155612239" // Google numeric ID — not a UUID

	t.Run("nil userLookup refuses the request", func(t *testing.T) {
		v := &OIDCVerifier{}
		appID, ok := v.resolveAppUserID(context.Background(), sub, "", "", "")
		if ok {
			t.Fatalf("expected ok=false when userLookup is nil, got appID=%q", appID)
		}
		if appID != "" {
			t.Fatalf("expected empty appID on refusal, got %q", appID)
		}
	})

	t.Run("lookup error refuses the request", func(t *testing.T) {
		lookup := &stubUserLookup{err: errors.New("db down")}
		v := &OIDCVerifier{userLookup: lookup}
		appID, ok := v.resolveAppUserID(context.Background(), sub, "", "", "")
		if ok {
			t.Fatalf("expected ok=false on lookup error, got appID=%q", appID)
		}
		if appID != "" {
			t.Fatalf("expected empty appID on refusal, got %q (raw sub must not leak)", appID)
		}
		if lookup.calls != 1 {
			t.Fatalf("expected userLookup called once, got %d", lookup.calls)
		}
	})

	t.Run("successful lookup returns the app UUID", func(t *testing.T) {
		want := "11111111-1111-1111-1111-111111111111"
		lookup := &stubUserLookup{appID: want}
		v := &OIDCVerifier{userLookup: lookup}
		appID, ok := v.resolveAppUserID(context.Background(), sub, "u@example.com", "First", "Last")
		if !ok {
			t.Fatalf("expected ok=true on successful lookup")
		}
		if appID != want {
			t.Fatalf("expected appID=%q, got %q", want, appID)
		}
	})
}
