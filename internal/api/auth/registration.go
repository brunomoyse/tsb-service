package auth

import (
	"encoding/json"
	"fmt"
	"net/http"

	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/logging"

	userDomain "tsb-service/internal/modules/user/domain"
)

// registerRequest is the frontend's user registration request.
type registerRequest struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
	Phone     string `json:"phone,omitempty"`
	Lang      string `json:"lang,omitempty"`
}

// RegisterHandler proxies user registration to Zitadel's User API and sends
// the verification email via Scaleway using our own templates. The user is
// created without a password — login happens via email OTP.
// POST /auth/register { firstName, lastName, email, phone?, lang? }
func RegisterHandler(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.FirstName == "" || req.LastName == "" || req.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "firstName, lastName and email are required"})
		return
	}

	lang := req.Lang
	if lang == "" {
		lang = "fr"
	}

	// Zitadel v2: POST /v2/users/human — returnCode so we send the email ourselves.
	// No password block: passwordless accounts authenticate via Session API otpEmail.
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
		msg := parseZitadelError(respBody)
		log.Warn("zitadel user creation rejected", zap.Int("status", status), zap.String("message", msg))

		if status == http.StatusConflict || strings.Contains(msg, "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": ErrEmailAlreadyExists})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": ErrRegistrationFailed, "message": msg})
		return
	}

	// Parse the verification code from Zitadel's response
	// Zitadel returns { "userId": "...", "emailCode": "..." } at the top level
	var createResp struct {
		UserID    string `json:"userId"`
		EmailCode string `json:"emailCode"`
	}
	if err := json.Unmarshal(respBody, &createResp); err == nil && createResp.EmailCode != "" && scaleway.IsInitialized() {
		appBaseURL := client.appBaseURL
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

			appBaseURL := client.appBaseURL
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

		appBaseURL := client.appBaseURL
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
