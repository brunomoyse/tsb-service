package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

// completeProfileRequest is the frontend's request to fill in first/last name
// after a successful OTP verify (or IdP sign-in) on a placeholder account
// (Pattern B identifier-first signup).
type completeProfileRequest struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
}

// maxProfileNameLen bounds each name field.
const maxProfileNameLen = 100

// CompleteOtpProfileHandler updates a Zitadel user's first/last name after a
// successful OTP verify or IdP sign-in. Used by Pattern B identifier-first
// signup: the OTP request handler creates a placeholder Zitadel user for
// unknown emails, the user proves email control by completing the OTP, and
// then fills in their real name here before /auth/finalize completes the OIDC
// flow.
//
// POST /auth/session/otp/complete-profile { sessionId, sessionToken, firstName, lastName }
//
// Authorization: Zitadel does not verify the sessionToken for the service
// account that created the session, so the token alone proves nothing here.
// The write is allowed only when the session carries a verified first factor
// (otpEmail or IdP intent) AND the user still has the placeholder name. An
// otp/request session for someone else's email never gets the otpEmail factor
// without the emailed code, and established accounts can never be renamed.
func CompleteOtpProfileHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req completeProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil ||
		req.SessionID == "" || req.SessionToken == "" ||
		req.FirstName == "" || req.LastName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId, sessionToken, firstName and lastName are required"})
		return
	}
	if len(req.FirstName) > maxProfileNameLen || len(req.LastName) > maxProfileNameLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name too long"})
		return
	}

	factors, err := fetchSessionFactors(req.SessionID)
	if err != nil {
		log.Warn("session lookup failed", zap.Error(err))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_session"})
		return
	}
	if !factors.OTPEmailVerified && !factors.IntentVerified {
		log.Warn("complete-profile on a session without a verified first factor")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_session"})
		return
	}

	given, family, err := fetchUserProfileNames(factors.UserID)
	if err != nil {
		log.Error("zitadel user lookup failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "profile update failed"})
		return
	}
	if given != placeholderProfileMarker {
		// A retried submit with the same names is treated as success.
		if given == req.FirstName && family == req.LastName {
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "profile_already_complete"})
		return
	}

	if err := updateZitadelUserProfile(factors.UserID, req.FirstName, req.LastName); err != nil {
		log.Error("zitadel user profile update failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "profile update failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
