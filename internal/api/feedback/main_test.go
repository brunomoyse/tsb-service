package feedback

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// TestMain makes any HTTP request that a test did not fake fail loudly. The handler calls
// Cloudflare Turnstile through http.DefaultTransport; a test that sets TURNSTILE_SECRET_KEY without
// fakeTurnstile would otherwise reach the real service. fakeTurnstile replaces this transport for
// the duration of a test and puts it back.
func TestMain(m *testing.M) {
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected real HTTP request to %s: the test must fake it (fakeTurnstile)", r.URL)
	})
	os.Exit(m.Run())
}
