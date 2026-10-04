package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/utils"
)

// recordingLookup is a UserLookup that records what the middleware extracted from the token.
type recordingLookup struct {
	appID string
	err   error

	sub, email, given, family string
	calls                     int
}

func (l *recordingLookup) ResolveZitadelID(_ context.Context, sub, email, given, family string) (string, error) {
	l.calls++
	l.sub, l.email, l.given, l.family = sub, email, given, family
	return l.appID, l.err
}

// fakeAppJWT is a POS token verifier whose answer is set per test.
type fakeAppJWT struct {
	deviceID uuid.UUID
	err      error
	exp      time.Time
	verified []string
}

func (f *fakeAppJWT) VerifyAccessToken(_ context.Context, tok string) (uuid.UUID, error) {
	f.verified = append(f.verified, tok)
	return f.deviceID, f.err
}
func (f *fakeAppJWT) AccessTokenExpiry(string) time.Time { return f.exp }

const testSub = "393485125419008005"

type authEnv struct {
	key    *rsa.PrivateKey
	v      *OIDCVerifier
	lookup *recordingLookup
	app    *fakeAppJWT
	appID  string
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	key, internalURL := fakeZitadel(t)
	appID := uuid.NewString()
	lookup := &recordingLookup{appID: appID}
	v, err := NewOIDCVerifier(t.Context(), "https://"+testIssuerHost, internalURL, testClientID, testProjectID, lookup)
	require.NoError(t, err)
	return &authEnv{key: key, v: v, lookup: lookup, app: &fakeAppJWT{}, appID: appID}
}

func (e *authEnv) token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	if _, ok := claims["aud"]; !ok {
		claims["aud"] = []string{testClientID}
	}
	return signToken(t, e.key, claims)
}

// seen is what a downstream handler observes about the request.
type seen struct {
	reached           bool
	userID, sub       string
	admin, pos, staff bool
	exp               time.Time
	ginUserID         string
	ginHasUserID      bool
}

