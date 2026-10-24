package hubrise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretNeverPrintsOrMarshals(t *testing.T) {
	s := secret("tok-123")
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		assert.Equal(t, "[REDACTED]", fmt.Sprintf(verb, s), verb)
	}
	assert.Equal(t, "[REDACTED]", s.String())
	assert.Equal(t, "[REDACTED]", s.GoString())
	j, err := json.Marshal(struct{ T secret }{s})
	require.NoError(t, err)
	assert.JSONEq(t, `{"T":"[REDACTED]"}`, string(j))
	txt, err := s.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, "[REDACTED]", string(txt))
}

// The defensive paths that cannot be reached through the public API: building a request from a
// call this package never makes.
func TestACallThatCannotBeBuiltIsAProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok", WithLimiter(NewLimiter(Limits{})))
	require.NoError(t, err)

	for name, cl := range map[string]call{
		"bad method":  {method: "BAD METHOD", path: "/x"},
		"bad escapes": {method: http.MethodGet, path: "/%zz"},
	} {
		_, err = c.do(context.Background(), cl)
		var he *Error
		require.ErrorAs(t, err, &he, name)
		assert.Equal(t, KindProtocol, he.Kind, name)
		assert.Equal(t, "build request", he.Message, name)
	}
}

func TestOAuthRequestThatCannotBeBuilt(t *testing.T) {
	o, err := NewOAuth("https://manager.hubrise.com/oauth2/v1", "cid", "csecret")
	require.NoError(t, err)
	//nolint:staticcheck // a nil context is the only way to make the request builder fail
	_, err = o.post(nil, "/token", nil)
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, KindProtocol, he.Kind)
}
