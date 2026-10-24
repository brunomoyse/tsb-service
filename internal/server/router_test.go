package server_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/server"
)

// oneMiB is written out here on purpose: the test must not follow a change of server.BodyLimit.
const oneMiB = 1 << 20

const (
	shopOrigin      = "https://shop.example.test"
	dashboardOrigin = "https://pili.example.test"
	extraOrigin     = "https://old-dashboard.example.test"
)

func init() { gin.SetMode(gin.TestMode) }

// stub is a handler that says which field of server.Handlers served the request, after reading the whole body: a body
// over the router's cap makes the read fail and the stub answer 413, like gqlgen and the real handlers do.
func stub(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
			c.String(http.StatusRequestEntityTooLarge, "too large")
			return
		}
		c.String(http.StatusOK, name)
	}
}

// strict is the StrictAuth stand-in: a request needs "Authorization: Bearer ok".
func strict(c *gin.Context) {
	if c.GetHeader("Authorization") != "Bearer ok" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.Next()
}

func handlers() server.Handlers {
	return server.Handlers{
		Health: func() error { return nil },
		DataLoader: func(c *gin.Context) {
			c.Header("X-Dataloader", "attached")
			c.Next()
		},
		OptionalAuth: func(c *gin.Context) {
			c.Header("X-Optional-Auth", "ran")
			c.Next()
		},
		StrictAuth: strict,
		GraphQL: func(c *gin.Context) {
			if c.GetHeader("X-Test-Panic") != "" {
				panic("boom")
			}
			stub("GraphQL")(c)
		},

		OTPRequest:         stub("OTPRequest"),
		OTPVerify:          stub("OTPVerify"),
		OTPResend:          stub("OTPResend"),
		OTPCompleteProfile: stub("OTPCompleteProfile"),
		ReviewLastOTP:      stub("ReviewLastOTP"),
		TOTPVerify:         stub("TOTPVerify"),
		Finalize:           stub("Finalize"),
		AuthorizeProxy:     stub("AuthorizeProxy"),
		TokenExchange:      stub("TokenExchange"),
		IdPStart:           stub("IdPStart"),
		IdPSession:         stub("IdPSession"),

		MFAStatus:      stub("MFAStatus"),
		MFAStartTOTP:   stub("MFAStartTOTP"),
		MFAConfirmTOTP: stub("MFAConfirmTOTP"),
		MFARemoveTOTP:  stub("MFARemoveTOTP"),

		POSDeviceLogin:    stub("POSDeviceLogin"),
		POSUpdateFCMToken: stub("POSUpdateFCMToken"),

		PaymentWebhook: stub("PaymentWebhook"),
		ImagePreview:   stub("ImagePreview"),
		OrderInvoice:   stub("OrderInvoice"),
		Feedback:       stub("Feedback"),
	}
}

func newRouter(t *testing.T, mutate ...func(*server.Handlers)) *gin.Engine {
	t.Helper()
	h := handlers()
	for _, m := range mutate {
		m(&h)
	}
	router, stop, err := server.New(server.Config{WebOrigins: []string{shopOrigin, dashboardOrigin, extraOrigin}}, h)
	require.NoError(t, err)
	t.Cleanup(stop)
	return router
}

type request struct {
	method  string
	path    string
	body    []byte
	headers map[string]string
}