func (e *authEnv) run(t *testing.T, mw gin.HandlerFunc, configure func(*http.Request)) (*httptest.ResponseRecorder, *seen) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &seen{}
	r := gin.New()
	r.Use(mw)
	r.GET("/x", func(c *gin.Context) {
		ctx := c.Request.Context()
		s.reached = true
		s.userID, s.sub = utils.GetUserID(ctx), utils.GetZitadelSub(ctx)
		s.admin, s.pos, s.staff = utils.GetIsAdmin(ctx), utils.GetIsPOS(ctx), utils.GetIsStaff(ctx)
		s.exp = utils.GetTokenExpiry(ctx)
		if v, ok := c.Get(string(utils.UserIDKey)); ok {
			s.ginUserID, _ = v.(string)
			s.ginHasUserID = true
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if configure != nil {
		configure(req)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, s
}

// unsignedToken is a JWT with alg "none" and an empty signature.
func unsignedToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tok.Header["kid"] = testKeyID
	s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	return s
}

// hmacToken is an HS256 JWT signed with the given secret, carrying the identity provider's key id.
func hmacToken(t *testing.T, secret []byte, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = testKeyID
	s, err := tok.SignedString(secret)
	require.NoError(t, err)
	return s
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

var adminRoles = map[string]any{"admin": map[string]any{"org": "domain"}}

func TestStrictAuthMiddleware_ZitadelTokens(t *testing.T) {
	t.Run("a valid customer token reaches the handler with the resolved app user", func(t *testing.T) {
		e := newAuthEnv(t)
		exp := time.Now().Add(30 * time.Minute).Truncate(time.Second)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "exp": exp.Unix(), "email": "  Ada@Example.COM ", "given_name": "Ada", "family_name": "Lovelace"})

		rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))

		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.True(t, s.reached)
		assert.Equal(t, e.appID, s.userID, "the app user id, never the raw Zitadel sub")
		assert.Equal(t, testSub, s.sub)
		assert.False(t, s.admin)
		assert.False(t, s.pos)
		assert.True(t, exp.Equal(s.exp), "the token expiry is bound to the request: %s", s.exp)
		assert.Equal(t, e.appID, s.ginUserID)
		assert.Equal(t, "ada@example.com", e.lookup.email, "email is normalised before provisioning")
		assert.Equal(t, "Ada", e.lookup.given)
		assert.Equal(t, "Lovelace", e.lookup.family)
		assert.Equal(t, testSub, e.lookup.sub)
	})

	t.Run("the token can come from the access_token cookie", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub})
		rec, s := e.run(t, e.v.StrictAuthMiddleware(), func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "access_token", Value: tok}) })
		require.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, e.appID, s.userID)
	})

	t.Run("the Authorization header wins over the cookie", func(t *testing.T) {
		e := newAuthEnv(t)
		good := e.token(t, jwt.MapClaims{"sub": testSub})
		rec, _ := e.run(t, e.v.StrictAuthMiddleware(), func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer not-a-token")
			r.AddCookie(&http.Cookie{Name: "access_token", Value: good})
		})
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("no credentials is 401 missing token", func(t *testing.T) {
		e := newAuthEnv(t)
		for name, configure := range map[string]func(*http.Request){
			"no header":    nil,
			"wrong scheme": func(r *http.Request) { r.Header.Set("Authorization", "Basic abc") },
			"bare token":   func(r *http.Request) { r.Header.Set("Authorization", e.token(t, jwt.MapClaims{"sub": testSub})) },
			"empty cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "access_token", Value: ""}) },
			"empty bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
			"lowercase bearer": func(r *http.Request) {
				r.Header.Set("Authorization", "bearer "+e.token(t, jwt.MapClaims{"sub": testSub}))
			},
		} {
			rec, s := e.run(t, e.v.StrictAuthMiddleware(), configure)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
			assert.JSONEq(t, `{"error":"missing token"}`, rec.Body.String(), name)
			assert.False(t, s.reached, name)
		}
	})

	t.Run("a bad token is 401 and never reaches the handler", func(t *testing.T) {
		e := newAuthEnv(t)
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		now := time.Now()
		cases := map[string]string{
			"garbage":                    "not-a-jwt",
			"expired":                    e.token(t, jwt.MapClaims{"sub": testSub, "exp": now.Add(-time.Hour).Unix()}),
			"signed with an unknown key": signToken(t, otherKey, jwt.MapClaims{"sub": testSub, "aud": []string{testClientID}}),
			"wrong audience":             e.token(t, jwt.MapClaims{"sub": testSub, "aud": []string{"someone-else"}}),
			"wrong issuer":               e.token(t, jwt.MapClaims{"sub": testSub, "iss": "https://evil.example"}),
			"empty subject":              e.token(t, jwt.MapClaims{"sub": ""}),
			// Algorithm confusion: the token claims to be unsigned, or HMAC-signed with a value an
			// attacker can know (a made-up secret, the public modulus), instead of RS256.
			"alg none":                    unsignedToken(t, jwt.MapClaims{"sub": testSub, "aud": []string{testClientID}, "iss": "https://" + testIssuerHost, "exp": now.Add(time.Hour).Unix()}),
			"HS256 with a guessed secret": hmacToken(t, []byte("secret"), jwt.MapClaims{"sub": testSub, "aud": []string{testClientID}, "iss": "https://" + testIssuerHost, "exp": now.Add(time.Hour).Unix()}),
			"HS256 with the public key":   hmacToken(t, e.key.N.Bytes(), jwt.MapClaims{"sub": testSub, "aud": []string{testClientID}, "iss": "https://" + testIssuerHost, "exp": now.Add(time.Hour).Unix()}),
		}
		for name, tok := range cases {
			rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
			assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
			assert.JSONEq(t, `{"error":"invalid or expired token"}`, rec.Body.String(), name)
			assert.False(t, s.reached, name)
		}
		assert.Zero(t, e.lookup.calls, "no account is provisioned for a rejected token")
	})

	// BUG(product decision pending): the verifier does not check "nbf" (not before). A token whose
	// nbf lies an hour in the future is accepted, although it says it must not be used yet. Zitadel
	// does not issue such tokens, so the exposure is low (the signature, issuer, audience and exp
	// are still enforced), but RFC 7519 asks verifiers to reject them. When the verifier starts
	// checking nbf, move this case into the rejected table of the test above.
	t.Run("a token that is not valid yet (nbf in the future) is currently accepted", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "nbf": time.Now().Add(time.Hour).Unix()})
		rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.True(t, s.reached)
	})

	t.Run("a user that cannot be resolved is refused, not let in under the raw sub", func(t *testing.T) {
		e := newAuthEnv(t)
		e.lookup.err = errors.New("db down")
		rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(e.token(t, jwt.MapClaims{"sub": testSub})))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, s.reached)
	})

	t.Run("without a user lookup every request is refused", func(t *testing.T) {
		e := newAuthEnv(t)
		e.v.userLookup = nil
		rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(e.token(t, jwt.MapClaims{"sub": testSub})))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, s.reached)
	})

	t.Run("a token without an exp claim is refused by the verifier", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "exp": nil})
		rec, _ := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestStrictAuthMiddleware_AdminScope(t *testing.T) {
	t.Run("the generic role claim grants admin", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "urn:zitadel:iam:org:project:roles": adminRoles})
		_, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
		assert.True(t, s.admin)
		assert.True(t, s.staff)
	})

	t.Run("the project-specific role claim grants admin as a fallback", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "urn:zitadel:iam:org:project:" + testProjectID + ":roles": adminRoles})
		_, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
		assert.True(t, s.admin)
	})

	t.Run("the project claim of another project does not", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "urn:zitadel:iam:org:project:other-project:roles": adminRoles})
		_, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
		assert.False(t, s.admin)
	})

	t.Run("another role is not admin", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "urn:zitadel:iam:org:project:roles": map[string]any{"cook": map[string]any{"org": "domain"}}})
		_, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(tok))
		assert.False(t, s.admin)
	})

	t.Run("with an admin client allowlist only the dashboard client keeps the role", func(t *testing.T) {
		e := newAuthEnv(t)
		e.v.SetAdminClientIDs([]string{"dashboard"})
		roles := jwt.MapClaims{"sub": testSub, "urn:zitadel:iam:org:project:roles": adminRoles}

		roles["client_id"] = "dashboard"
		_, dash := e.run(t, e.v.StrictAuthMiddleware(), bearer(e.token(t, roles)))
		assert.True(t, dash.admin)

		roles["client_id"] = "customer-site"
		_, shop := e.run(t, e.v.StrictAuthMiddleware(), bearer(e.token(t, roles)))
		assert.True(t, shop.reached, "a customer-site token is still a valid customer")
		assert.False(t, shop.admin, "but the admin role is not honored for it")
	})
}

