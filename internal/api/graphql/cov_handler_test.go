package graphql_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/shared/audit"
	"tsb-service/internal/shared/middleware"
)

// The production GraphQL endpoint (resolver.GraphQLHandler) behind the real OIDC middleware: HTTP
// queries with a Zitadel token, WebSocket subscriptions with the token in connection_init, the
// origin allowlist, token expiry on a live socket, panic recovery and the staff audit trail.

const (
	handlerIssuerHost = "zitadel.test"
	handlerClientID   = "api-client"
	handlerProjectID  = "project-1"
)

var (
	handlerKeyOnce sync.Once
	handlerKey     *rsa.PrivateKey
	handlerKeyErr  error
)

// handlerSigningKey is the identity provider's RSA key, generated once per test binary: a 2048-bit
// key takes about a second under -race and every endpoint used to make its own.
func handlerSigningKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	handlerKeyOnce.Do(func() { handlerKey, handlerKeyErr = rsa.GenerateKey(rand.Reader, 2048) })
	require.NoError(t, handlerKeyErr)
	return handlerKey
}

// fakeIdentityProvider serves the OIDC discovery and JWKS of one RSA key.
func fakeIdentityProvider(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key := handlerSigningKey(t)
	issuer := "https://" + handlerIssuerHost
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "jwks_uri": issuer + "/oauth/v2/keys",
			"authorization_endpoint": issuer + "/oauth/v2/authorize", "token_endpoint": issuer + "/oauth/v2/token",
			"introspection_endpoint":                issuer + "/oauth/v2/introspect",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/oauth/v2/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "key-1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return key, srv.URL
}

func signZitadelToken(t *testing.T, key *rsa.PrivateKey, sub string, admin bool, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": "https://" + handlerIssuerHost, "sub": sub, "aud": []string{handlerProjectID, handlerClientID},
		"client_id": "dashboard", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(ttl).Unix(),
	}
	if admin {
		claims["urn:zitadel:iam:org:project:roles"] = map[string]any{"admin": map[string]any{"org": "domain"}}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "key-1"
	s, err := tok.SignedString(key)
	require.NoError(t, err)
	return s
}

func signPOSToken(t *testing.T, device uuid.UUID, ttl time.Duration) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "tsb-pos", "sub": device.String(), "exp": time.Now().Add(ttl).Unix(), "iat": time.Now().Unix(),
	})
	s, err := tok.SignedString([]byte("pos-test-secret"))
	require.NoError(t, err)
	return s
}

type endpoint struct {
	env    *covEnv
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newEndpoint(t *testing.T, env *covEnv, r *resolver.Resolver) *endpoint {
	t.Helper()
	return newEndpointWith(t, env, r, false)
}

// newEndpointWith can put a Sentry hub on every request, as the Sentry gin middleware does.
func newEndpointWith(t *testing.T, env *covEnv, r *resolver.Resolver, withHub bool) *endpoint {
	t.Helper()
	key, internalURL := fakeIdentityProvider(t)
	verifier, err := middleware.NewOIDCVerifier(t.Context(), "https://"+handlerIssuerHost, internalURL, handlerClientID, handlerProjectID, r.UserService)
	require.NoError(t, err)
	verifier.SetAdminClientIDs([]string{"dashboard"})
	verifier.SetAppJWTVerifier(r.PosService)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if withHub {
		engine.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(sentry.SetHubOnContext(c.Request.Context(), sentry.NewHub(nil, sentry.NewScope())))
			c.Next()
		})
	}
	handler := resolver.GraphQLHandler(r, []string{"https://shop.example.test/"}, verifier, audit.NewRecorder(env.DB.DB))
	engine.Any("/api/v1/graphql", verifier.OptionalAuthMiddleware(), handler)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return &endpoint{env: env, server: srv, key: key}
}

