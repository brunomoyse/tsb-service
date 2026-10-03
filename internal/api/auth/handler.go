// Package auth provides proxy endpoints for Zitadel authentication.
// The frontend calls these endpoints instead of Zitadel directly,
// because the Session API requires a service account token.
package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

// sessionResponse is returned to the frontend after a successful session
// creation or update. Used by both the OTP request (initial session) and
// the OTP verify (session token re-issued with otpEmail check fulfilled).
type sessionResponse struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
}

// finalizeRequest is the frontend's request to complete the OIDC flow.
type finalizeRequest struct {
	AuthRequestID string `json:"authRequestId"`
	SessionID     string `json:"sessionId"`
	SessionToken  string `json:"sessionToken"`
}

// finalizeResponse is returned to the frontend.
type finalizeResponse struct {
	CallbackURL string `json:"callbackUrl"`
}

// zitadelSessionResponse mirrors Zitadel's Session API response shape.
// Used by IdP session creation; the OTP flow has its own struct that also
// captures the otpEmail challenge code.
type zitadelSessionResponse struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
}

// zitadelFinalizeResponse is Zitadel's OIDC authorize finalize response.
type zitadelFinalizeResponse struct {
	CallbackURL string `json:"callbackUrl"`
}

// FinalizeOIDCHandler proxies the OIDC auth request finalization to Zitadel.
// POST /auth/finalize { authRequestId, sessionId, sessionToken }
func FinalizeOIDCHandler(c *gin.Context) {
	var req finalizeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AuthRequestID == "" || req.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authRequestId, sessionId, and sessionToken are required"})
		return
	}

	// Serialize finalizes for the same authRequestID. Zitadel's auth request
	// is one-shot — a duplicate POST after a stutter would 4xx and the user
	// sees "expired". The cache returns the already-resolved callbackUrl.
	entry := finalizeGate.acquire(req.AuthRequestID)
	defer finalizeGate.release(entry)
	if cached, ok := entry.hit(req.SessionID); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	// Staff logins (admin clients) must pass TOTP when the user enrolled it.
	// The login UI asks for it, but the check has to live here: Zitadel does
	// not apply login policies to the Session API, so a client could otherwise
	// skip the second step and call finalize directly.
	mfaErr, err := checkStaffMFA(req.AuthRequestID, req.SessionID)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("staff mfa check failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}
	if mfaErr != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": mfaErr})
		return
	}

	// Finalize the OIDC auth request by linking it to the session
	// Zitadel v2 API: POST /v2/oidc/auth_requests/{authRequestId}
	body := map[string]any{
		"session": map[string]any{
			"sessionId":    req.SessionID,
			"sessionToken": req.SessionToken,
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/oidc/auth_requests/"+req.AuthRequestID, body)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("zitadel oidc finalize failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	var zResp zitadelFinalizeResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid response from auth service"})
		return
	}

	// Zitadel returns the final OIDC-client redirect URL (with ?code+&state)
	// directly. For http(s) clients we MUST return it as-is: doing a server-side
	// GET would (a) consume the one-shot OAuth code and (b) hit confidential
	// clients without their state cookie, getting bounced to /login with a
	// relative redirect that we'd then mis-resolve.
	//
	// Custom-scheme URIs (Capacitor: be.tokyosushibarliege.app:/) can't be
	// fetched server-side anyway; the frontend extracts the code from the
	// URL and exchanges it via /auth/token-exchange.
	finalURL := zResp.CallbackURL

	finalResp := finalizeResponse{CallbackURL: finalURL}
	finalizeGate.cache(entry, req.SessionID, finalResp)
	c.JSON(http.StatusOK, finalResp)
}