func TestStrictAuthMiddleware_POSFallback(t *testing.T) {
	t.Run("a POS device token gets staff scope, never admin", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.deviceID = uuid.New()
		e.app.exp = time.Now().Add(time.Hour).Truncate(time.Second)
		e.v.SetAppJWTVerifier(e.app)

		rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer("pos-token"))

		require.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, e.app.deviceID.String(), s.userID)
		assert.Equal(t, e.app.deviceID.String(), s.ginUserID)
		assert.True(t, s.pos)
		assert.True(t, s.staff)
		assert.False(t, s.admin)
		assert.True(t, e.app.exp.Equal(s.exp))
		assert.Zero(t, e.lookup.calls, "a device is not a Zitadel user")
	})

	t.Run("the POS verifier is only asked when Zitadel refused the token", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.deviceID = uuid.New()
		e.v.SetAppJWTVerifier(e.app)
		_, s := e.run(t, e.v.StrictAuthMiddleware(), bearer(e.token(t, jwt.MapClaims{"sub": testSub})))
		assert.Equal(t, e.appID, s.userID)
		assert.Empty(t, e.app.verified)
	})

	t.Run("a token neither side accepts is 401", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.err = errors.New("revoked")
		e.v.SetAppJWTVerifier(e.app)
		rec, s := e.run(t, e.v.StrictAuthMiddleware(), bearer("nope"))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, s.reached)
	})

	t.Run("a verifier that answers with the nil UUID is not trusted", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.deviceID = uuid.Nil
		e.v.SetAppJWTVerifier(e.app)
		rec, _ := e.run(t, e.v.StrictAuthMiddleware(), bearer("nope"))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestOptionalAuthMiddleware(t *testing.T) {
	t.Run("anonymous requests pass through with no identity", func(t *testing.T) {
		e := newAuthEnv(t)
		rec, s := e.run(t, e.v.OptionalAuthMiddleware(), nil)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.True(t, s.reached)
		assert.Empty(t, s.userID)
		assert.False(t, s.staff)
	})

	t.Run("a valid token identifies the caller", func(t *testing.T) {
		e := newAuthEnv(t)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "urn:zitadel:iam:org:project:roles": adminRoles})
		_, s := e.run(t, e.v.OptionalAuthMiddleware(), bearer(tok))
		assert.Equal(t, e.appID, s.userID)
		assert.True(t, s.admin)
	})

	t.Run("an invalid token is ignored, not rejected", func(t *testing.T) {
		e := newAuthEnv(t)
		rec, s := e.run(t, e.v.OptionalAuthMiddleware(), bearer("garbage"))
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.True(t, s.reached)
		assert.Empty(t, s.userID)
	})

	t.Run("a POS token identifies the device", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.deviceID = uuid.New()
		e.v.SetAppJWTVerifier(e.app)
		_, s := e.run(t, e.v.OptionalAuthMiddleware(), bearer("pos"))
		assert.Equal(t, e.app.deviceID.String(), s.userID)
		assert.True(t, s.pos)
	})

	t.Run("a refused user lookup leaves the caller anonymous", func(t *testing.T) {
		e := newAuthEnv(t)
		e.lookup.err = errors.New("db down")
		_, s := e.run(t, e.v.OptionalAuthMiddleware(), bearer(e.token(t, jwt.MapClaims{"sub": testSub})))
		assert.True(t, s.reached)
		assert.Empty(t, s.userID)
	})
}

