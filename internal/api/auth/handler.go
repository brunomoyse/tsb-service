// Package auth provides proxy endpoints for Zitadel authentication.
// The frontend calls these endpoints instead of Zitadel directly,
// because the Session API requires a service account token.
package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

	// Follow the Zitadel callback URL to get the final redirect with code+state.
	// This is needed for Capacitor apps where the frontend can't follow HTTP→custom-scheme redirects.
	finalURL := zResp.CallbackURL
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Capture the redirect URL without following it
			finalURL = req.URL.String()
			return http.ErrUseLastResponse
		},
	}
	resp, err := httpClient.Get(zResp.CallbackURL)
	if err == nil {
		_ = resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != "" {
			finalURL = loc
		}
	}

	c.JSON(http.StatusOK, finalizeResponse{CallbackURL: finalURL})
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

	// Rewrite the authorize URL to use the internal Zitadel address (if configured)
	// to avoid Cloudflare Tunnel hairpin (public domain → Cloudflare → Tunnel → same server → 502).
	// Preserve the original host for the Host header (Zitadel uses virtual hosting).
	var originalHost string
	if client.externalHost != "" {
		if parsed, err := url.Parse(req.AuthorizeURL); err == nil {
			if internal, err2 := url.Parse(client.baseURL); err2 == nil {
				originalHost = parsed.Host
				parsed.Scheme = internal.Scheme
				parsed.Host = internal.Host
				req.AuthorizeURL = parsed.String()
			}
		}
	}

	// Follow the redirect chain to capture the authRequestID from the Location header
	var redirectURL string
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			redirectURL = r.URL.String()
			return http.ErrUseLastResponse
		},
	}

	httpReq, err := http.NewRequest("GET", req.AuthorizeURL, nil)
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
