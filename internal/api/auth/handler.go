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
	"os"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
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
		// Forward the error status (401 for bad credentials, etc.)
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

	c.JSON(http.StatusOK, finalizeResponse(zResp))
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