func TestVerifyToken(t *testing.T) {
	t.Run("a Zitadel token returns the raw sub, admin flag and expiry", func(t *testing.T) {
		e := newAuthEnv(t)
		exp := time.Now().Add(time.Hour).Truncate(time.Second)
		tok := e.token(t, jwt.MapClaims{"sub": testSub, "exp": exp.Unix(), "urn:zitadel:iam:org:project:roles": adminRoles})
		sub, admin, pos, gotExp, err := e.v.VerifyToken(t.Context(), tok)
		require.NoError(t, err)
		assert.Equal(t, testSub, sub)
		assert.True(t, admin)
		assert.False(t, pos)
		assert.True(t, exp.Equal(gotExp))
		assert.Zero(t, e.lookup.calls, "VerifyToken does not provision users")
	})

	t.Run("a POS token falls back to the device verifier", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.deviceID = uuid.New()
		e.app.exp = time.Now().Add(2 * time.Hour).Truncate(time.Second)
		e.v.SetAppJWTVerifier(e.app)
		sub, admin, pos, exp, err := e.v.VerifyToken(t.Context(), "pos-token")
		require.NoError(t, err)
		assert.Equal(t, e.app.deviceID.String(), sub)
		assert.False(t, admin)
		assert.True(t, pos)
		assert.True(t, e.app.exp.Equal(exp))
	})

	t.Run("a token nobody accepts returns the Zitadel error", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.err = errors.New("revoked")
		e.v.SetAppJWTVerifier(e.app)
		sub, admin, pos, exp, err := e.v.VerifyToken(t.Context(), "nope")
		require.Error(t, err)
		assert.Empty(t, sub)
		assert.False(t, admin || pos)
		assert.True(t, exp.IsZero())
		assert.Equal(t, []string{"nope"}, e.app.verified)

		e.v.appJWT = nil
		_, _, _, _, err = e.v.VerifyToken(t.Context(), "nope")
		require.Error(t, err)
	})

	t.Run("a token without a readable exp yields a zero expiry, not an error", func(t *testing.T) {
		e := newAuthEnv(t)
		e.app.deviceID = uuid.New()
		e.v.SetAppJWTVerifier(e.app)
		_, _, _, exp, err := e.v.VerifyToken(t.Context(), "pos")
		require.NoError(t, err)
		assert.True(t, exp.IsZero())
	})
}

