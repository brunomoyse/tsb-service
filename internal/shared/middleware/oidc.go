package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"go.uber.org/zap"

	"tsb-service/pkg/utils"
)

// internalRouteTransport rewrites outgoing requests to use a Docker-internal URL
// while preserving the external Host header. This is necessary because Zitadel
// resolves instances by Host header, and the external URL may not be reachable
// from inside the Docker network.
type internalRouteTransport struct {
	externalHost   string // e.g., "auth.tokyosushibarliege.be"
	internalScheme string // e.g., "http"
	internalHost   string // e.g., "zitadel-api:8080"
	base           http.RoundTripper
}

func (t *internalRouteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Host = t.externalHost
	req.URL.Scheme = t.internalScheme
	req.URL.Host = t.internalHost
	return t.base.RoundTrip(req)
}

// UserLookup resolves a Zitadel sub to an app user UUID.
// Implemented by UserService to avoid circular imports.
type UserLookup interface {
	// ResolveZitadelID returns the app user UUID for a Zitadel sub.
	// If the user doesn't exist, it creates one (JIT provisioning).
	ResolveZitadelID(ctx context.Context, zitadelID, email, firstName, lastName string) (appUserID string, err error)
}

// AppJWTVerifier is a secondary verifier for tsb-service-signed JWTs issued by
// the POS /auth/device-login endpoint. It lets the shop-floor device hit
// GraphQL with an app token instead of a Zitadel JWT; see internal/modules/pos.
// POS tokens grant staff scope (utils.SetIsPOS), never admin: the device only
// reaches the @staff operations the shop floor needs.
type AppJWTVerifier interface {
	VerifyAccessToken(ctx context.Context, token string) (deviceID uuid.UUID, err error)
	// AccessTokenExpiry parses the token's exp claim without re-verifying
	// the signature. Callers SHOULD only use the returned time after a
	// successful VerifyAccessToken. Zero means the token carries no exp.
	AccessTokenExpiry(token string) time.Time
}

// OIDCVerifier validates Zitadel JWTs via JWKS (no network call per request).
// Optionally verifies app-signed POS JWTs as a fallback when Zitadel validation fails.
type OIDCVerifier struct {
	// authorizers accept tokens for the API client ID first, then for the
	// project ID. Interactive logins put every app's client ID in aud; machine
	// users (client_credentials) only get the project ID they asked for.
	authorizers []*authorization.Authorizer[*oauth.IntrospectionContext]
	userLookup  UserLookup
	appJWT      AppJWTVerifier // optional
	projectID   string         // Zitadel project ID for project-specific role claim fallback
	// adminClientIDs restricts which OIDC clients may carry the admin role.
	// All apps share one Zitadel project, so without this a token minted for
	// the customer site would also be admin for anyone holding the role.
	// Empty = legacy behavior (any client), kept so a missing env var does not
	// lock admins out.
	adminClientIDs map[string]bool
}