func (r request) do(router http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(r.method, r.path, bytes.NewReader(r.body))
	req.RemoteAddr = "198.51.100.7:4242"
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

var nextIP atomic.Int32

// anotherClient returns headers that make the request come from a client no other request used: tests that are not
// about the rate limits must not eat each other's budget.
func anotherClient(headers map[string]string) map[string]string {
	out := map[string]string{"CF-Connecting-IP": fmt.Sprintf("192.0.2.%d", nextIP.Add(1)%250+1)}
	maps.Copy(out, headers)
	return out
}

func get(router http.Handler, path string) *httptest.ResponseRecorder {
	return request{method: http.MethodGet, path: path}.do(router)
}

// route is one entry of the documented API surface.
type route struct {
	method  string
	path    string
	handler string
	// strict: the route sits behind StrictAuth.
	strict bool
	// limited: the route shares a per-IP rate limit, with this burst (0 = no limit).
	burst int
}

// routes is the complete route table of tsb-service. A route added to or removed from the router without a line here
// fails TestRouteTable.
var routes = []route{
	{http.MethodGet, "/api/v1/up", "", false, 0},
	{http.MethodHead, "/api/v1/up", "", false, 0},
	{http.MethodPost, "/api/v1/graphql", "GraphQL", false, 0},
	{http.MethodGet, "/api/v1/graphql", "GraphQL", false, 0},

	{http.MethodPost, "/api/v1/auth/session/otp/request", "OTPRequest", false, authBurst},
	{http.MethodPost, "/api/v1/auth/session/otp/verify", "OTPVerify", false, authBurst},
	{http.MethodPost, "/api/v1/auth/session/otp/resend", "OTPResend", false, authBurst},
	{http.MethodPost, "/api/v1/auth/session/otp/complete-profile", "OTPCompleteProfile", false, authBurst},
	{http.MethodGet, "/api/v1/auth/review/last-otp", "ReviewLastOTP", false, authBurst},
	{http.MethodPost, "/api/v1/auth/session/totp/verify", "TOTPVerify", false, authBurst},
	{http.MethodPost, "/api/v1/auth/finalize", "Finalize", false, authBurst},
	{http.MethodPost, "/api/v1/auth/authorize-proxy", "AuthorizeProxy", false, authBurst},
	{http.MethodPost, "/api/v1/auth/token-exchange", "TokenExchange", false, authBurst},
	{http.MethodPost, "/api/v1/auth/idp/start", "IdPStart", false, authBurst},
	{http.MethodPost, "/api/v1/auth/idp/session", "IdPSession", false, authBurst},

	{http.MethodGet, "/api/v1/auth/mfa", "MFAStatus", true, 0},
	{http.MethodPost, "/api/v1/auth/mfa/totp", "MFAStartTOTP", true, authBurst},
	{http.MethodPost, "/api/v1/auth/mfa/totp/verify", "MFAConfirmTOTP", true, authBurst},
	{http.MethodPost, "/api/v1/auth/mfa/totp/remove", "MFARemoveTOTP", true, authBurst},

	{http.MethodPost, "/api/v1/pos/auth/device-login", "POSDeviceLogin", false, posBurst},
	{http.MethodPatch, "/api/v1/pos/devices/fcm-token", "POSUpdateFCMToken", false, posBurst},

	{http.MethodPost, "/api/v1/payments/webhook", "PaymentWebhook", false, webhookBurst},
	{http.MethodPost, "/api/v1/images/preview", "ImagePreview", true, 0},
	{http.MethodGet, "/api/v1/orders/:id/invoice", "OrderInvoice", true, 0},
	{http.MethodPost, "/api/v1/feedback", "Feedback", false, feedbackBurst},

	{http.MethodGet, "/metrics", "", false, 0},
	// The catch-all preflight route registered before the CORS-protected ones.
	{http.MethodOptions, "/*any", "", false, 0},
}

// The bursts of the per-IP limiters, as documented next to them in router.go.
const (
	authBurst     = 10
	posBurst      = 10
	webhookBurst  = 10
	feedbackBurst = 2
)

func concrete(path string) string {
	return strings.NewReplacer(":id", "0a0b0c0d", "/*any", "/anything").Replace(path)
}

func TestRouteTable(t *testing.T) {
	router := newRouter(t)

	var got, want []string
	for _, r := range router.Routes() {
		got = append(got, r.Method+" "+r.Path)
	}
	for _, r := range routes {
		want = append(want, r.method+" "+r.path)
	}
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got, "the router serves exactly the documented routes")
}

func TestEveryRouteReachesItsOwnHandler(t *testing.T) {
	router := newRouter(t)
	for _, r := range routes {
		if r.handler == "" {
			continue
		}
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			rec := request{method: r.method, path: concrete(r.path), headers: anotherClient(map[string]string{"Authorization": "Bearer ok"})}.do(router)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, r.handler, rec.Body.String(), "the route is wired to another handler")
		})
	}
}