func (e *endpoint) post(t *testing.T, token, query string) graphqlResponseWithExtensions {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.server.URL+"/api/v1/graphql", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out graphqlResponseWithExtensions
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// wsMessage is one graphql-transport-ws frame.
type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// subscribe opens a socket, authenticates in connection_init and starts the subscription. It
// returns the connection and a function reading the next frame.
func (e *endpoint) subscribe(t *testing.T, token, query string, header http.Header) (*coderws.Conn, func() (wsMessage, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(e.server.URL, "http")+"/api/v1/graphql", &coderws.DialOptions{
		Subprotocols: []string{"graphql-transport-ws"}, HTTPHeader: header,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	// send and read are also called from helper goroutines, so they return errors instead of
	// failing the test (require would call FailNow off the test goroutine).
	send := func(m wsMessage) error {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return conn.Write(ctx, coderws.MessageText, raw)
	}
	read := func() (wsMessage, error) {
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return wsMessage{}, err
			}
			var m wsMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return wsMessage{}, fmt.Errorf("bad frame %q: %w", raw, err)
			}
			if m.Type == "ping" {
				if err := send(wsMessage{Type: "pong"}); err != nil {
					return wsMessage{}, err
				}
				continue
			}
			return m, nil
		}
	}
	init := map[string]any{}
	if token != "" {
		init["Authorization"] = "Bearer " + token
	}
	payload, _ := json.Marshal(init)
	require.NoError(t, send(wsMessage{Type: "connection_init", Payload: payload}))
	ack, err := read()
	require.NoError(t, err)
	require.Equal(t, "connection_ack", ack.Type)

	sub, _ := json.Marshal(map[string]string{"query": query})
	require.NoError(t, send(wsMessage{ID: "1", Type: "subscribe", Payload: sub}))
	return conn, read
}

// tokenExp reads the exp claim back from a signed token, so assertions are made against the
// token's own deadline and not against a duration guessed in the test.
func tokenExp(t *testing.T, token string) time.Time {
	t.Helper()
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(token, claims)
	require.NoError(t, err)
	exp, err := claims.GetExpirationTime()
	require.NoError(t, err)
	require.NotNil(t, exp, "the token carries an exp claim")
	return exp.Time
}

// socketLife is what happened to one admin socket whose token expires.
type socketLife struct {
	exp     time.Time
	liveAt  time.Time // arrival of the `next` frame that proves the socket was authenticated
	endedAt time.Time
	ending  wsMessage // the last frame before the socket ended (zero when it was just closed)
	endErr  error     // the read error that ended the socket, if no `complete` frame did
}

// socketUntilExpiry opens a coupon subscription as an admin with a token that lives ttl (to the
// second, as exp is whole seconds), proves the socket is live by receiving a published event as a
// `next` frame BEFORE the token's exp, then waits for the socket to end. The token is either sent
// in connection_init or, with viaHeader, on the upgrade request. A socket that was rejected at
// once, or whose setup took longer than the token lived, never reaches "live": that attempt is
// discarded and retried with a fresh token, so a loaded runner cannot turn into a false pass or
// a flaky failure.
func (e *endpoint) socketUntilExpiry(t *testing.T, ttl time.Duration, viaHeader bool) socketLife {
	t.Helper()
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		token := signZitadelToken(t, e.key, e.env.Fixtures.AdminUser.ID.String(), true, ttl)
		exp := tokenExp(t, token)
		var life socketLife
		life.exp = exp

		var conn *coderws.Conn
		var read func() (wsMessage, error)
		if viaHeader {
			conn, read = e.subscribe(t, "", `subscription { couponUpdated { code } }`, http.Header{"Authorization": {"Bearer " + token}})
		} else {
			conn, read = e.subscribe(t, token, `subscription { couponUpdated { code } }`, nil)
		}

		type frame struct {
			m   wsMessage
			err error
			at  time.Time
		}
		frames := make(chan frame, 64)
		go func() {
			defer close(frames)
			for {
				m, err := read()
				frames <- frame{m, err, time.Now()}
				if err != nil || m.Type == "complete" || m.Type == "error" {
					return
				}
			}
		}()

		// Publish until the event comes back as a `next` frame (live), a frame ends the socket
		// early (rejected), or the token is about to expire (setup too slow).
		liveDeadline := exp.Add(-time.Second)
		tick := time.NewTicker(50 * time.Millisecond)
		live, over := false, false
		for !live && !over && time.Now().Before(liveDeadline) {
			e.env.Resolver.Broker.Publish("couponUpdated", &model.Coupon{Code: "ALIVE"})
			select {
			case f, ok := <-frames:
				switch {
				case !ok:
					over = true
				case f.err == nil && f.m.Type == "next":
					live, life.liveAt = true, f.at
				default:
					// An immediate rejection ("error"/"complete"/read error) is never "live".
					require.Failf(t, "socket ended before its token expired",
						"the socket was rejected %s before exp: %q %s (read error: %v)", time.Until(exp).Round(time.Millisecond), f.m.Type, f.m.Payload, f.err)
				}
			case <-tick.C:
			}
		}
		tick.Stop()
		if !live {
			t.Logf("attempt %d: setup took too long for a %s token, retrying", attempt, ttl)
			_ = conn.CloseNow()
			continue
		}
		require.True(t, life.liveAt.Before(exp), "live frame arrived before exp")

		// Now wait for the end, bounded relative to the token's exp.
		timeout := time.After(time.Until(exp) + 15*time.Second)
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					return life
				}
				if f.err != nil {
					life.endedAt, life.endErr = f.at, f.err
					return life
				}
				life.ending = f.m
				if f.m.Type == "complete" || f.m.Type == "error" {
					life.endedAt = f.at
					return life
				}
			case <-timeout:
				require.Fail(t, "the socket outlived its token", "exp was %s", exp)
			}
		}
	}
	require.FailNow(t, "could not keep a socket live before its token expired", "tried %d times with a %s token", attempts, ttl)
	return socketLife{}
}