// NewOIDCVerifier initializes the Zitadel Go SDK authorizer for local JWT validation.
// issuerURL is the Zitadel instance URL (e.g., "https://auth.example.com").
// internalURL is optional: when set (e.g., "http://zitadel-api:8080" in Docker),
// OIDC discovery and JWKS requests are routed to the internal URL while the external
// domain is preserved as the Host header and issuer.
// clientID is the audience expected in the JWT (the API app client ID). The
// project ID is accepted as an audience too, for machine users.
// userLookup resolves Zitadel sub → app user UUID (pass nil to skip, userID will be the raw Zitadel sub).
// NewOIDCVerifier initializes the Zitadel Go SDK authorizer for local JWT validation.
// projectID is the Zitadel project ID used to check the project-specific role claim
// (urn:zitadel:iam:org:project:{projectID}:roles) as a fallback when the generic
// role claim is not present in JWT access tokens.
func NewOIDCVerifier(ctx context.Context, issuerURL, internalURL, clientID, projectID string, userLookup UserLookup) (*OIDCVerifier, error) {
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid issuer URL: %w", err)
	}
	domain := parsed.Host

	// Build the Zitadel configuration and HTTP client
	var httpClient *http.Client
	if internalURL != "" {
		// In Docker, route requests to the internal URL while keeping the external Host header.
		// The SDK uses the external domain for issuer validation (matches the JWT iss claim),
		// and the transport rewrites the actual HTTP connection to the internal address.
		internalParsed, parseErr := url.Parse(internalURL)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid internal URL: %w", parseErr)
		}
		httpClient = &http.Client{
			Transport: &internalRouteTransport{
				externalHost:   domain,
				internalScheme: internalParsed.Scheme,
				internalHost:   internalParsed.Host,
				base:           http.DefaultTransport,
			},
		}
	}

	z := zitadel.New(domain)

	audiences := []string{clientID}
	if projectID != "" && projectID != clientID {
		audiences = append(audiences, projectID)
	}

	v := &OIDCVerifier{userLookup: userLookup, projectID: projectID}
	for i, aud := range audiences {
		// Local JWT validation (JWKS-based, no per-request introspection)
		var verifierInit authorization.VerifierInitializer[*oauth.IntrospectionContext]
		if httpClient != nil {
			verifierInit = oauth.WithJWT(aud, httpClient)
		} else {
			verifierInit = oauth.DefaultJWTAuthorization(aud)
		}
		var opts []authorization.Option[*oauth.IntrospectionContext]
		if i < len(audiences)-1 {
			// A miss here is expected whenever a later audience matches; only
			// the last authorizer logs its refusal.
			opts = append(opts, authorization.WithLogger[*oauth.IntrospectionContext](slog.New(slog.DiscardHandler)))
		}
		authZ, err := authorization.New(ctx, z, verifierInit, opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Zitadel authorizer: %w", err)
		}
		v.authorizers = append(v.authorizers, authZ)
	}
	return v, nil
}

// checkAuthorization validates a Zitadel JWT against each accepted audience.
// It returns the first error when none accepts the token.
func (v *OIDCVerifier) checkAuthorization(ctx context.Context, tokenStr string) (*oauth.IntrospectionContext, error) {
	var firstErr error
	for _, authZ := range v.authorizers {
		authCtx, err := authZ.CheckAuthorization(ctx, "Bearer "+tokenStr)
		if err == nil {
			if err := checkNotBefore(authCtx.Claims, time.Now()); err != nil {
				return nil, err
			}
			return authCtx, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = errors.New("no Zitadel authorizer configured")
	}
	return nil, firstErr
}

// notBeforeLeeway tolerates clock skew between Zitadel and this service.
const notBeforeLeeway = time.Minute

// checkNotBefore rejects a token whose "nbf" (not before) claim is still in the future
// (RFC 7519 §4.1.5); the Zitadel SDK's local JWT check does not enforce it.
func checkNotBefore(claims map[string]any, now time.Time) error {
	nbf, ok := claims["nbf"].(float64)
	if !ok {
		return nil
	}
	if now.Add(notBeforeLeeway).Before(time.Unix(int64(nbf), 0)) {
		return errors.New("token is not valid yet (nbf in the future)")
	}
	return nil
}

// SetAdminClientIDs restricts the admin role to tokens issued to these OIDC
// client IDs (the dashboard). Blank entries are ignored.
func (v *OIDCVerifier) SetAdminClientIDs(ids []string) {
	v.adminClientIDs = make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			v.adminClientIDs[id] = true
		}
	}
	if len(v.adminClientIDs) == 0 {
		v.adminClientIDs = nil
		zap.L().Warn("ZITADEL_ADMIN_CLIENT_IDS is empty: the admin role is honored on tokens from every client")
	}
}

// isAdmin reports whether a verified Zitadel token grants admin scope: the
// admin project role AND, when an allowlist is configured, a token issued to
// an admin client.
func (v *OIDCVerifier) isAdmin(authCtx *oauth.IntrospectionContext) bool {
	// Try the generic claim first (works with introspection), then fall back
	// to the project-specific claim path (works with JWT access tokens where
	// the role is under urn:zitadel:iam:org:project:{projectID}:roles).
	hasRole := authCtx.IsGrantedRole("admin")
	if !hasRole && v.projectID != "" {
		hasRole = authCtx.IsGrantedRoleInProject(v.projectID, "admin", "")
	}
	return hasRole && v.clientMayBeAdmin(authCtx.Claims)
}

