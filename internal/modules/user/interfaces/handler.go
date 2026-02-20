package interfaces

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/internal/modules/user/application"
	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/oauth2"
	es "tsb-service/services/email/scaleway"
)

func getSameSiteMode() http.SameSite {
	if os.Getenv("APP_ENV") == "development" {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

func setAuthCookies(c *gin.Context, accessToken, refreshToken string) {
	domain := os.Getenv("SESSION_COOKIE_DOMAIN")
	c.SetSameSite(getSameSiteMode())
	c.SetCookie("access_token", accessToken, 15*60, "/", domain, true, true)
	c.SetCookie("refresh_token", refreshToken, 7*24*3600, "/", domain, true, true)
}

func clearAuthCookies(c *gin.Context) {
	domain := os.Getenv("SESSION_COOKIE_DOMAIN")
	c.SetSameSite(getSameSiteMode())
	c.SetCookie("access_token", "", -1, "/", domain, true, true)
	c.SetCookie("refresh_token", "", -1, "/", domain, true, true)
}

type UserHandler struct {
	service        application.UserService
	addressService addressApplication.AddressService
	jwtSecret      string
}

func NewUserHandler(
	service application.UserService,
	addressService addressApplication.AddressService,
	jwtSecret string,
) *UserHandler {
	return &UserHandler{
		service:        service,
		addressService: addressService,
		jwtSecret:      jwtSecret,
	}
}

func (h *UserHandler) GetUserProfileHandler(c *gin.Context) {
	// Retrieve the logged-in user's ID from the Gin context.
	userID := c.GetString("userID")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "handler: user not authenticated"})
		return
	}

	user, err := h.service.GetUserByID(c, userID)

	if err != nil {
		slog.ErrorContext(c.Request.Context(), "failed to fetch user profile", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user profile"})
		return
	}

	// If user has an address ID, fetch the address details.
	var address *addressDomain.Address
	if user.AddressID != nil {
		address, _ = h.addressService.GetAddressByID(c.Request.Context(), *user.AddressID)
	}

	res := NewUserResponse(user, address)

	c.JSON(http.StatusOK, res)
}

func (h *UserHandler) UpdateMeHandler(c *gin.Context) {
	ctx := c.Request.Context()

	userID := c.GetString("userID")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "handler: user not authenticated"})
		return
	}

	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.WarnContext(ctx, "invalid request payload", "handler", "updateMe", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	user, err := h.service.UpdateMe(ctx, userID, req.FirstName, req.LastName, req.Email, req.PhoneNumber, req.AddressID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to update user profile", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update user profile"})
		return
	}

	// If user has an address ID, fetch the address details.
	var address *addressDomain.Address
	if user.AddressID != nil {
		address, _ = h.addressService.GetAddressByID(c.Request.Context(), *user.AddressID)
	}

	res := NewUserResponse(user, address)
	c.JSON(http.StatusOK, res)
}

func (h *UserHandler) RegisterHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req RegistrationRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		slog.WarnContext(ctx, "invalid request payload", "handler", "register", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	// Validate required fields.
	if req.FirstName == "" || req.LastName == "" || req.Email == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "missing required fields",
		})
		return
	}

	user, err := h.service.CreateUser(ctx, req.FirstName, req.LastName, req.Email, req.PhoneNumber, req.AddressID, &req.Password, nil)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	// If user has an address ID, fetch the address details.
	var address *addressDomain.Address
	if user.AddressID != nil {
		address, _ = h.addressService.GetAddressByID(c.Request.Context(), *user.AddressID)
	}

	res := NewUserResponse(user, address)
	c.JSON(http.StatusOK, res)
}

// VerifyEmailHandler validates the verification token and marks the user as verified.
func (h *UserHandler) VerifyEmailHandler(c *gin.Context) {
	// Get the token from the query parameter.
	tokenStr := c.Query("token")
	if tokenStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token is required"})
		return
	}

	// Load the JWT secret from the environment.
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server misconfiguration"})
		return
	}

	// Parse and validate the token.
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		// Ensure the signing method is HMAC.
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		slog.WarnContext(c.Request.Context(), "invalid verification token", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token"})
		return
	}

	// Validate token claims.
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token claims"})
		return
	}

	// Check for a valid subject (user id) and the correct purpose.
	userID, ok := claims["sub"].(string)
	if !ok || userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token subject"})
		return
	}
	if claims["purpose"] != "email_verification" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token purpose"})
		return
	}

	// Call the service to mark the user as verified.
	err = h.service.VerifyUserEmail(c.Request.Context(), userID)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "failed to verify email", "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify email"})
		return
	}

	c.Redirect(http.StatusFound, os.Getenv("REDIRECT_EMAIL_VERIFY_SUCCESSFUL"))
}

func (h *UserHandler) LoginHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req LoginRequest

	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"invalid request payload": err.Error()})
		return
	}

	user, accessToken, refreshToken, err := h.service.Login(ctx, req.Email, req.Password, h.jwtSecret)
	if err != nil {
		slog.WarnContext(ctx, "login failed", "error", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// If user has an address ID, fetch the address details.
	var address *addressDomain.Address
	if user.AddressID != nil {
		address, _ = h.addressService.GetAddressByID(c.Request.Context(), *user.AddressID)
	}

	setAuthCookies(c, *accessToken, *refreshToken)

	c.JSON(http.StatusOK, NewLoginResponse(user, address))
}

func (h *UserHandler) LogoutHandler(c *gin.Context) {
	ctx := c.Request.Context()

	// Retrieve and invalidate refresh token
	refreshToken, _ := c.Cookie("refresh_token")
	if refreshToken != "" {
		if err := h.service.InvalidateRefreshToken(ctx, refreshToken); err != nil {
			slog.ErrorContext(ctx, "failed to invalidate refresh token", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to complete logout. Please try again.",
			})
			return
		}
	}

	// Cache headers to prevent caching of logout response
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")

	// Clear authentication cookies
	clearAuthCookies(c)

	// Return success response
	c.JSON(http.StatusOK, gin.H{
		"message": "Logged out successfully",
	})
}

