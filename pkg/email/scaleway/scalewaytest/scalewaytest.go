// Package scalewaytest points the process-wide e-mail backend of pkg/email/scaleway at a fake SMTP
// server for the duration of one test and puts the previous backend back afterwards, so tests
// neither depend on nor leak the backend to the tests that run before or after them.
package scalewaytest

import (
	"os"
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

// UseInMain is Use for a whole test binary, called from TestMain (before m.Run) where no *testing.T
// exists: the backend stays on the server for every test of the package, so no test depends on
// which test happened to configure it first. Call the returned function after m.Run.
func UseInMain(s *smtptest.Server) (restore func(), err error) {
	restoreBackend := es.SaveBackend()
	set := map[string]string{
		"SMTP_HOST": s.Host(), "SMTP_PORT": s.Port(), "SMTP_USER": "", "SMTP_PASSWORD": "",
		"SCW_SENDER_EMAIL": "noreply@example.test", "SCW_SENDER_NAME": "Test shop",
	}
	prev := map[string]*string{}
	for k, v := range set {
		if old, ok := os.LookupEnv(k); ok {
			prev[k] = &old
		} else {
			prev[k] = nil
		}
		if err := os.Setenv(k, v); err != nil {
			return nil, err
		}
	}
	if err := es.InitService(); err != nil {
		return nil, err
	}
	return func() {
		for k, old := range prev {
			if old == nil {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, *old)
			}
		}
		restoreBackend()
	}, nil
}