// clientMayBeAdmin reports whether the token's OIDC client is allowed to carry
// admin scope. Always true when no allowlist is configured.
func (v *OIDCVerifier) clientMayBeAdmin(claims map[string]any) bool {
	if v.adminClientIDs == nil {
		return true
	}
	return v.adminClientIDs[tokenClientID(claims)]
}

// tokenClientID returns the OIDC client a Zitadel access token was issued to.
// Zitadel JWT access tokens carry it as client_id; azp is the standard OIDC
// name and is checked as a fallback.
func tokenClientID(claims map[string]any) string {
	if id := claimString(claims, "client_id"); id != "" {
		return id
	}
	return claimString(claims, "azp")
}

// SetAppJWTVerifier registers the optional POS JWT verifier. Call this after
// constructing the POS service so StrictAuth / OptionalAuth fall back to it
// when a bearer token is not a valid Zitadel JWT.
func (v *OIDCVerifier) SetAppJWTVerifier(appJWT AppJWTVerifier) {
	v.appJWT = appJWT
}

// tryVerifyAppJWT attempts to validate a POS-issued HS256 token. Returns true
// on success and populates the device principal in the Gin context with staff
// (POS) scope.
func (v *OIDCVerifier) tryVerifyAppJWT(c *gin.Context, tokenStr string) bool {
	if v.appJWT == nil {
		return false
	}
	deviceID, err := v.appJWT.VerifyAccessToken(c.Request.Context(), tokenStr)
	if err != nil || deviceID == uuid.Nil {
		zap.L().Debug("app JWT verification failed", zap.Error(err))
		return false
	}
	zap.L().Debug("app JWT verified", zap.String("deviceID", deviceID.String()))
	ctx := utils.SetUserID(c.Request.Context(), deviceID.String())
	ctx = utils.SetIsAdmin(ctx, false)
	ctx = utils.SetIsPOS(ctx, true)
	ctx = utils.SetTokenExpiry(ctx, v.appJWT.AccessTokenExpiry(tokenStr))
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(utils.UserIDKey), deviceID.String())
	return true
}

// claimString safely extracts a string claim from the JWT raw claims map.
func claimString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// extractToken gets the token from Authorization header (or cookie as fallback).
func extractToken(c *gin.Context) string {
	if token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer "); ok {
		return token
	}
	if cookie, err := c.Cookie("access_token"); err == nil && cookie != "" {
		return cookie
	}
	return ""
}

// resolveAppUserID resolves a Zitadel sub to an app user UUID via the
// configured userLookup (with JIT provisioning). Returns ("", false) if no
// lookup is configured or resolution fails. Callers should refuse the request
// when ok is false: raw subs may be opaque provider identifiers (e.g. Google
// numeric IDs) that break Postgres UUID columns downstream.
func (v *OIDCVerifier) resolveAppUserID(ctx context.Context, sub, email, givenName, familyName string) (string, bool) {
	if v.userLookup == nil {
		zap.L().Warn("OIDC verifier has no userLookup configured, refusing request",
			zap.String("sub", sub))
		return "", false
	}
	appID, err := v.userLookup.ResolveZitadelID(ctx, sub, email, givenName, familyName)
	if err != nil {
		zap.L().Warn("failed to resolve Zitadel user, refusing request",
			zap.String("sub", sub), zap.Error(err))
		return "", false
	}
	return appID, true
}