func TestStrictRoutesNeedAToken(t *testing.T) {
	router := newRouter(t)
	for _, r := range routes {
		if r.handler == "" {
			continue
		}
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			rec := request{method: r.method, path: concrete(r.path), headers: anotherClient(nil)}.do(router)
			if r.strict {
				assert.Equal(t, http.StatusUnauthorized, rec.Code, "a protected route answered an anonymous request")
			} else {
				assert.Equal(t, http.StatusOK, rec.Code, "a public route asked for a token")
			}
		})
	}
}

func TestGraphQLRunsOptionalAuthAndEveryAPIRouteGetsTheDataLoaders(t *testing.T) {
	router := newRouter(t)

	rec := request{method: http.MethodPost, path: "/api/v1/graphql"}.do(router)
	assert.Equal(t, "ran", rec.Header().Get("X-Optional-Auth"))
	assert.Equal(t, "attached", rec.Header().Get("X-Dataloader"))

	rec = request{method: http.MethodPost, path: "/api/v1/feedback"}.do(router)
	assert.Equal(t, "attached", rec.Header().Get("X-Dataloader"))
	assert.Empty(t, rec.Header().Get("X-Optional-Auth"), "only GraphQL decodes an optional token")

	assert.Empty(t, get(router, "/metrics").Header().Get("X-Dataloader"), "/metrics is outside the API group")
}

func TestHealthCheck(t *testing.T) {
	t.Run("the database answers", func(t *testing.T) {
		router := newRouter(t)
		rec := get(router, "/api/v1/up")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
		assert.Equal(t, http.StatusOK, request{method: http.MethodHead, path: "/api/v1/up"}.do(router).Code)
	})
	t.Run("the database does not answer", func(t *testing.T) {
		router := newRouter(t, func(h *server.Handlers) { h.Health = func() error { return errors.New("down") } })
		rec := get(router, "/api/v1/up")
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.JSONEq(t, `{"status":"unhealthy","db":"unreachable"}`, rec.Body.String())
		assert.Equal(t, http.StatusServiceUnavailable, request{method: http.MethodHead, path: "/api/v1/up"}.do(router).Code)
	})
}

func TestMetricsEndpoint(t *testing.T) {
	router := newRouter(t)
	get(router, "/api/v1/up") // at least one counted request
	rec := get(router, "/metrics")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "http_requests_total")
}