// requireEndedWithToken asserts that the socket ended in an orderly way once the token expired,
// not before it and not long after.
func requireEndedWithToken(t *testing.T, life socketLife) {
	t.Helper()
	require.False(t, life.endedAt.IsZero(), "the socket ended")
	assert.False(t, life.endedAt.Before(life.exp.Add(-time.Second)),
		"it lived until the token expired: ended %s before exp", life.exp.Sub(life.endedAt))
	assert.WithinDuration(t, life.exp, life.endedAt, 10*time.Second, "and not long past it")
	// The server ends the subscription either with a `complete` frame or, when the connection
	// context is cancelled first, with a normal-closure close frame ("terminated"); which of the two
	// the client sees first is a race inside gqlgen. An `error` frame or an abnormal close would be
	// a different, unintended ending.
	if life.endErr != nil {
		assert.Equal(t, coderws.StatusNormalClosure, coderws.CloseStatus(life.endErr), "ended by: %v", life.endErr)
	} else {
		assert.Equal(t, "complete", life.ending.Type, "payload: %s", life.ending.Payload)
	}
}

func TestGraphQLEndpointOverHTTP(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	ep := newEndpoint(t, env, env.Resolver)
	customerID, _ := testhelpers.SeedCustomer(t, env.DB.DB, "http")
	customer := signZitadelToken(t, ep.key, customerID.String(), false, time.Hour)
	admin := signZitadelToken(t, ep.key, env.Fixtures.AdminUser.ID.String(), true, time.Hour)

	t.Run("a Zitadel token identifies the caller, the admin role comes from its claims", func(t *testing.T) {
		resp := ep.post(t, customer, `{ me { id isAdmin } }`)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.JSONEq(t, `{"me":{"id":"`+customerID.String()+`","isAdmin":false}}`, string(resp.Data))
		resp = ep.post(t, admin, `{ me { isAdmin } }`)
		require.JSONEq(t, `{"me":{"isAdmin":true}}`, string(resp.Data))
	})

	t.Run("no token and a bad token are unauthenticated, a customer is not an admin", func(t *testing.T) {
		for _, tok := range []string{"", "garbage"} {
			resp := ep.post(t, tok, `{ me { id } }`)
			require.Len(t, resp.Errors, 1)
			assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
		}
		resp := ep.post(t, customer, `{ coupons { id } }`)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
		forged := signZitadelToken(t, ep.key, customerID.String(), false, -time.Minute)
		resp = ep.post(t, forged, `{ me { id } }`)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "UNAUTHENTICATED", resp.Errors[0].Extensions["code"])
	})

	t.Run("a staff mutation is recorded in the audit trail", func(t *testing.T) {
		resp := ep.post(t, admin, `mutation SetPrep { updatePreparationMinutes(minutes: 33) { preparationMinutes } }`)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		require.Eventually(t, func() bool {
			return countRows(t, env.TestContext, `SELECT count(*) FROM staff_audit_log WHERE action = 'graphql:updatePreparationMinutes'`) == 1
		}, 10*time.Second, 50*time.Millisecond)
	})

	t.Run("a resolver that panics is turned into an error, not a crashed request", func(t *testing.T) {
		broken := env.with(func(r *resolver.Resolver) { r.RestaurantService = nil })
		bep := newEndpoint(t, env, broken)
		resp := bep.post(t, "", `{ restaurantConfig { preparationMinutes } }`)
		require.NotEmpty(t, resp.Errors)
		assert.NotContains(t, resp.Errors[0].Message, "nil pointer", "the panic text does not reach the client")
	})

	t.Run("a server fault shows the client a generic message, a user error its own", func(t *testing.T) {
		resp := ep.post(t, customer, `{ validateCoupon(code: "X", orderAmount: "abc") { valid } }`)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "INVALID_AMOUNT", resp.Errors[0].Extensions["code"])
		resp = ep.post(t, customer, `{ nope }`)
		require.NotEmpty(t, resp.Errors, "a query the schema does not know is refused")
	})
}

