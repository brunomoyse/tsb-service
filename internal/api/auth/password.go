package auth

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"

	userDomain "tsb-service/internal/modules/user/domain"
)

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
		msg := parseZitadelError(respBody)
		log.Warn("zitadel password change rejected", zap.Int("status", status), zap.String("message", msg))
		c.JSON(http.StatusBadRequest, gin.H{"error": mapPasswordError(msg)})
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

		appBaseURL := client.appBaseURL
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
		msg := parseZitadelError(respBody)
		log.Warn("zitadel set new password rejected", zap.Int("status", status), zap.String("message", msg))
		c.JSON(http.StatusBadRequest, gin.H{"error": mapPasswordResetError(msg)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