func TestNewRefusesMissingHandlers(t *testing.T) {
	_, _, err := server.New(server.Config{}, server.Handlers{Health: func() error { return nil }, GraphQL: stub("GraphQL")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DataLoader")
	assert.Contains(t, err.Error(), "Feedback")
	assert.NotContains(t, err.Error(), "GraphQL")
	assert.NotContains(t, err.Error(), "Health")
}

// ---- body size limits ----

func TestBodyLimit(t *testing.T) {
	router := newRouter(t)
	under := bytes.Repeat([]byte("a"), oneMiB-1)
	over := bytes.Repeat([]byte("a"), oneMiB+1)
	json := map[string]string{"Content-Type": "application/json"}
	multipart := map[string]string{"Content-Type": "multipart/form-data; boundary=xyz"}

	cases := []struct {
		name    string
		method  string
		path    string
		body    []byte
		headers map[string]string
		want    int
	}{
		{"graphql json under the cap", http.MethodPost, server.GraphQLPath, under, json, http.StatusOK},
		{"graphql json over the cap", http.MethodPost, server.GraphQLPath, over, json, http.StatusRequestEntityTooLarge},
		{"graphql json without content type over the cap", http.MethodPost, server.GraphQLPath, over, nil, http.StatusRequestEntityTooLarge},
		{"graphql multipart upload over the cap", http.MethodPost, server.GraphQLPath, bytes.Repeat([]byte("a"), 5<<20), multipart, http.StatusOK},
		{"graphql multipart in another case", http.MethodPost, server.GraphQLPath, bytes.Repeat([]byte("a"), 5<<20), map[string]string{"Content-Type": "Multipart/Form-Data; boundary=xyz"}, http.StatusOK},
		{"image preview over the cap", http.MethodPost, server.ImagePreviewPath, bytes.Repeat([]byte("a"), 5<<20), map[string]string{"Authorization": "Bearer ok"}, http.StatusOK},
		{"multipart elsewhere is capped", http.MethodPost, "/api/v1/feedback", over, multipart, http.StatusRequestEntityTooLarge},
		{"auth endpoint over the cap", http.MethodPost, "/api/v1/auth/session/otp/request", over, json, http.StatusRequestEntityTooLarge},
		{"payment webhook over the cap", http.MethodPost, "/api/v1/payments/webhook", over, nil, http.StatusRequestEntityTooLarge},
		{"feedback under the cap", http.MethodPost, "/api/v1/feedback", under, json, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh router per case: the feedback limiter allows two requests per IP.
			router := router
			if strings.Contains(tc.path, "feedback") {
				router = newRouter(t)
			}
			rec := request{method: tc.method, path: tc.path, body: tc.body, headers: tc.headers}.do(router)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

// ---- security headers ----

func TestSecurityHeadersOnEveryAnswer(t *testing.T) {
	router := newRouter(t)
	answers := map[string]*httptest.ResponseRecorder{
		"ok":            get(router, "/api/v1/up"),
		"post":          request{method: http.MethodPost, path: "/api/v1/graphql"}.do(router),
		"unauthorized":  request{method: http.MethodGet, path: "/api/v1/auth/mfa"}.do(router),
		"unknown route": get(router, "/api/v1/nothing-here"),
		"metrics":       get(router, "/metrics"),
	}
	// A 429 too.
	var limited *httptest.ResponseRecorder
	for range feedbackBurst + 1 {
		limited = request{method: http.MethodPost, path: "/api/v1/feedback"}.do(router)
	}
	answers["rate limited"] = limited
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Equal(t, http.StatusNotFound, answers["unknown route"].Code)

	for name, rec := range answers {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
			assert.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))
			assert.NotEmpty(t, rec.Header().Get("X-Request-ID"), "every answer carries a request id")
		})
	}
}

// ---- CORS ----

func preflight(router http.Handler, origin, method string) *httptest.ResponseRecorder {
	return request{method: http.MethodOptions, path: server.GraphQLPath, headers: map[string]string{
		"Origin":                         origin,
		"Access-Control-Request-Method":  method,
		"Access-Control-Request-Headers": "authorization,content-type,accept-language",
	}}.do(router)
}

func TestCORSAllowsTheKnownOrigins(t *testing.T) {
	router := newRouter(t)
	allowed := []string{shopOrigin, dashboardOrigin, extraOrigin, "capacitor://localhost", "http://localhost", "https://localhost"}
	for _, origin := range allowed {
		t.Run(origin, func(t *testing.T) {
			rec := preflight(router, origin, http.MethodPost)
			assert.Less(t, rec.Code, 300)
			assert.Equal(t, origin, rec.Header().Get("Access-Control-Allow-Origin"), "the origin is echoed, never a wildcard")
			assert.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
			assert.Equal(t, "43200", rec.Header().Get("Access-Control-Max-Age"))
			allowMethods := rec.Header().Get("Access-Control-Allow-Methods")
			for _, m := range []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"} {
				assert.Contains(t, allowMethods, m)
			}
			allowHeaders := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
			for _, h := range []string{"authorization", "content-type", "accept-language"} {
				assert.Contains(t, allowHeaders, h)
			}
		})
	}
}

func TestCORSRefusesOtherOrigins(t *testing.T) {
	router := newRouter(t)
	for _, origin := range []string{"https://evil.example.test", "http://shop.example.test", shopOrigin + ".evil.test", "null"} {
		t.Run(origin, func(t *testing.T) {
			rec := preflight(router, origin, http.MethodPost)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))

			rec = request{method: http.MethodPost, path: server.GraphQLPath, headers: map[string]string{"Origin": origin}}.do(router)
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"))
		})
	}
}

func TestCORSExposesTheDownloadHeaders(t *testing.T) {
	router := newRouter(t)
	rec := request{method: http.MethodGet, path: "/api/v1/orders/1/invoice", headers: map[string]string{
		"Origin": dashboardOrigin, "Authorization": "Bearer ok",
	}}.do(router)
	assert.Equal(t, dashboardOrigin, rec.Header().Get("Access-Control-Allow-Origin"))
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	for _, h := range []string{"Content-Disposition", "Content-Length", "X-Original-Width", "X-Trim-Applied"} {
		assert.Contains(t, exposed, h)
	}
}

