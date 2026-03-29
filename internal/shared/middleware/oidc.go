package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
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

// OIDCVerifier validates Zitadel JWTs via JWKS (no network call per request).
type OIDCVerifier struct {
	authorizer *authorization.Authorizer[*oauth.IntrospectionContext]
	userLookup UserLookup
}

// NewOIDCVerifier initializes the Zitadel Go SDK authorizer for local JWT validation.
// issuerURL is the Zitadel instance URL (e.g., "https://auth.example.com").
// internalURL is optional — when set (e.g., "http://zitadel-api:8080" in Docker),
// OIDC discovery and JWKS requests are routed to the internal URL while the external
// domain is preserved as the Host header and issuer.
// clientID is the audience expected in the JWT (the Zitadel project ID or app client ID).
// userLookup resolves Zitadel sub → app user UUID (pass nil to skip, userID will be the raw Zitadel sub).
func NewOIDCVerifier(ctx context.Context, issuerURL, internalURL, clientID string, userLookup UserLookup) (*OIDCVerifier, error) {
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

	return &OIDCVerifier{authorizer: authZ, userLookup: userLookup}, nil
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

	isAdmin := authCtx.IsGrantedRole("admin")

	// Resolve Zitadel sub → app user UUID (with JIT provisioning)
	userID := sub
	if v.userLookup != nil {
		appID, lookupErr := v.userLookup.ResolveZitadelID(
			c.Request.Context(), sub,
			authCtx.Email, authCtx.GivenName, authCtx.FamilyName,
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
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(utils.UserIDKey), userID)

	return true
}

// StrictAuthMiddleware validates a Zitadel JWT and aborts with 401 if invalid.
func (v *OIDCVerifier) StrictAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}

		if !v.verifyAndSetContext(c, tokenStr) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Next()
	}
}

// OptionalAuthMiddleware parses a Zitadel JWT if present. Never aborts.
func (v *OIDCVerifier) OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractToken(c)
		if tokenStr != "" {
			v.verifyAndSetContext(c, tokenStr)
		}
		c.Next()
	}
}

// VerifyToken verifies a raw JWT string and returns the subject and admin status.
// Used by WebSocket InitFunc. Returns the raw Zitadel sub (no DB lookup).
func (v *OIDCVerifier) VerifyToken(ctx context.Context, tokenStr string) (subject string, isAdmin bool, err error) {
	authCtx, err := v.authorizer.CheckAuthorization(ctx, "Bearer "+tokenStr)
	if err != nil {
		return "", false, err
	}

	return authCtx.UserID(), authCtx.IsGrantedRole("admin"), nil
}