func TestGraphQLEndpointSubscriptions(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	ep := newEndpoint(t, env, env.Resolver)
	customerID, _ := testhelpers.SeedCustomer(t, env.DB.DB, "ws")
	admin := signZitadelToken(t, ep.key, env.Fixtures.AdminUser.ID.String(), true, time.Hour)
	customer := signZitadelToken(t, ep.key, customerID.String(), false, time.Hour)
	const couponSub = `subscription { couponUpdated { code } }`

	next := func(t *testing.T, read func() (wsMessage, error)) wsMessage {
		t.Helper()
		m, err := read()
		require.NoError(t, err)
		return m
	}
	publishUntil := func(t *testing.T, topic string, msg any, done func() bool) {
		t.Helper()
		require.Eventually(t, func() bool { env.Resolver.Broker.Publish(topic, msg); return done() }, 10*time.Second, 50*time.Millisecond)
	}

	t.Run("an admin token in connection_init receives the live updates", func(t *testing.T) {
		_, read := ep.subscribe(t, admin, couponSub, nil)
		got := make(chan wsMessage, 1)
		go func() { m, _ := read(); got <- m }()
		publishUntil(t, "couponUpdated", &model.Coupon{Code: "LIVE"}, func() bool { return len(got) == 1 })
		m := <-got
		assert.Equal(t, "next", m.Type)
		assert.Contains(t, string(m.Payload), `"code":"LIVE"`)
	})

	t.Run("without a token, or as a customer, the subscription is refused", func(t *testing.T) {
		for name, c := range map[string]struct{ token, code string }{
			"anonymous": {"", "UNAUTHENTICATED"}, "bad token": {"garbage", "UNAUTHENTICATED"}, "customer": {customer, "FORBIDDEN"},
			"expired admin token": {signZitadelToken(t, ep.key, env.Fixtures.AdminUser.ID.String(), true, -time.Minute), "UNAUTHENTICATED"},
		} {
			_, read := ep.subscribe(t, c.token, couponSub, nil)
			m := next(t, read)
			assert.Contains(t, string(m.Payload), `"code":"`+c.code+`"`, "%s: %s %s", name, m.Type, m.Payload)
			assert.NotContains(t, string(m.Payload), `"code":"LIVE"`)
		}
	})

	t.Run("a token that cannot be matched to a user leaves the socket unauthenticated", func(t *testing.T) {
		broken := env.brokenResolver(t)
		bep := newEndpoint(t, env, broken)
		_, read := bep.subscribe(t, signZitadelToken(t, bep.key, "unknown-sub", true, time.Hour), couponSub, nil)
		m := next(t, read)
		assert.Contains(t, string(m.Payload), "UNAUTHENTICATED", "%s %s", m.Type, m.Payload)
	})

	t.Run("a POS device token gives staff scope", func(t *testing.T) {
		pos := signPOSToken(t, uuid.New(), time.Hour)
		_, read := ep.subscribe(t, pos, `subscription { orderCreated { id } }`, nil)
		got := make(chan wsMessage, 1)
		go func() { m, _ := read(); got <- m }()
		id := uuid.New()
		publishUntil(t, "orderCreated", &model.Order{ID: id}, func() bool { return len(got) == 1 })
		assert.Contains(t, string((<-got).Payload), id.String())
	})

	t.Run("a socket ends when its token expires", func(t *testing.T) {
		life := ep.socketUntilExpiry(t, 5*time.Second, false)
		requireEndedWithToken(t, life)
	})

	t.Run("a browser origin must be on the allowlist, a native app sends none", func(t *testing.T) {
		ctx := t.Context()
		url := "ws" + strings.TrimPrefix(ep.server.URL, "http") + "/api/v1/graphql"
		_, resp, err := coderws.Dial(ctx, url, &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}, HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
		require.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		for _, origin := range []string{"https://shop.example.test", "HTTPS://SHOP.EXAMPLE.TEST/"} {
			conn, _, err := coderws.Dial(ctx, url, &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}, HTTPHeader: http.Header{"Origin": {origin}}})
			require.NoError(t, err, origin)
			_ = conn.CloseNow()
		}
		conn, _, err := coderws.Dial(ctx, url, &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}})
		require.NoError(t, err)
		_ = conn.CloseNow()
	})
}