// ---- rate limits ----

func TestRateLimitedRoutes(t *testing.T) {
	for _, r := range routes {
		if r.burst == 0 {
			continue
		}
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			router := newRouter(t)
			send := func(headers map[string]string) int {
				if r.strict {
					headers["Authorization"] = "Bearer ok"
				}
				return request{method: r.method, path: concrete(r.path), headers: headers}.do(router).Code
			}
			for i := range r.burst {
				require.Equal(t, http.StatusOK, send(map[string]string{}), "request %d is inside the burst", i+1)
			}
			assert.Equal(t, http.StatusTooManyRequests, send(map[string]string{}), "the request after the burst is refused")
			assert.Equal(t, http.StatusOK, send(map[string]string{"CF-Connecting-IP": "203.0.113.9"}), "another client is not affected")
		})
	}
}

func TestRateLimitCannotBeBypassedWithForwardedFor(t *testing.T) {
	router := newRouter(t)
	var last int
	for i := range feedbackBurst + 3 {
		last = request{method: http.MethodPost, path: "/api/v1/feedback", headers: map[string]string{
			"X-Forwarded-For": fmt.Sprintf("10.0.0.%d", i+1),
			"X-Real-IP":       fmt.Sprintf("10.0.1.%d", i+1),
		}}.do(router).Code
	}
	assert.Equal(t, http.StatusTooManyRequests, last, "a client-supplied forwarding header must not pick the rate limit bucket")
}

func TestRateLimitUsesTheCloudflareClientIP(t *testing.T) {
	router := newRouter(t)
	for range feedbackBurst {
		require.Equal(t, http.StatusOK, request{method: http.MethodPost, path: "/api/v1/feedback", headers: map[string]string{"CF-Connecting-IP": "203.0.113.1"}}.do(router).Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, request{method: http.MethodPost, path: "/api/v1/feedback", headers: map[string]string{"CF-Connecting-IP": "203.0.113.1"}}.do(router).Code)
	assert.Equal(t, http.StatusOK, request{method: http.MethodPost, path: "/api/v1/feedback", headers: map[string]string{"CF-Connecting-IP": "203.0.113.2"}}.do(router).Code)
}

func TestAuthEndpointsShareOneBucketPerIP(t *testing.T) {
	router := newRouter(t)
	authRoutes := slices.DeleteFunc(slices.Clone(routes), func(r route) bool { return r.burst != authBurst || r.strict })
	require.GreaterOrEqual(t, len(authRoutes), authBurst, "the shared burst is only visible with enough auth routes")
	for i := range authBurst {
		r := authRoutes[i]
		require.Equal(t, http.StatusOK, request{method: r.method, path: r.path}.do(router).Code, r.path)
	}
	r := authRoutes[authBurst]
	assert.Equal(t, http.StatusTooManyRequests, request{method: r.method, path: r.path}.do(router).Code,
		"login, finalize and token exchange draw on the same per-IP budget")
}

func TestUnlimitedRoutes(t *testing.T) {
	router := newRouter(t)
	for _, r := range routes {
		if r.burst != 0 || r.handler == "" {
			continue
		}
		for range 60 {
			rec := request{method: r.method, path: concrete(r.path), headers: map[string]string{"Authorization": "Bearer ok"}}.do(router)
			require.Equal(t, http.StatusOK, rec.Code, r.method+" "+r.path)
		}
	}
}

// ---- panics and redirects ----

func TestAPanickingHandlerAnswers500AndTheServerKeepsServing(t *testing.T) {
	router := newRouter(t)
	rec := request{method: http.MethodPost, path: server.GraphQLPath, headers: map[string]string{"X-Test-Panic": "1"}}.do(router)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, http.StatusOK, get(router, "/api/v1/up").Code)
}

func TestTrailingSlashRedirects(t *testing.T) {
	router := newRouter(t)
	rec := get(router, "/api/v1/up/")
	assert.Equal(t, http.StatusMovedPermanently, rec.Code)
	assert.Equal(t, "/api/v1/up", rec.Header().Get("Location"))
}
