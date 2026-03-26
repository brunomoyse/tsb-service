package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"tsb-service/pkg/utils"
)

// hostOverrideTransport sets the Host header to the external domain
// when making requests to an internal Docker URL for OIDC discovery.
// Zitadel resolves instances by Host header, so this is required.
type hostOverrideTransport struct {
	host string
	base http.RoundTripper
}

func (t *hostOverrideTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Host = t.host
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
	verifier   *oidc.IDTokenVerifier
	userLookup UserLookup
}

// zitadelClaims represents the claims we extract from Zitadel-issued JWT access tokens.
type zitadelClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	// Zitadel encodes project roles as:
	// "urn:zitadel:iam:org:project:roles": { "admin": { ... } }
	ProjectRoles map[string]any `json:"urn:zitadel:iam:org:project:roles"`
}

// NewOIDCVerifier initializes the OIDC provider and returns a JWT verifier.
// issuerURL is the Zitadel instance URL (e.g., "https://auth.example.com").
// internalURL is optional — when set (e.g., "http://zitadel-api:8080" in Docker),
// OIDC discovery uses the internal URL while tokens are validated against the external issuer.
// clientID is the audience expected in the JWT (the Zitadel project ID or app client ID).
// userLookup resolves Zitadel sub → app user UUID (pass nil to skip, userID will be the raw Zitadel sub).
func NewOIDCVerifier(ctx context.Context, issuerURL, internalURL, clientID string, userLookup UserLookup) (*OIDCVerifier, error) {
	discoveryURL := issuerURL
	if internalURL != "" {
		// In Docker, the external issuer (https://...) isn't reachable from the container.
		// Use the internal URL for discovery but allow the issuer mismatch in the response.
		// Also override the Host header so Zitadel can resolve the correct instance.
		discoveryURL = internalURL
		ctx = oidc.InsecureIssuerURLContext(ctx, internalURL)

		parsed, _ := url.Parse(issuerURL)
		externalHost := parsed.Host
		client := &http.Client{
			Transport: &hostOverrideTransport{
				host: externalHost,
				base: http.DefaultTransport,
			},
		}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	}

	provider, err := oidc.NewProvider(ctx, discoveryURL)
	if err != nil {
		return nil, err
	}

	verifierCfg := &oidc.Config{
		ClientID: clientID,
	}
	if internalURL != "" {
		// When using internal discovery URL, the provider's issuer is the internal URL,
		// but tokens contain the external issuer. Skip the automatic issuer check.
		verifierCfg.SkipIssuerCheck = true
	}
	verifier := provider.Verifier(verifierCfg)

	return &OIDCVerifier{verifier: verifier, userLookup: userLookup}, nil
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
	idToken, err := v.verifier.Verify(c.Request.Context(), tokenStr)
	if err != nil {
		zap.L().Debug("OIDC token verification failed", zap.Error(err))
		return false
	}

	var claims zitadelClaims
	if err := idToken.Claims(&claims); err != nil {
		zap.L().Debug("failed to parse OIDC claims", zap.Error(err))
		return false
	}

	sub := idToken.Subject
	if sub == "" {
		return false
	}

	// Reject users who haven't verified their email
	if !claims.EmailVerified {
		zap.L().Debug("OIDC user email not verified", zap.String("sub", sub), zap.String("email", claims.Email))
		return false
	}

	isAdmin := false
	if claims.ProjectRoles != nil {
		if _, ok := claims.ProjectRoles["admin"]; ok {
			isAdmin = true
		}
	}

	// Resolve Zitadel sub → app user UUID (with JIT provisioning)
	userID := sub
	if v.userLookup != nil {
		appID, err := v.userLookup.ResolveZitadelID(c.Request.Context(), sub, claims.Email, claims.GivenName, claims.FamilyName)
		if err != nil {
			zap.L().Warn("failed to resolve Zitadel user", zap.String("sub", sub), zap.Error(err))
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
	idToken, err := v.verifier.Verify(ctx, tokenStr)
	if err != nil {
		return "", false, err
	}

	var claims zitadelClaims
	if err := idToken.Claims(&claims); err != nil {
		return "", false, err
	}

	if claims.ProjectRoles != nil {
		if _, ok := claims.ProjectRoles["admin"]; ok {
			isAdmin = true
		}
	}

	return idToken.Subject, isAdmin, nil
}