func TestNewOIDCVerifierErrors(t *testing.T) {
	t.Run("an unparsable issuer URL", func(t *testing.T) {
		_, err := NewOIDCVerifier(t.Context(), "http://[::1", "", testClientID, testProjectID, nil)
		require.ErrorContains(t, err, "invalid issuer URL")
	})

	t.Run("an unparsable internal URL", func(t *testing.T) {
		_, err := NewOIDCVerifier(t.Context(), "https://"+testIssuerHost, "http://[::1", testClientID, testProjectID, nil)
		require.ErrorContains(t, err, "invalid internal URL")
	})

	t.Run("an unreachable Zitadel through the internal URL", func(t *testing.T) {
		_, err := NewOIDCVerifier(t.Context(), "https://"+testIssuerHost, "http://127.0.0.1:1", testClientID, testProjectID, nil)
		require.ErrorContains(t, err, "failed to initialize Zitadel authorizer")
	})

	t.Run("an unreachable Zitadel with the default transport", func(t *testing.T) {
		_, err := NewOIDCVerifier(t.Context(), "http://127.0.0.1:1", "", testClientID, "", nil)
		require.ErrorContains(t, err, "failed to initialize Zitadel authorizer")
	})
}

func TestNewOIDCVerifierAudiences(t *testing.T) {
	_, internalURL := fakeZitadel(t)

	same, err := NewOIDCVerifier(t.Context(), "https://"+testIssuerHost, internalURL, testClientID, testClientID, nil)
	require.NoError(t, err)
	assert.Len(t, same.authorizers, 1, "a project id equal to the client id is not registered twice")

	noProject, err := NewOIDCVerifier(t.Context(), "https://"+testIssuerHost, internalURL, testClientID, "", nil)
	require.NoError(t, err)
	assert.Len(t, noProject.authorizers, 1)

	both, err := NewOIDCVerifier(t.Context(), "https://"+testIssuerHost, internalURL, testClientID, testProjectID, nil)
	require.NoError(t, err)
	assert.Len(t, both.authorizers, 2, "the API client id first, then the project id")
}

func TestCheckAuthorizationWithoutAuthorizers(t *testing.T) {
	_, err := (&OIDCVerifier{}).checkAuthorization(t.Context(), "tok")
	require.EqualError(t, err, "no Zitadel authorizer configured")
}

func TestExtractTokenAndClaimString(t *testing.T) {
	assert.Equal(t, "", claimString(nil, "k"))
	assert.Equal(t, "", claimString(map[string]any{"k": 12}, "k"), "a non-string claim reads as empty")
	assert.Equal(t, "v", claimString(map[string]any{"k": "v"}, "k"))
	assert.Equal(t, "azp-1", tokenClientID(map[string]any{"azp": "azp-1"}))
}

func TestTryVerifyAppJWTWithoutVerifier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	assert.False(t, (&OIDCVerifier{}).tryVerifyAppJWT(c, "tok"))
	assert.Empty(t, utils.GetUserID(c.Request.Context()))
}
