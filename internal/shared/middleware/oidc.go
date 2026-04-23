package middleware

import (
	"context"
	"fmt"
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
// the POS /auth/rrn-login endpoint. It lets shop-floor devices hit GraphQL with
// an app token instead of a Zitadel JWT — see internal/modules/pos. POS tokens
// only ever grant the staff role; admin is reserved for Zitadel JWTs.
type AppJWTVerifier interface {
	VerifyAccessToken(token string) (userID uuid.UUID, isStaff bool, err error)
	// AccessTokenExpiry parses the token's exp claim without re-verifying
	// the signature. Callers SHOULD only use the returned time after a
	// successful VerifyAccessToken. Zero means the token carries no exp.
	AccessTokenExpiry(token string) time.Time
}

// OIDCVerifier validates Zitadel JWTs via JWKS (no network call per request).
// Optionally verifies app-signed POS JWTs as a fallback when Zitadel validation fails.
type OIDCVerifier struct {
	authorizer *authorization.Authorizer[*oauth.IntrospectionContext]
	userLookup UserLookup
	appJWT     AppJWTVerifier // optional
	projectID  string         // Zitadel project ID for project-specific role claim fallback
}

// NewOIDCVerifier initializes the Zitadel Go SDK authorizer for local JWT validation.
// issuerURL is the Zitadel instance URL (e.g., "https://auth.example.com").
// internalURL is optional — when set (e.g., "http://zitadel-api:8080" in Docker),
// OIDC discovery and JWKS requests are routed to the internal URL while the external
// domain is preserved as the Host header and issuer.
// clientID is the audience expected in the JWT (the Zitadel project ID or app client ID).
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

	// Initialize with local JWT validation (JWKS-based, no per-request introspection)
	var verifierInit authorization.VerifierInitializer[*oauth.IntrospectionContext]
	if httpClient != nil {
		verifierInit = oauth.WithJWT(clientID, httpClient)
	} else {
		verifierInit = oauth.DefaultJWTAuthorization(clientID)
	}

	authZ, err := authorization.New(ctx, z, verifierInit)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Zitadel authorizer: %w", err)
	}

	return &OIDCVerifier{authorizer: authZ, userLookup: userLookup, projectID: projectID}, nil
}

// SetAppJWTVerifier registers the optional POS JWT verifier. Call this after
// constructing the POS service so StrictAuth / OptionalAuth fall back to it
// when a bearer token is not a valid Zitadel JWT.
func (v *OIDCVerifier) SetAppJWTVerifier(appJWT AppJWTVerifier) {
	v.appJWT = appJWT
}

// tryVerifyAppJWT attempts to validate a POS-issued HS256 token. Returns true
// on success and populates userID/isStaff in the Gin context. POS tokens are
// never granted admin — only the staff role.
func (v *OIDCVerifier) tryVerifyAppJWT(c *gin.Context, tokenStr string) bool {
	if v.appJWT == nil {
		return false
	}
	userID, isStaff, err := v.appJWT.VerifyAccessToken(tokenStr)
	if err != nil || userID == uuid.Nil {
		zap.L().Debug("app JWT verification failed", zap.Error(err))
		return false
	}
	zap.L().Debug("app JWT verified", zap.String("userID", userID.String()), zap.Bool("isStaff", isStaff))
	ctx := utils.SetUserID(c.Request.Context(), userID.String())
	ctx = utils.SetIsAdmin(ctx, false)
	ctx = utils.SetIsStaff(ctx, isStaff)
	ctx = utils.SetTokenExpiry(ctx, v.appJWT.AccessTokenExpiry(tokenStr))
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(utils.UserIDKey), userID.String())
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
	if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if cookie, err := c.Cookie("access_token"); err == nil && cookie != "" {
		return cookie
	}
	return ""
}

