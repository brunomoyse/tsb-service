// Package scalewaytest points the process-wide e-mail backend of pkg/email/scaleway at a fake SMTP
// server for the duration of one test and puts the previous backend back afterwards, so tests
// neither depend on nor leak the backend to the tests that run before or after them.
package scalewaytest

import (
	"testing"

	"github.com/stretchr/testify/require"

	es "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/email/smtptest"
)

// Use routes every e-mail sent during the test to the server (what InitService does when SMTP_HOST
// is set) with the sender noreply@example.test, and restores the previous backend when the test
// ends. Do not use it in parallel tests: the backend is process-wide.
func Use(t *testing.T, s *smtptest.Server) {
	t.Helper()
	use(t, s.Host(), s.Port())
}

// UseDead routes e-mail to an address nothing listens on, so every send fails (connection
// refused), and restores the previous backend when the test ends.
func UseDead(t *testing.T) {
	t.Helper()
	use(t, "127.0.0.1", "1")
}

func use(t *testing.T, host, port string) {
	t.Helper()
	t.Cleanup(es.SaveBackend())
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_USER", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("SCW_SENDER_EMAIL", "noreply@example.test")
	t.Setenv("SCW_SENDER_NAME", "Test")
	require.NoError(t, es.InitService())
}
