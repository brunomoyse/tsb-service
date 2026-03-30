package auth

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

// idpStartRequest is the frontend's request to start a social IdP login.
type idpStartRequest struct {
	Provider   string `json:"provider"`   // "google", "apple"
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
	return client.idpIDs[provider]
}

// StartIdPIntentHandler starts an IdP intent for social login (Google, Apple).
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