// POST /auth/authorize-proxy { authorizeUrl }
// Follows the OIDC authorize redirect server-side and returns the authRequestID.
// Used by Capacitor apps that can't follow browser redirects.
func AuthorizeProxyHandler(c *gin.Context) {
	var req struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AuthorizeURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authorizeUrl is required"})
		return
	}

	// Only the issuer's own authorize endpoint may be proxied. Without this
	// check the handler is an unauthenticated server-side GET to any URL.
	target, err := validateAuthorizeURL(req.AuthorizeURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_authorize_url"})
		return
	}

	// Rewrite the authorize URL to use the internal Zitadel address (if configured)
	// to avoid Cloudflare Tunnel hairpin (public domain → Cloudflare → Tunnel → same server → 502).
	// Keep the issuer host for the Host header (Zitadel uses virtual hosting).
	var originalHost string
	if client.externalHost != "" {
		if internal, err2 := url.Parse(client.baseURL); err2 == nil {
			originalHost = target.Host
			target.Scheme = internal.Scheme
			target.Host = internal.Host
		}
	}
	req.AuthorizeURL = target.String()

	// Follow the redirect chain to capture the authRequestID from the Location header
	var redirectURL string
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			redirectURL = r.URL.String()
			return http.ErrUseLastResponse
		},
	}

	httpReq, err := http.NewRequest(http.MethodGet, req.AuthorizeURL, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid authorize URL"})
		return
	}
	// Set the original public Host header so Zitadel's virtual hosting resolves correctly
	if originalHost != "" {
		httpReq.Host = originalHost
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("authorize proxy failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach authorization server"})
		return
	}
	_ = resp.Body.Close()

	// Use Location header if available, otherwise the captured redirect URL
	if loc := resp.Header.Get("Location"); loc != "" {
		redirectURL = loc
	}

	if redirectURL == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "no redirect from authorization server"})
		return
	}

	// Parse authRequestID from the redirect URL
	parsed, parseErr := url.Parse(redirectURL)
	if parseErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid redirect URL"})
		return
	}

	authRequestID := parsed.Query().Get("authRequestID")
	if authRequestID == "" {
		authRequestID = parsed.Query().Get("authRequest")
	}

	c.JSON(http.StatusOK, gin.H{"authRequestId": authRequestID, "redirectUrl": redirectURL})
}

// authorizePath is the only Zitadel path AuthorizeProxyHandler may fetch.
const authorizePath = "/oauth/v2/authorize"

// errInvalidAuthorizeURL is returned when a proxied authorize URL is not the
// issuer's authorize endpoint.
var errInvalidAuthorizeURL = errors.New("authorize URL is not the issuer authorize endpoint")

// validateAuthorizeURL accepts only {issuer}/oauth/v2/authorize?... and
// returns the parsed URL. Scheme and host must match the configured issuer;
// userinfo and fragments are rejected.
func validateAuthorizeURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errInvalidAuthorizeURL
	}
	issuer, err := url.Parse(strings.TrimRight(client.issuerURL, "/"))
	if err != nil || issuer.Host == "" {
		return nil, errInvalidAuthorizeURL
	}
	if u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		!strings.EqualFold(u.Scheme, issuer.Scheme) ||
		!strings.EqualFold(u.Host, issuer.Host) ||
		u.Path != issuer.Path+authorizePath {
		return nil, errInvalidAuthorizeURL
	}
	return u, nil
}

// POST /auth/token-exchange
// Proxies the OIDC token exchange to Zitadel, avoiding CORS issues from Capacitor WebView.
// Supports both authorization_code (code exchange) and refresh_token (token refresh) grants.
func TokenExchangeHandler(c *gin.Context) {
	var req struct {
		Code         string `json:"code"`
		RedirectURI  string `json:"redirectUri"`
		ClientID     string `json:"clientId"`
		CodeVerifier string `json:"codeVerifier"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ClientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clientId is required"})
		return
	}

	// Validate client ID against known app client IDs
	if !client.allowedClients[req.ClientID] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client_id"})
		return
	}

	// Require exactly one of code or refreshToken
	if (req.Code == "" && req.RefreshToken == "") || (req.Code != "" && req.RefreshToken != "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide either code or refreshToken, not both"})
		return
	}

	tokenURL := client.issuerURL + "/oauth/v2/token"

	var form url.Values
	if req.RefreshToken != "" {
		form = url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {req.RefreshToken},
			"client_id":     {req.ClientID},
		}
	} else {
		form = url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {req.Code},
			"client_id":    {req.ClientID},
			"redirect_uri": {req.RedirectURI},
		}
		if req.CodeVerifier != "" {
			form.Set("code_verifier", req.CodeVerifier)
		}
	}

	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("token exchange failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "token exchange failed"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json", body)
}