// verifyAndSetContext verifies the JWT and sets userID/isAdmin in context.
func (v *OIDCVerifier) verifyAndSetContext(c *gin.Context, tokenStr string) bool {
	authCtx, err := v.authorizer.CheckAuthorization(c.Request.Context(), "Bearer "+tokenStr)
	if err != nil {
		zap.L().Debug("OIDC token verification failed", zap.Error(err))
		return false
	}

	sub := authCtx.UserID()
	if sub == "" {
		return false
	}

	// Try the generic claim first (works with introspection), then fall back
	// to the project-specific claim path (works with JWT access tokens where
	// the role is under urn:zitadel:iam:org:project:{projectID}:roles).
	isAdmin := authCtx.IsGrantedRole("admin")
	if !isAdmin && v.projectID != "" {
		isAdmin = authCtx.IsGrantedRoleInProject(v.projectID, "admin", "")
	}

	// Profile claims — the zitadel-go SDK's local JWT path only populates
	// sub/aud/iss on IntrospectionContext; everything else (including email/
	// given_name/family_name) must be read from the raw Claims map. If the
	// JWT doesn't carry them at all (common for social-IdP logins), the user
	// service falls back to fetching from Zitadel's user API.
	email := claimString(authCtx.Claims, "email")
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
	userID := sub
	if v.userLookup != nil {
		appID, lookupErr := v.userLookup.ResolveZitadelID(
			c.Request.Context(), sub, email, givenName, familyName,
		)
		if lookupErr != nil {
			zap.L().Warn("failed to resolve Zitadel user", zap.String("sub", sub), zap.Error(lookupErr))
			// Fall back to raw sub — downstream will handle the error
		} else {
			userID = appID
		}
	}

	ctx := utils.SetUserID(c.Request.Context(), userID)
	ctx = utils.SetZitadelSub(ctx, sub)
	ctx = utils.SetIsAdmin(ctx, isAdmin)
	if expRaw, ok := authCtx.Claims["exp"].(float64); ok {
		ctx = utils.SetTokenExpiry(ctx, time.Unix(int64(expRaw), 0).UTC())
	}
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(utils.UserIDKey), userID)

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
// Never aborts — unauthenticated requests pass through with no context values.
func (v *OIDCVerifier) OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.Next()
			return
		}
		if !v.verifyAndSetContext(c, tokenStr) {
			v.tryVerifyAppJWT(c, tokenStr)
		}
		c.Next()
	}
}

// VerifyToken verifies a raw JWT string and returns the subject plus role flags.
// Used by the GraphQL WebSocket InitFunc. Tries Zitadel first, then falls back
// to the POS app JWT verifier. Returns the raw Zitadel sub (no DB lookup) for
// Zitadel tokens, or the app user UUID for POS tokens. Admin implies staff —
// callers may rely on isStaff to gate @staff-protected subscriptions.
//
// exp is the access token's expiry (UTC). The caller should bind the WS/HTTP
// context to this deadline so long-lived subscriptions die when the token
// expires instead of outliving the credential that authorized them. A zero
// value means the token had no readable exp claim; callers should treat that
// as "do not enforce a deadline" rather than "token is already expired".
func (v *OIDCVerifier) VerifyToken(ctx context.Context, tokenStr string) (subject string, isAdmin, isStaff bool, exp time.Time, err error) {
	authCtx, zitadelErr := v.authorizer.CheckAuthorization(ctx, "Bearer "+tokenStr)
	if zitadelErr == nil {
		admin := authCtx.IsGrantedRole("admin")
		if !admin && v.projectID != "" {
			admin = authCtx.IsGrantedRoleInProject(v.projectID, "admin", "")
		}
		// The SDK's introspection context exposes exp via the raw claims map.
		var tokenExp time.Time
		if expRaw, ok := authCtx.Claims["exp"].(float64); ok {
			tokenExp = time.Unix(int64(expRaw), 0).UTC()
		}
		return authCtx.UserID(), admin, admin, tokenExp, nil
	}
	if v.appJWT != nil {
		if uid, staff, appErr := v.appJWT.VerifyAccessToken(tokenStr); appErr == nil {
			return uid.String(), false, staff, v.appJWT.AccessTokenExpiry(tokenStr), nil
		}
	}
	return "", false, false, time.Time{}, zitadelErr
}
