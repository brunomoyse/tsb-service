package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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

// idpSessionResponse mirrors the OTP verify response: the session pair plus a
// flag telling the frontend the user still has a placeholder name and must
// complete their profile (via /auth/session/otp/complete-profile) before
// /auth/finalize. RequiresProfile is true for IdP users whose provider omitted
// the name (e.g. Apple on repeat authorizations).
type idpSessionResponse struct {
	SessionID       string `json:"sessionId"`
	SessionToken    string `json:"sessionToken"`
	RequiresProfile bool   `json:"requiresProfile"`
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

	// Best-effort: a user just provisioned from an IdP that omitted the name
	// (Apple on repeat auth) carries the placeholder marker and should be routed
	// through profile completion before /auth/finalize — same signal and same
	// /auth/session/otp/complete-profile endpoint the OTP flow uses. A failed
	// lookup must never block login: the user keeps the placeholder name and can
	// edit it from their profile later.
	var requiresProfile bool
	if userID != "" {
		if needs, err := userNeedsProfileCompletion(userID); err == nil {
			requiresProfile = needs
		}
	}

	c.JSON(http.StatusOK, idpSessionResponse{
		SessionID:       zResp.SessionID,
		SessionToken:    zResp.SessionToken,
		RequiresProfile: requiresProfile,
	})
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
		// Top-level userId is set by Zitadel when the external identity is ALREADY
		// linked to a Zitadel user (a repeat IdP login). When present we use it
		// directly — see step 0.
		UserID       string          `json:"userId"`
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

	// 0. The external identity is already linked to a Zitadel user (repeat IdP
	// login). Use that user directly and skip the find-by-email / link / create
	// path below, which is fragile when the link already exists — e.g. an
	// incomplete first sign-in (user closed the app before completing their
	// profile) left a placeholder account with the IdP already linked, and
	// re-linking or re-creating it would fail.
	if intentInfo.UserID != "" {
		log.Info("idp identity already linked to zitadel user", zap.String("user_id", intentInfo.UserID))
		return intentInfo.UserID, nil
	}

	// 2. Try to find existing Zitadel user by the IdP email
	email := strings.ToLower(strings.TrimSpace(intentInfo.IdpInfo.UserName))
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
			// An already-existing link is success. Zitadel usually returns 409,
			// but some versions surface "already exists" under a different status
			// (e.g. 400/412), so inspect the message too rather than trusting 409.
			switch {
			case linkStatus == http.StatusOK || linkStatus == http.StatusCreated || linkStatus == http.StatusConflict:
				// freshly linked, or already linked (409) — ok
			case containsAny(parseZitadelError(linkResp), "already", "AlreadyExists"):
				// already linked under a non-409 status — ok
			default:
				return "", fmt.Errorf("link idp to user returned status %d: %s", linkStatus, linkResp)
			}

			return userID, nil
		}
	}

	// 3. No existing user — create one using the template from the intent.
	// Uses admin PAT since user creation requires management permissions.
	//
	// Apple only returns the user's name on the FIRST authorization of an Apple
	// ID against our Services ID; every later sign-in (including any sign-in
	// after the app account was deleted — Apple keeps the consent server-side)
	// omits it. Zitadel's /v2/users/human rejects an empty givenName/familyName
	// (min 1 rune), so we backfill the placeholder marker — same as the OTP
	// signup path — and let the complete-profile flow collect the real name.
	addHumanUser := ensureProfileName(log, intentInfo.AddHumanUser)
	log.Info("creating new zitadel user from IdP intent",
		zap.String("email", email),
		zap.String("idp_user_id", intentInfo.IdpInfo.UserID),
		zap.ByteString("add_human_user", addHumanUser),
	)
	userBody, userStatus, err := zitadelAdminRequest("POST", "/v2/users/human", addHumanUser)
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

	// Zitadel's user query projection updates asynchronously after creation, so
	// the POST /v2/sessions call that immediately follows can race it and fail
	// with a spurious NotFound (404, "User could not be found"), surfacing as
	// "Authentication failed" — this hit an App Store reviewer on a first-time
	// Apple sign-in. Wait for the new user to be queryable before returning. If
	// it never shows we proceed anyway: the session attempt is no worse off than
	// without the wait, and a non-blocking login beats blocking on a slow poll.
	if err := waitForZitadelUserProjection(userResp.UserID); err != nil {
		log.Warn("new IdP user not yet visible in query projection; proceeding to session create anyway",
			zap.String("user_id", userResp.UserID), zap.Error(err))
	}

	return userResp.UserID, nil
}

// ensureProfileName guarantees the AddHumanUser template carries a non-empty
// givenName/familyName. Social IdPs (notably Apple) omit the name on repeat
// authorizations, but Zitadel requires both fields (min 1 rune), so an empty
// name makes /v2/users/human fail with HTTP 400 and the login surfaces an
// error. When either is missing we substitute the placeholder marker so the
// account is created; the user is then routed through profile completion (the
// marker is detected by userNeedsProfileCompletion) to supply their real name.
// On any parse/marshal failure the original payload is returned unchanged.
func ensureProfileName(log *zap.Logger, raw json.RawMessage) json.RawMessage {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		log.Warn("could not parse AddHumanUser template; sending as-is", zap.Error(err))
		return raw
	}

	profile, ok := payload["profile"].(map[string]any)
	if !ok || profile == nil {
		profile = map[string]any{}
		payload["profile"] = profile
	}

	filled := false
	if s, _ := profile["givenName"].(string); strings.TrimSpace(s) == "" {
		profile["givenName"] = placeholderProfileMarker
		filled = true
	}
	if s, _ := profile["familyName"].(string); strings.TrimSpace(s) == "" {
		profile["familyName"] = placeholderProfileMarker
		filled = true
	}

	if !filled {
		return raw
	}

	patched, err := json.Marshal(payload)
	if err != nil {
		log.Warn("could not re-marshal AddHumanUser template; sending as-is", zap.Error(err))
		return raw
	}
	log.Info("backfilled placeholder name on IdP user creation (provider omitted name)")
	return patched
}
