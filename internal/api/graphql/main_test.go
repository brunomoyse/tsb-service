package graphql_test

import (
	"fmt"
	"os"
	"testing"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/email/scaleway/scalewaytest"
	"tsb-service/pkg/email/smtptest"
)

// TestMain installs the fake SMTP server as the e-mail backend for the whole package, before any
// test runs, and removes the shared PostgreSQL container at the end. The backend is process-wide state; installing it here (and not lazily in the first
// test that needs it) makes every test behave the same with -run, -shuffle or a different order.
func TestMain(m *testing.M) {
	srv, err := smtptest.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake SMTP server:", err)
		os.Exit(1)
	}
	restore, err := scalewaytest.UseInMain(srv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e-mail backend:", err)
		os.Exit(1)
	}
	sharedSMTP = srv
	code := testhelpers.Main(m)
	restore()
	srv.Close()
	os.Exit(code)
}
