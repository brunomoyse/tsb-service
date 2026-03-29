// Package auth provides proxy endpoints for Zitadel authentication.
// The frontend calls these endpoints instead of Zitadel directly,
// because the Session API requires a service account token.
package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"

	userDomain "tsb-service/internal/modules/user/domain"
)

// sessionRequest is the frontend's login request.
type sessionRequest struct {
	LoginName string `json:"loginName"`
	Password  string `json:"password"`
}

// sessionResponse is returned to the frontend.
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

// zitadelSessionResponse is Zitadel's Session API response.
type zitadelSessionResponse struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
}

// zitadelFinalizeResponse is Zitadel's OIDC authorize finalize response.
type zitadelFinalizeResponse struct {
	CallbackURL string `json:"callbackUrl"`
}

func getZitadelPAT() string {
	return os.Getenv("ZITADEL_SERVICE_PAT")
}

func getZitadelAdminPAT() string {
	pat := os.Getenv("ZITADEL_ADMIN_PAT")
	if pat == "" {
		return getZitadelPAT() // Fallback to service PAT
	}
	return pat
}

func getZitadelURL() string {
	// Prefer internal Docker URL for API calls (avoids going through reverse proxy/tunnel)
	if u := os.Getenv("ZITADEL_INTERNAL_URL"); u != "" {
		return u
	}
	return os.Getenv("ZITADEL_ISSUER")
}

