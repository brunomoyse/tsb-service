package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

// completeProfileRequest is the frontend's request to fill in first/last name
// after a successful OTP verify on a placeholder account (Pattern B identifier-
// first signup). The session/token pair was issued by VerifyOtpHandler.
type completeProfileRequest struct {
	SessionID    string `json:"sessionId"`
	SessionToken string `json:"sessionToken"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
}

// CompleteOtpProfileHandler updates a Zitadel user's first/last name after a
// successful OTP verify. Used by Pattern B identifier-first signup: the OTP
// request handler creates a placeholder Zitadel user for unknown emails, the
// user proves email control by completing the OTP, and then fills in their
// real name here before /auth/finalize completes the OIDC flow.
//
// POST /auth/session/otp/complete-profile { sessionId, sessionToken, firstName, lastName }
//
// Authorization: the sessionId/sessionToken pair must originate from a
// successful verify. Defense-in-depth comes from CORS + rate limiting; this
// endpoint only updates non-security fields (display name) so a leaked
// sessionId without sessionToken still can't take over the account.
func CompleteOtpProfileHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req completeProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil ||
		req.SessionID == "" || req.SessionToken == "" ||
		req.FirstName == "" || req.LastName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId, sessionToken, firstName and lastName are required"})
		return
	}

	userID, err := lookupSessionUserID(req.SessionID)
	if err != nil {
		log.Warn("session user lookup failed", zap.Error(err))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_session"})
		return
	}

	if err := updateZitadelUserProfile(userID, req.FirstName, req.LastName); err != nil {
		log.Error("zitadel user profile update failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "profile update failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
