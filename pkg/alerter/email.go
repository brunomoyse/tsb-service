package alerter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"tsb-service/pkg/email/scaleway"
)

// EmailAlerter delivers alerts as Scaleway TEM emails, with an
// in-memory dedup cache so a flapping HubRise outage doesn't trigger
// dozens of identical emails in the same 10-minute window.
//
// Dedup key is `severity::title` — the body is free to vary without
// triggering a new email, which means a stream of "Circuit breaker
// opened" alerts with slightly different error strings is coalesced
// into a single notification per window.
type EmailAlerter struct {
	recipients []string
	dedupTTL   time.Duration

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewEmailAlerter constructs an EmailAlerter. `recipients` is a
// comma-separated list already split into individual addresses.
// `dedupTTL` is the minimum time between identical alerts.
func NewEmailAlerter(recipients []string, dedupTTL time.Duration) *EmailAlerter {
	cleaned := make([]string, 0, len(recipients))
	for _, r := range recipients {
		if trimmed := strings.TrimSpace(r); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return &EmailAlerter{
		recipients: cleaned,
		dedupTTL:   dedupTTL,
		seen:       make(map[string]time.Time),
	}
}

// Alert implements Alerter. Silently drops if no recipients are
// configured (degrading gracefully to NoopAlerter semantics).
func (a *EmailAlerter) Alert(_ context.Context, severity Severity, title, body string) error {
	if len(a.recipients) == 0 {
		return nil
	}
	if !a.shouldSend(severity, title) {
		return nil
	}
	subject := fmt.Sprintf("[%s] Tokyo Sushi Bar — %s", strings.ToUpper(string(severity)), title)
	return scaleway.SendAlertEmail(a.recipients, subject, body)
}

// shouldSend is thread-safe and returns true if enough time has
// passed since the last alert with the same severity+title. Expired
// entries are pruned lazily on each call.
func (a *EmailAlerter) shouldSend(severity Severity, title string) bool {
	key := string(severity) + "::" + title
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	if last, ok := a.seen[key]; ok && now.Sub(last) < a.dedupTTL {
		return false
	}

	// Opportunistic cleanup — avoid unbounded growth.
	for k, t := range a.seen {
		if now.Sub(t) > a.dedupTTL*2 {
			delete(a.seen, k)
		}
	}

	a.seen[key] = now
	return true
}