// CreateSessionHandler proxies login requests to Zitadel's Session API.
// POST /auth/session { loginName, password }
func CreateSessionHandler(c *gin.Context) {
	var req sessionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.LoginName == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "loginName and password are required"})
		return
	}

	body := map[string]any{
		"checks": map[string]any{
			"user":     map[string]any{"loginName": req.LoginName},
			"password": map[string]any{"password": req.Password},
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/sessions", body)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("zitadel session create failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusCreated && status != http.StatusOK {
		// Parse Zitadel error to return meaningful codes to the frontend
		var zErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &zErr)

		if strings.Contains(zErr.Message, "not set a password") || strings.Contains(zErr.Message, "COMMAND-3nJ4t") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no_password", "message": "This account uses social login. Please sign in with Google, Facebook, or Apple."})
			return
		}

		// Forward other errors as-is (401 for bad credentials, etc.)
		c.Data(status, "application/json", respBody)
		return
	}

	// Block login if email is not verified
	if userID, err := findZitadelUserByEmail(req.LoginName); err == nil {
		if !isZitadelEmailVerified(userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "email_not_verified"})
			return
		}
	}

	var zResp zitadelSessionResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid response from auth service"})
		return
	}

	c.JSON(http.StatusOK, sessionResponse(zResp))
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
	if internalURL := os.Getenv("ZITADEL_INTERNAL_URL"); internalURL != "" {
		if parsed, err := url.Parse(req.AuthorizeURL); err == nil {
			if internal, err2 := url.Parse(internalURL); err2 == nil {
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

	// Require exactly one of code or refreshToken
	if (req.Code == "" && req.RefreshToken == "") || (req.Code != "" && req.RefreshToken != "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide either code or refreshToken, not both"})
		return
	}

	zitadelIssuer := os.Getenv("ZITADEL_ISSUER")
	tokenURL := zitadelIssuer + "/oauth/v2/token"

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

// idpStartRequest is the frontend's request to start a social IdP login.
type idpStartRequest struct {
	Provider   string `json:"provider"`   // "google", "facebook", "apple"
	SuccessURL string `json:"successUrl"`
	FailureURL string `json:"failureUrl"`
}

// idpStartResponse is returned to the frontend.
type idpStartResponse struct {
	AuthURL string `json:"authUrl"`
}

// idpSessionRequest creates a session from an IdP intent.
type idpSessionRequest struct {
	IdPIntentID    string `json:"idpIntentId"`
	IdPIntentToken string `json:"idpIntentToken"`
	UserID         string `json:"userId,omitempty"` // Zitadel user ID from callback (if user already exists)
}

func getIdpID(provider string) string {
	switch provider {
	case "google":
		return os.Getenv("ZITADEL_IDP_GOOGLE_ID")
	case "facebook":
		return os.Getenv("ZITADEL_IDP_FACEBOOK_ID")
	case "apple":
		return os.Getenv("ZITADEL_IDP_APPLE_ID")
	default:
		return ""
	}
}

// StartIdPIntentHandler starts an IdP intent for social login (Google, Facebook, Apple).
// POST /auth/idp/start { provider, successUrl, failureUrl }
func StartIdPIntentHandler(c *gin.Context) {
	var req idpStartRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Provider == "" || req.SuccessURL == "" || req.FailureURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider, successUrl, and failureUrl are required"})
		return
	}

	idpID := getIdpID(req.Provider)
	if idpID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported or unconfigured provider"})
		return
	}

	body := map[string]any{
		"idpId": idpID,
		"urls": map[string]any{
			"successUrl": req.SuccessURL,
			"failureUrl": req.FailureURL,
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/idp_intents", body)
	if err != nil {
		logging.FromContext(c.Request.Context()).Error("zitadel idp intent failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusCreated && status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	var zResp struct {
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid response from auth service"})
		return
	}

	c.JSON(http.StatusOK, idpStartResponse{AuthURL: zResp.AuthURL})
}

// CreateIdPSessionHandler creates a Zitadel session from an IdP intent result.
// If no userId is provided (new user), it retrieves the IdP intent info
// and creates the Zitadel user first.
// POST /auth/idp/session { idpIntentId, idpIntentToken, userId? }
func CreateIdPSessionHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req idpSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.IdPIntentID == "" || req.IdPIntentToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "idpIntentId and idpIntentToken are required"})
		return
	}

	userID := req.UserID

	// If no user ID from callback, we need to create/find the Zitadel user
	if userID == "" {
		var err error
		userID, err = resolveOrCreateZitadelUser(log, req.IdPIntentID, req.IdPIntentToken)
		if err != nil {
			log.Error("failed to resolve/create zitadel user for IdP intent", zap.Error(err))
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to provision user"})
			return
		}
	}

	// Create session with both user + idpIntent checks
	body := map[string]any{
		"checks": map[string]any{
			"user": map[string]any{
				"userId": userID,
			},
			"idpIntent": map[string]any{
				"idpIntentId":    req.IdPIntentID,
				"idpIntentToken": req.IdPIntentToken,
			},
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/sessions", body)
	if err != nil {
		log.Error("zitadel idp session create failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusCreated && status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	var zResp zitadelSessionResponse
	if err := json.Unmarshal(respBody, &zResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid response from auth service"})
		return
	}

	c.JSON(http.StatusOK, sessionResponse(zResp))
}

// resolveOrCreateZitadelUser retrieves the IdP intent info, then either finds
// an existing Zitadel user by email (and links the IdP) or creates a new one.
func resolveOrCreateZitadelUser(log *zap.Logger, intentID, intentToken string) (string, error) {
	// 1. Retrieve IdP intent info (includes user template and IdP details)
	intentBody, intentStatus, err := zitadelRequest("POST", "/v2/idp_intents/"+intentID, map[string]any{
		"idpIntentToken": intentToken,
	})
	if err != nil {
		return "", fmt.Errorf("retrieve idp intent: %w", err)
	}
	if intentStatus != http.StatusOK {
		return "", fmt.Errorf("retrieve idp intent returned status %d: %s", intentStatus, intentBody)
	}

	var intentInfo struct {
		AddHumanUser json.RawMessage `json:"addHumanUser"`
		IdpInfo      struct {
			IdpID    string `json:"idpId"`
			UserID   string `json:"userId"`   // IdP-side user ID (e.g. Google sub)
			UserName string `json:"userName"` // IdP-side username (email)
		} `json:"idpInformation"`
	}
	if err := json.Unmarshal(intentBody, &intentInfo); err != nil {
		return "", fmt.Errorf("parse idp intent info: %w", err)
	}

	// 2. Try to find existing Zitadel user by the IdP email
	email := intentInfo.IdpInfo.UserName
	if email != "" {
		userID, findErr := findZitadelUserByEmail(email)
		if findErr == nil && userID != "" {
			log.Info("found existing zitadel user for IdP login, linking IdP", zap.String("email", email), zap.String("user_id", userID))

			// Link the IdP to the existing user so the session check passes
			// Uses admin PAT since this requires user management permissions
			linkBody := map[string]any{
				"idpLink": map[string]any{
					"idpId":    intentInfo.IdpInfo.IdpID,
					"userId":   intentInfo.IdpInfo.UserID,
					"userName": intentInfo.IdpInfo.UserName,
				},
			}
			linkResp, linkStatus, linkErr := zitadelAdminRequest("POST", "/v2/users/"+userID+"/links", linkBody)
			if linkErr != nil {
				return "", fmt.Errorf("link idp to user: %w", linkErr)
			}
			// 409 Conflict means it's already linked — that's fine
			if linkStatus != http.StatusOK && linkStatus != http.StatusCreated && linkStatus != http.StatusConflict {
				return "", fmt.Errorf("link idp to user returned status %d: %s", linkStatus, linkResp)
			}

			return userID, nil
		}
	}

	// 3. No existing user — create one using the template from the intent
	// Uses admin PAT since user creation requires management permissions
	log.Info("creating new zitadel user from IdP intent", zap.String("email", email))
	userBody, userStatus, err := zitadelAdminRequest("POST", "/v2/users/human", intentInfo.AddHumanUser)
	if err != nil {
		return "", fmt.Errorf("create zitadel user: %w", err)
	}

	if userStatus != http.StatusCreated && userStatus != http.StatusOK {
		return "", fmt.Errorf("create zitadel user returned status %d: %s", userStatus, userBody)
	}

	var userResp struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(userBody, &userResp); err != nil {
		return "", fmt.Errorf("parse user creation response: %w", err)
	}

	return userResp.UserID, nil
}

// findZitadelUserByEmail searches for a Zitadel user by email.
func findZitadelUserByEmail(email string) (string, error) {
	body := map[string]any{
		"queries": []map[string]any{
			{
				"emailQuery": map[string]any{
					"emailAddress": email,
					"method":       "TEXT_QUERY_METHOD_EQUALS",
				},
			},
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/users", body)
	if err != nil {
		return "", fmt.Errorf("search users: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("search users returned status %d", status)
	}

	var searchResp struct {
		Result []struct {
			UserID string `json:"userId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &searchResp); err != nil {
		return "", fmt.Errorf("parse search response: %w", err)
	}

	if len(searchResp.Result) > 0 {
		return searchResp.Result[0].UserID, nil
	}
	return "", fmt.Errorf("no user found")
}

// changePasswordRequest is the frontend's request to change password.
type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePasswordHandler proxies password change to Zitadel's User API.
// POST /auth/change-password { currentPassword, newPassword }
// Requires strict auth middleware (needs Zitadel sub from context).
func ChangePasswordHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	zitadelSub := utils.GetZitadelSub(c.Request.Context())
	if zitadelSub == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.CurrentPassword == "" || req.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "currentPassword and newPassword are required"})
		return
	}

	// Zitadel v2: PUT /v2/users/{userId}/password
	body := map[string]any{
		"currentPassword": req.CurrentPassword,
		"newPassword": map[string]any{
			"password":       req.NewPassword,
			"changeRequired": false,
		},
	}

	respBody, status, err := zitadelAdminRequest("PUT", "/v2/users/"+zitadelSub+"/password", body)
	if err != nil {
		log.Error("zitadel password change failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK && status != http.StatusCreated {
		// Map Zitadel errors to frontend-expected error codes
		var zErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &zErr)
		msg := zErr.Message

		log.Warn("zitadel password change rejected", zap.Int("status", status), zap.String("message", msg))

		if strings.Contains(msg, "password invalid") || strings.Contains(msg, "COMMAND-3M0fs") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "wrong_password"})
		} else if strings.Contains(msg, "complexity") || strings.Contains(msg, "COMMAND-oz74F") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "weak_password"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password_change_failed"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// HasPasswordHandler checks if the authenticated user has a password set in Zitadel.
// GET /auth/has-password
func HasPasswordHandler(c *gin.Context) {
	zitadelSub := utils.GetZitadelSub(c.Request.Context())
	if zitadelSub == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	// Zitadel v2: GET /v2/users/{userId}
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+zitadelSub, nil)
	if err != nil || status != http.StatusOK {
		c.JSON(http.StatusOK, gin.H{"hasPassword": false})
		return
	}

	var userResp struct {
		User struct {
			Human struct {
				PasswordChanged string `json:"passwordChanged"`
			} `json:"human"`
		} `json:"user"`
	}
	if err := json.Unmarshal(respBody, &userResp); err != nil {
		c.JSON(http.StatusOK, gin.H{"hasPassword": false})
		return
	}

	hasPassword := userResp.User.Human.PasswordChanged != "" && userResp.User.Human.PasswordChanged != "0001-01-01T00:00:00Z"
	c.JSON(http.StatusOK, gin.H{"hasPassword": hasPassword})
}

// registerRequest is the frontend's user registration request.
type registerRequest struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	Phone     string `json:"phone,omitempty"`
	Lang      string `json:"lang,omitempty"`
}

// RegisterHandler proxies user registration to Zitadel's User API and sends
// the verification email via Scaleway using our own templates.
// POST /auth/register { firstName, lastName, email, password, phone?, lang? }
func RegisterHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.FirstName == "" || req.LastName == "" || req.Email == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "firstName, lastName, email and password are required"})
		return
	}

	lang := req.Lang
	if lang == "" {
		lang = "fr"
	}

	// Zitadel v2: POST /v2/users/human — returnCode so we send the email ourselves
	body := map[string]any{
		"userName": req.Email,
		"profile": map[string]any{
			"givenName":  req.FirstName,
			"familyName": req.LastName,
		},
		"email": map[string]any{
			"email":      req.Email,
			"returnCode": map[string]any{},
		},
		"password": map[string]any{
			"password":       req.Password,
			"changeRequired": false,
		},
	}
	if req.Phone != "" {
		body["phone"] = map[string]any{
			"phone": req.Phone,
		}
	}

	respBody, status, err := zitadelAdminRequest("POST", "/v2/users/human", body)
	if err != nil {
		log.Error("zitadel user creation failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK && status != http.StatusCreated {
		var zErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &zErr)
		msg := zErr.Message

		log.Warn("zitadel user creation rejected", zap.Int("status", status), zap.String("message", msg))

		if status == http.StatusConflict || strings.Contains(msg, "already exists") {
			// Google-first user linking: if user exists without a password, set the password
			if linkedUserID, findErr := findZitadelUserByEmail(req.Email); findErr == nil && !hasZitadelPassword(linkedUserID) {
				pwdBody := map[string]any{
					"newPassword": map[string]any{
						"password":       req.Password,
						"changeRequired": false,
					},
				}
				pwdResp, pwdStatus, pwdErr := zitadelAdminRequest("POST", "/v2/users/"+linkedUserID+"/password", pwdBody)
				if pwdErr != nil || (pwdStatus != http.StatusOK && pwdStatus != http.StatusCreated) {
					var pwdErrMsg struct {
						Message string `json:"message"`
					}
					_ = json.Unmarshal(pwdResp, &pwdErrMsg)
					if strings.Contains(pwdErrMsg.Message, "complexity") {
						c.JSON(http.StatusBadRequest, gin.H{"error": "weak_password", "message": "weak_password"})
					} else {
						c.JSON(http.StatusBadRequest, gin.H{"error": "registration_failed", "message": pwdErrMsg.Message})
					}
					return
				}
				log.Info("linked password to social-login user", zap.String("email", req.Email))
				c.JSON(http.StatusCreated, gin.H{"success": true})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "email_already_exists"})
		} else if strings.Contains(msg, "complexity") || strings.Contains(msg, "COMMAND-oz74F") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "weak_password", "message": "weak_password"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "registration_failed", "message": msg})
		}
		return
	}

	// Parse the verification code from Zitadel's response
	// Zitadel returns { "userId": "...", "emailCode": "..." } at the top level
	var createResp struct {
		UserID    string `json:"userId"`
		EmailCode string `json:"emailCode"`
	}
	if err := json.Unmarshal(respBody, &createResp); err == nil && createResp.EmailCode != "" && scaleway.IsInitialized() {
		appBaseURL := os.Getenv("APP_BASE_URL")
		verifyLink := fmt.Sprintf("%s/%s/auth/verify?userId=%s&code=%s",
			appBaseURL, lang, createResp.UserID, createResp.EmailCode)

		user := userDomain.User{
			FirstName: req.FirstName,
			LastName:  req.LastName,
			Email:     req.Email,
		}
		if err := scaleway.SendVerificationEmail(user, lang, verifyLink); err != nil {
			log.Error("failed to send verification email", zap.Error(err))
		}
	}

	c.JSON(http.StatusCreated, gin.H{"success": true})
}

// passwordResetRequestBody is the frontend's request to initiate a password reset.
type passwordResetRequestBody struct {
	Email string `json:"email"`
	Lang  string `json:"lang,omitempty"`
}

// RequestPasswordResetHandler proxies password reset requests to Zitadel and sends
// the reset email via Scaleway using our own templates.
// POST /auth/password/request-reset { email, lang? }
// Always returns 200 to prevent email enumeration.
func RequestPasswordResetHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req passwordResetRequestBody
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}

	lang := req.Lang
	if lang == "" {
		lang = "fr"
	}

	// Find user by email — silent success if not found (email enumeration prevention)
	userID, err := findZitadelUserByEmail(req.Email)
	if err != nil {
		log.Debug("password reset requested for unknown email", zap.String("email", req.Email))
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	// Zitadel v2: POST /v2/users/{userId}/password_reset — returnCode so we send the email ourselves
	respBody, _, err := zitadelAdminRequest("POST", "/v2/users/"+userID+"/password_reset", map[string]any{
		"returnCode": map[string]any{},
	})
	if err != nil {
		log.Error("zitadel password reset failed", zap.Error(err), zap.String("userId", userID))
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	// Parse the reset code and send our own email
	var resetResp struct {
		VerificationCode string `json:"verificationCode"`
	}
	if err := json.Unmarshal(respBody, &resetResp); err == nil && resetResp.VerificationCode != "" && scaleway.IsInitialized() {
		// Fetch user details for the email template (need firstName/lastName)
		userName := req.Email // Fallback
		userResp, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
		if err == nil && status == http.StatusOK {
			var u struct {
				User struct {
					Human struct {
						Profile struct {
							GivenName  string `json:"givenName"`
							FamilyName string `json:"familyName"`
						} `json:"profile"`
					} `json:"human"`
				} `json:"user"`
			}
			if json.Unmarshal(userResp, &u) == nil && u.User.Human.Profile.GivenName != "" {
				userName = u.User.Human.Profile.GivenName
			}
		}

		appBaseURL := os.Getenv("APP_BASE_URL")
		resetLink := fmt.Sprintf("%s/%s/auth/reset-password?userId=%s&code=%s",
			appBaseURL, lang, userID, resetResp.VerificationCode)

		user := userDomain.User{
			FirstName: userName,
			Email:     req.Email,
		}
		if err := scaleway.SendPasswordResetEmail(user, lang, resetLink); err != nil {
			log.Error("failed to send password reset email", zap.Error(err))
		}
	}

	// Always return success
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// setNewPasswordRequest is the frontend's request to complete a password reset.
type setNewPasswordRequest struct {
	UserID   string `json:"userId"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// SetNewPasswordHandler proxies password reset completion to Zitadel.
// POST /auth/password/reset { userId, code, password }
func SetNewPasswordHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req setNewPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserID == "" || req.Code == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userId, code and password are required"})
		return
	}

	// Zitadel v2: POST /v2/users/{userId}/password (with verification code)
	body := map[string]any{
		"newPassword": map[string]any{
			"password":       req.Password,
			"changeRequired": false,
		},
		"verificationCode": req.Code,
	}

	respBody, status, err := zitadelAdminRequest("POST", "/v2/users/"+req.UserID+"/password", body)
	if err != nil {
		log.Error("zitadel set new password failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK && status != http.StatusCreated {
		var zErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &zErr)
		msg := zErr.Message

		log.Warn("zitadel set new password rejected", zap.Int("status", status), zap.String("message", msg))

		if strings.Contains(msg, "complexity") || strings.Contains(msg, "COMMAND-oz74F") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "weak_password"})
		} else if strings.Contains(msg, "invalid") || strings.Contains(msg, "expired") || strings.Contains(msg, "COMMAND-3M0fs") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_code"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password_reset_failed"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// verifyEmailRequest is the frontend's request to verify an email address.
type verifyEmailRequest struct {
	UserID string `json:"userId"`
	Code   string `json:"code"`
}

// VerifyEmailHandler proxies email verification to Zitadel and sends a welcome email.
// POST /auth/verify-email { userId, code }
func VerifyEmailHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req verifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserID == "" || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userId and code are required"})
		return
	}

	// Zitadel v2: POST /v2/users/{userId}/email/verify
	body := map[string]any{
		"verificationCode": req.Code,
	}

	respBody, status, err := zitadelAdminRequest("POST", "/v2/users/"+req.UserID+"/email/verify", body)
	if err != nil {
		log.Error("zitadel email verification failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "authentication service unavailable"})
		return
	}

	if status != http.StatusOK && status != http.StatusCreated {
		var zErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &zErr)
		log.Warn("zitadel email verification rejected", zap.Int("status", status), zap.String("message", zErr.Message))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_code"})
		return
	}

	// Send welcome email asynchronously — don't block the response
	if scaleway.IsInitialized() {
		go func() {
			// Fetch user details for the welcome email
			userResp, uStatus, err := zitadelRequest("GET", "/v2/users/"+req.UserID, nil)
			if err != nil || uStatus != http.StatusOK {
				zap.L().Error("failed to fetch user for welcome email", zap.Error(err))
				return
			}

			var u struct {
				User struct {
					Human struct {
						Profile struct {
							GivenName  string `json:"givenName"`
							FamilyName string `json:"familyName"`
						} `json:"profile"`
						Email struct {
							Email string `json:"email"`
						} `json:"email"`
					} `json:"human"`
				} `json:"user"`
			}
			if json.Unmarshal(userResp, &u) != nil {
				return
			}

			user := userDomain.User{
				FirstName: u.User.Human.Profile.GivenName,
				LastName:  u.User.Human.Profile.FamilyName,
				Email:     u.User.Human.Email.Email,
			}

			appBaseURL := os.Getenv("APP_BASE_URL")
			// Default to "fr" — the welcome email just needs the menu link
			if err := scaleway.SendWelcomeEmail(user, "fr", appBaseURL+"/fr/menu"); err != nil {
				zap.L().Error("failed to send welcome email", zap.Error(err))
			}
		}()
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// resendVerificationRequest is the frontend's request to resend a verification email.
type resendVerificationRequest struct {
	Email string `json:"email"`
	Lang  string `json:"lang,omitempty"`
}

// ResendVerificationHandler resends the email verification code via Scaleway.
// POST /auth/resend-verification { email, lang? }
func ResendVerificationHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req resendVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}

	lang := req.Lang
	if lang == "" {
		lang = "fr"
	}

	// Find user by email
	userID, err := findZitadelUserByEmail(req.Email)
	if err != nil {
		// Silent success to prevent email enumeration
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	// Check if already verified
	if isZitadelEmailVerified(userID) {
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	// Request a new verification code from Zitadel
	respBody, _, err := zitadelAdminRequest("POST", "/v2/users/"+userID+"/email/resend", map[string]any{
		"returnCode": map[string]any{},
	})
	if err != nil {
		log.Error("zitadel resend verification failed", zap.Error(err))
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	// Parse the verification code and send our own email
	var codeResp struct {
		VerificationCode string `json:"verificationCode"`
	}
	if err := json.Unmarshal(respBody, &codeResp); err == nil && codeResp.VerificationCode != "" && scaleway.IsInitialized() {
		// Fetch user profile for the email template
		firstName := req.Email // Fallback
		userResp, uStatus, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
		if err == nil && uStatus == http.StatusOK {
			var u struct {
				User struct {
					Human struct {
						Profile struct {
							GivenName  string `json:"givenName"`
							FamilyName string `json:"familyName"`
						} `json:"profile"`
					} `json:"human"`
				} `json:"user"`
			}
			if json.Unmarshal(userResp, &u) == nil && u.User.Human.Profile.GivenName != "" {
				firstName = u.User.Human.Profile.GivenName
			}
		}

		appBaseURL := os.Getenv("APP_BASE_URL")
		verifyLink := fmt.Sprintf("%s/%s/auth/verify?userId=%s&code=%s",
			appBaseURL, lang, userID, codeResp.VerificationCode)

		user := userDomain.User{
			FirstName: firstName,
			Email:     req.Email,
		}
		if err := scaleway.SendVerificationEmail(user, lang, verifyLink); err != nil {
			log.Error("failed to send verification email", zap.Error(err))
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// isZitadelEmailVerified checks if a Zitadel user's email is verified.
func isZitadelEmailVerified(userID string) bool {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
	if err != nil || status != http.StatusOK {
		return false // Deny on error (safe default)
	}
	var userResp struct {
		User struct {
			Human struct {
				Email struct {
					IsVerified bool `json:"isVerified"`
				} `json:"email"`
			} `json:"human"`
		} `json:"user"`
	}
	if json.Unmarshal(respBody, &userResp) != nil {
		return false
	}
	return userResp.User.Human.Email.IsVerified
}

// hasZitadelPassword checks if a Zitadel user has a password set.
func hasZitadelPassword(userID string) bool {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
	if err != nil || status != http.StatusOK {
		return true // Assume has password on error (safe default)
	}
	var userResp struct {
		User struct {
			Human struct {
				PasswordChanged string `json:"passwordChanged"`
			} `json:"human"`
		} `json:"user"`
	}
	if json.Unmarshal(respBody, &userResp) != nil {
		return true
	}
	return userResp.User.Human.PasswordChanged != "" &&
		userResp.User.Human.PasswordChanged != "0001-01-01T00:00:00Z"
}

// zitadelRequest makes an authenticated request to the Zitadel API using the service PAT.
func zitadelRequest(method, path string, body any) ([]byte, int, error) {
	return zitadelRequestWithPAT(method, path, body, getZitadelPAT())
}

// zitadelAdminRequest makes an authenticated request to the Zitadel API using the admin PAT.
func zitadelAdminRequest(method, path string, body any) ([]byte, int, error) {
	return zitadelRequestWithPAT(method, path, body, getZitadelAdminPAT())
}

func zitadelRequestWithPAT(method, path string, body any, pat string) ([]byte, int, error) {
	url := getZitadelURL() + path

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pat)

	// When using internal Docker URL, set Host header to the external domain.
	// Zitadel resolves instances by Host header.
	if os.Getenv("ZITADEL_INTERNAL_URL") != "" {
		req.Host = os.Getenv("ZITADEL_ISSUER")
		// Strip protocol prefix for Host header
		req.Host = strings.TrimPrefix(req.Host, "https://")
		req.Host = strings.TrimPrefix(req.Host, "http://")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}
