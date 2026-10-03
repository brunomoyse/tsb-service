package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authorizeMock answers the authorize endpoint with a 302 to the login UI and
// counts every request it receives.
func authorizeMock(t *testing.T, hits *atomic.Int32, gotHost *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if gotHost != nil {
			gotHost.Store(r.Host)
		}
		if r.URL.Path != authorizePath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", "/ui/v2/login/login?authRequest=V2_123")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func postAuthorizeProxy(t *testing.T, authorizeURL string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"authorizeUrl": authorizeURL})
	require.NoError(t, err)
	w, c := ginContext("POST", "/auth/authorize-proxy", string(body))
	AuthorizeProxyHandler(c)
	return w
}

func TestAuthorizeProxyHandler_ValidIssuerURL(t *testing.T) {
	var hits atomic.Int32
	srv := authorizeMock(t, &hits, nil)
	Init(Config{ZitadelIssuer: srv.URL, ZitadelClientID: "test-client-id", ServicePAT: "test-pat"})

	w := postAuthorizeProxy(t, srv.URL+"/oauth/v2/authorize?client_id=test-client-id&response_type=code")

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "V2_123", resp["authRequestId"])
	assert.Equal(t, int32(1), hits.Load())
}

func TestAuthorizeProxyHandler_RejectsOtherTargets(t *testing.T) {
	var hits atomic.Int32
	srv := authorizeMock(t, &hits, nil)
	Init(Config{ZitadelIssuer: srv.URL, ZitadelClientID: "test-client-id", ServicePAT: "test-pat"})
	issuerHost := strings.TrimPrefix(srv.URL, "http://")

	cases := map[string]string{
		"other host":     "http://127.0.0.2:1/oauth/v2/authorize?x=1",
		"wrong path":     srv.URL + "/debug/pprof?x=1",
		"path prefix":    srv.URL + "/oauth/v2/authorize/../../admin",
		"userinfo":       "http://user:pass@" + issuerHost + "/oauth/v2/authorize",
		"other scheme":   "https://" + issuerHost + "/oauth/v2/authorize",
		"fragment":       srv.URL + "/oauth/v2/authorize#frag",
		"not a url":      "::not a url",
		"relative":       "/oauth/v2/authorize",
		"metadata ip":    "http://169.254.169.254/latest/meta-data/",
		"opaque scheme":  "mailto:someone@example.com",
		"host lookalike": "http://" + issuerHost + ".evil.test/oauth/v2/authorize",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			w := postAuthorizeProxy(t, target)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "invalid_authorize_url")
		})
	}
	assert.Equal(t, int32(0), hits.Load(), "no rejected URL may reach the network")
}

func TestAuthorizeProxyHandler_InternalURLKeepsIssuerHost(t *testing.T) {
	var hits atomic.Int32
	var gotHost atomic.Value
	internal := authorizeMock(t, &hits, &gotHost)
	Init(Config{
		ZitadelIssuer:      "https://auth.example.test",
		ZitadelInternalURL: internal.URL,
		ZitadelClientID:    "test-client-id",
		ServicePAT:         "test-pat",
	})

	w := postAuthorizeProxy(t, "https://auth.example.test/oauth/v2/authorize?client_id=test-client-id")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, "auth.example.test", gotHost.Load())

	// A caller-chosen host is refused before the rewrite, so it can never
	// become the Host header sent to the internal Zitadel.
	w = postAuthorizeProxy(t, "https://attacker.test/debug/pprof?x=1")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, int32(1), hits.Load())
}