// verifyAndSetContext verifies the JWT and sets userID/isAdmin in context.
func (v *OIDCVerifier) verifyAndSetContext(c *gin.Context, tokenStr string) bool {
	authCtx, err := v.checkAuthorization(c.Request.Context(), tokenStr)
	if err != nil {
		zap.L().Debug("OIDC token verification failed", zap.Error(err))
		return false
	}

	sub := authCtx.UserID()
	if sub == "" {
		return false
	}

	isAdmin := v.isAdmin(authCtx)

	// Profile claims: the zitadel-go SDK's local JWT path only populates
	// sub/aud/iss on IntrospectionContext; everything else (including email/
	// given_name/family_name) must be read from the raw Claims map. If the
	// JWT doesn't carry them at all (common for social-IdP logins), the user
	// service falls back to fetching from Zitadel's user API.
	email := strings.ToLower(strings.TrimSpace(claimString(authCtx.Claims, "email")))
	givenName := claimString(authCtx.Claims, "given_name")
	familyName := claimString(authCtx.Claims, "family_name")

	zap.L().Debug("oidc claims extracted",
		zap.String("sub", sub),
		zap.Bool("has_email", email != ""),
		zap.Bool("has_given", givenName != ""),
		zap.Bool("has_family", familyName != ""),
		zap.Bool("is_admin", isAdmin),
	)

	// Resolve Zitadel sub → app user UUID (with JIT provisioning)
	appID, ok := v.resolveAppUserID(c.Request.Context(), sub, email, givenName, familyName)
	if !ok {
		return false
	}
	ctx := utils.SetUserID(c.Request.Context(), appID)
	ctx = utils.SetZitadelSub(ctx, sub)
	ctx = utils.SetIsAdmin(ctx, isAdmin)
	if expRaw, ok := authCtx.Claims["exp"].(float64); ok {
		ctx = utils.SetTokenExpiry(ctx, time.Unix(int64(expRaw), 0).UTC())
	}
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(utils.UserIDKey), appID)
	return true
}

// StrictAuthMiddleware validates a Zitadel JWT (or an app-signed POS JWT) and
// aborts with 401 if both paths reject the token.
func (v *OIDCVerifier) StrictAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}

		if v.verifyAndSetContext(c, tokenStr) {
			c.Next()
			return
		}
		if v.tryVerifyAppJWT(c, tokenStr) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
	}
}

// OptionalAuthMiddleware parses a Zitadel JWT (or POS app JWT) if present.
// Unauthenticated requests pass through with no context values, with one
// exception: a WebSocket upgrade whose Authorization header carries a token
// that verifies on neither path is refused with 401 (see staleSocketUpgrade).
func (v *OIDCVerifier) OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.Next()
			return
		}
		if !v.verifyAndSetContext(c, tokenStr) && !v.tryVerifyAppJWT(c, tokenStr) && staleSocketUpgrade(c) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		c.Next()
	}
}

// staleSocketUpgrade reports whether the request is a WebSocket upgrade that
// authenticates with an Authorization header. Only the POS handheld does this
// (browsers cannot set headers on a WebSocket; tsb-core, the dashboard and
// tsb-mobile send the token in connection_init). If such an upgrade went
// through anonymously, the handheld would keep resubscribing on a socket that
// can never be authorized (seen after a POS signing key change). A 401 makes
// it refresh its token and open a new socket instead.
func staleSocketUpgrade(c *gin.Context) bool {
	_, hasBearer := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	return hasBearer && strings.EqualFold(c.GetHeader("Upgrade"), "websocket")
}

// VerifyToken verifies a raw JWT string and returns the subject plus admin flag
// (always false for POS tokens: check isPOS for staff scope).
// Used by the GraphQL WebSocket InitFunc. Tries Zitadel first, then falls back
// to the POS app JWT verifier. Returns the raw Zitadel sub for Zitadel tokens,
// or the device UUID for POS tokens (in which case isPOS=true and the caller
// should NOT run Zitadel JIT provisioning on the subject).
//
// exp is the access token's expiry (UTC). The caller should bind the WS/HTTP
// context to this deadline so long-lived subscriptions die when the token
// expires instead of outliving the credential that authorized them. A zero
// value means the token had no readable exp claim; callers should treat that
// as "do not enforce a deadline" rather than "token is already expired".
func (v *OIDCVerifier) VerifyToken(ctx context.Context, tokenStr string) (subject string, isAdmin, isPOS bool, exp time.Time, err error) {
	authCtx, zitadelErr := v.checkAuthorization(ctx, tokenStr)
	if zitadelErr == nil {
		admin := v.isAdmin(authCtx)
		var tokenExp time.Time
		if expRaw, ok := authCtx.Claims["exp"].(float64); ok {
			tokenExp = time.Unix(int64(expRaw), 0).UTC()
		}
		return authCtx.UserID(), admin, false, tokenExp, nil
	}
	if v.appJWT != nil {
		if deviceID, appErr := v.appJWT.VerifyAccessToken(ctx, tokenStr); appErr == nil {
			return deviceID.String(), false, true, v.appJWT.AccessTokenExpiry(tokenStr), nil
		}
	}
	return "", false, false, time.Time{}, zitadelErr
}
