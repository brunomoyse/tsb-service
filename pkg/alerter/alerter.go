// Package alerter defines a minimal interface for sending operational
// alerts out of the application. Implementations currently include a
// no-op alerter (used when alerting is disabled) and an email alerter
// (Phase B, see email.go) that reuses the Scaleway TEM client.
//
// Severity levels are intentionally simple — the granularity is enough
// for subject-line routing and optional Discord mirroring later.
package alerter

import "context"

// Severity categorises an alert for downstream filtering.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Alerter is the interface the rest of tsb-service depends on. Real
// code should accept `alerter.Alerter`, not a concrete type, so a
// NoopAlerter can be substituted in tests and in environments where
// no recipients are configured.
type Alerter interface {
	Alert(ctx context.Context, severity Severity, title, body string) error
}

// NoopAlerter is the zero-config implementation — it silently drops
// every alert. It's the safe default when HUBRISE_ALERT_ENABLED is
// unset or false.
type NoopAlerter struct{}

// Alert implements Alerter for NoopAlerter.
func (NoopAlerter) Alert(_ context.Context, _ Severity, _, _ string) error {
	return nil
}