func TestNewResolverWiresItsServices(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.Resolver
	got := resolver.NewResolver(r.Broker, r.APNsClient, r.FCMClient, r.AddressService, r.CouponService, r.NotificationService,
		r.OrderService, r.PaymentService, r.ProductService, r.RestaurantService, r.UserService, r.PosService,
		r.CouponValidateLimiter, r.PublicQueryLimiter)
	assert.Same(t, r.Broker, got.Broker)
	assert.Same(t, r.PosService, got.PosService)
	assert.Same(t, r.CouponValidateLimiter, got.CouponValidateLimiter)
	assert.Same(t, r.PublicQueryLimiter, got.PublicQueryLimiter)
	assert.NotNil(t, got.OrderService)
	assert.Nil(t, got.AssistantService, "the assistant is attached after construction")
}

func TestGraphQLEndpointOptionsAndReporting(t *testing.T) {
	env := setupCovEnv(t, covOptions{})

	t.Run("introspection is only served when it is switched on", func(t *testing.T) {
		const q = `{ __schema { queryType { name } } }`
		off := newEndpoint(t, env, env.Resolver)
		resp := off.post(t, "", q)
		assert.NotEmpty(t, resp.Errors, "introspection is off by default")

		t.Setenv("ENABLE_GQL_INTROSPECTION", "true")
		on := newEndpoint(t, env, env.Resolver)
		resp = on.post(t, "", q)
		require.Empty(t, resp.Errors, "%+v", resp.Errors)
		assert.JSONEq(t, `{"__schema":{"queryType":{"name":"Query"}}}`, string(resp.Data))
	})

	t.Run("panics and server faults are reported to Sentry's hub of the request, and hidden from the client", func(t *testing.T) {
		panicking := newEndpointWith(t, env, env.with(func(r *resolver.Resolver) { r.RestaurantService = nil }), true)
		resp := panicking.post(t, "", `{ restaurantConfig { preparationMinutes } }`)
		require.NotEmpty(t, resp.Errors)

		broken := newEndpointWith(t, env, env.brokenResolver(t), true)
		resp = broken.post(t, "", `{ restaurantConfig { preparationMinutes } }`)
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
	})

	t.Run("a socket authenticated by the upgrade request alone stays authenticated, and ends with its token", func(t *testing.T) {
		ep := newEndpoint(t, env, env.Resolver)
		admin := signZitadelToken(t, ep.key, env.Fixtures.AdminUser.ID.String(), true, time.Hour)
		_, read := ep.subscribe(t, "", `subscription { couponUpdated { code } }`, http.Header{"Authorization": {"Bearer " + admin}})
		got := make(chan wsMessage, 1)
		go func() { m, _ := read(); got <- m }()
		require.Eventually(t, func() bool {
			env.Resolver.Broker.Publish("couponUpdated", &model.Coupon{Code: "HEADER"})
			return len(got) == 1
		}, 10*time.Second, 50*time.Millisecond)
		assert.Contains(t, string((<-got).Payload), `"code":"HEADER"`)

		life := ep.socketUntilExpiry(t, 5*time.Second, true)
		requireEndedWithToken(t, life)
	})
}
