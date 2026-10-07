package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"go.uber.org/zap"
)

// captureTransport records events instead of sending them over the network.
type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *captureTransport) Configure(sentry.ClientOptions) {}
func (t *captureTransport) SendEvent(e *sentry.Event) {
	t.mu.Lock()
	t.events = append(t.events, e)
	t.mu.Unlock()
}
func (t *captureTransport) Flush(time.Duration) bool              { return true }
func (t *captureTransport) FlushWithContext(context.Context) bool { return true }
func (t *captureTransport) Close()                                {}

func (t *captureTransport) all() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event{}, t.events...)
}

// TestSentryBridge verifies the zap→Sentry core: Error logs are captured,
// Warn logs are not, and SkipSentry suppresses the duplicate capture.
func TestSentryBridge(t *testing.T) {
	tr := &captureTransport{}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://test@test.ingest.sentry.io/1",
		Transport: tr,
	}); err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	t.Cleanup(func() { sentry.Flush(time.Second) })

	Setup("info", "json") // installs the bridge into the global logger

	zap.L().Warn("benign warning")                        // excluded (below Error)
	zap.L().Error("boom", zap.String("detail", "kaboom")) // captured
	zap.L().Error("already reported", SkipSentry)         // suppressed
	FromContext(SetRequestID(t.Context(), "req-9")).
		Error("with context") // captured + request_id tag

	sentry.Flush(time.Second)

	got := tr.all()
	if len(got) != 2 {
		t.Fatalf("expected 2 captured events, got %d", len(got))
	}

	msgs := map[string]*sentry.Event{}
	for _, e := range got {
		msgs[e.Message] = e
	}
	if _, ok := msgs["boom"]; !ok {
		t.Errorf("Error log 'boom' was not captured")
	}
	if _, ok := msgs["benign warning"]; ok {
		t.Errorf("Warn log must not be captured")
	}
	if _, ok := msgs["already reported"]; ok {
		t.Errorf("SkipSentry log must not be captured")
	}
	if e, ok := msgs["with context"]; !ok {
		t.Errorf("context Error log was not captured")
	} else if e.Tags["request_id"] != "req-9" {
		t.Errorf("request_id tag = %q, want req-9", e.Tags["request_id"])
	}
}

// TestSentryBridgeSkipsClientCancel verifies that an Error log whose error only says the client went away (canceled
// request context, or Postgres canceling the statement for it) raises no Sentry event, while a timeout still does.
func TestSentryBridgeSkipsClientCancel(t *testing.T) {
	tr := &captureTransport{}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://test@test.ingest.sentry.io/1",
		Transport: tr,
	}); err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	t.Cleanup(func() { sentry.Flush(time.Second) })

	Setup("info", "json")

	zap.L().Error("query failed", zap.Error(context.Canceled))                                       // skipped
	zap.L().Error("wrapped", zap.Error(fmt.Errorf("fetch categories: %w", context.Canceled)))        // skipped
	zap.L().With(zap.Error(context.Canceled)).Error("logger field")                                  // skipped
	zap.L().Error("pg cancel", zap.Error(errors.New("pq: canceling statement due to user request"))) // skipped
	zap.L().Error("timeout", zap.Error(context.DeadlineExceeded))                                    // captured
	zap.L().Error("real failure", zap.Error(errors.New("relation does not exist")))                  // captured

	sentry.Flush(time.Second)

	got := map[string]bool{}
	for _, e := range tr.all() {
		got[e.Message] = true
	}
	for _, skipped := range []string{"query failed", "wrapped", "logger field", "pg cancel"} {
		if got[skipped] {
			t.Errorf("client cancel %q must not be captured", skipped)
		}
	}
	for _, captured := range []string{"timeout", "real failure"} {
		if !got[captured] {
			t.Errorf("%q was not captured", captured)
		}
	}
}