func (h *UserHandler) ForgotPasswordHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid email is required"})
		return
	}

	if err := h.service.RequestPasswordReset(ctx, req.Email); err != nil {
		slog.ErrorContext(ctx, "password reset request failed", "error", err)
	}

	// Always return 200 to prevent email enumeration
	c.JSON(http.StatusOK, gin.H{"message": "If an account with that email exists, a password reset link has been sent."})
}

func (h *UserHandler) ResetPasswordHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token and password are required"})
		return
	}

	if err := h.service.ResetPassword(ctx, req.Token, req.Password); err != nil {
		slog.WarnContext(ctx, "password reset failed", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	clearAuthCookies(c)
	c.JSON(http.StatusOK, gin.H{"message": "Password has been reset successfully."})
}

func (h *UserHandler) GoogleAuthHandler(c *gin.Context) {
	state, err := generateStateToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}
	c.SetCookie("oauth_state", state, 60, "/", "", true, true)
	c.Redirect(http.StatusFound, oauth2.GetGoogleAuthURL(state))
}

func (h *UserHandler) GoogleAuthCallbackHandler(c *gin.Context) {
	ctx := c.Request.Context()

	// Validate state.
	state := c.Query("state")
	storedState, err := c.Cookie("oauth_state")
	if err != nil || state != storedState {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid state parameter"})
		return
	}

	// Exchange code for token.
	code := c.Query("code")
	token, err := oauth2.GoogleOAuthConfig.Exchange(ctx, code)
	if err != nil {
		slog.ErrorContext(ctx, "failed to exchange OAuth code", "provider", "google", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exchange code"})
		return
	}

	// Fetch user info from Google.
	type GoogleUserInfo struct {
		Sub        string `json:"sub"`
		Email      string `json:"email"`
		GivenName  string `json:"given_name"`
		FamilyName string `json:"family_name"`
	}
	client := oauth2.GoogleOAuthConfig.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch user info", "provider", "google", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user info"})
		return
	}
	defer resp.Body.Close()

	var googleUser GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&googleUser); err != nil {
		slog.ErrorContext(ctx, "failed to parse user info", "provider", "google", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse user info"})
		return
	}

	// Prepare the auth request DTO.
	req := GoogleAuthRequest{
		GoogleID:  googleUser.Sub,
		Email:     googleUser.Email,
		FirstName: googleUser.GivenName,
		LastName:  googleUser.FamilyName,
	}

	var user *domain.User

	// 1. Try to find the user by Google ID.
	user, err = h.service.GetUserByGoogleID(ctx, req.GoogleID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.ErrorContext(ctx, "error finding user by google ID", "provider", "google", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error finding user by Google ID"})
		return
	}

	// 2. If not found by Google ID, try to find by email.
	if user == nil {
		user, err = h.service.GetUserByEmail(ctx, req.Email)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.ErrorContext(ctx, "error finding user by email", "provider", "google", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error finding user by email"})
			return
		}

		// 3a. If still not found, create a new user.
		if user == nil {
			user, err = h.service.CreateUser(ctx, req.FirstName, req.LastName, req.Email, nil, nil, nil, &req.GoogleID)
			if err != nil {
				slog.ErrorContext(ctx, "failed to create user via OAuth", "provider", "google", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
				return
			}
		} else {
			// 3b. If found by email but without a Google ID, update the user.
			if user.GoogleID == nil {
				user, err = h.service.UpdateGoogleID(ctx, user.ID.String(), req.GoogleID)
				if err != nil {
					slog.ErrorContext(ctx, "failed to update google ID", "provider", "google", "error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Google ID"})
					return
				}

				// Send account linked notification email
				go func() {
					if emailErr := es.SendAccountLinkedEmail(*user, "fr"); emailErr != nil {
						slog.ErrorContext(ctx, "failed to send account linked email", "provider", "google", "error", emailErr)
					}
				}()
			}
		}
	}

	// Generate tokens.
	accessToken, refreshToken, err := h.service.GenerateTokens(ctx, *user, h.jwtSecret)
	if err != nil {
		slog.ErrorContext(ctx, "failed to generate token", "provider", "google", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	// Set tokens as cookies and redirect.
	setAuthCookies(c, accessToken, refreshToken)
	c.Redirect(http.StatusFound, os.Getenv("REDIRECT_LOGIN_SUCCESSFUL"))
}

func (h *UserHandler) RefreshTokenHandler(c *gin.Context) {
	// 1. Get refresh token from cookie
	refreshToken, err := c.Cookie("refresh_token")
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	// 2. Process token refresh
	newAccessToken, newRefreshToken, user, err := h.service.RefreshToken(
		c.Request.Context(),
		refreshToken,
		h.jwtSecret,
	)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session expired"})
		return
	}

	// 3. Set new cookies
	setAuthCookies(c, newAccessToken, newRefreshToken)

	// 4. Return minimal user data
	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":    user.ID,
			"email": user.Email,
		},
	})
}

func generateStateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate state token: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
