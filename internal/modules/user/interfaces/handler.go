package interfaces

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"tsb-service/internal/modules/user/application"
	"tsb-service/internal/modules/user/domain"

	"tsb-service/pkg/oauth2"

	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	service   application.UserService
	jwtSecret string
}

func NewUserHandler(service application.UserService, jwtSecret string) *UserHandler {
	return &UserHandler{
		service:   service,
		jwtSecret: jwtSecret,
	}
}

func (h *UserHandler) RegisterHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req RegistrationRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload", "details": err.Error()})
		return
	}

	// Validate required fields.
	if req.Name == "" || req.Email == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "missing required fields",
			"details": "name, email and password are required",
		})
		return
	}

	user, err := h.service.CreateUser(ctx, req.Email, req.Name, &req.Password, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user", "details": err.Error()})
		return
	}

	res := NewUserResponse(user)
	c.JSON(http.StatusOK, res)
}

func (h *UserHandler) LoginHandler(c *gin.Context) {
	ctx := c.Request.Context()

	var req LoginRequest

	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"invalid request payload": err.Error()})
	}

	user, accessToken, refreshToken, err := h.service.Login(ctx, req.Email, req.Password, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	res := NewLoginResponse(user, *accessToken)

	c.SetCookie("refresh_token", *refreshToken, 7*24*3600, "/", "", true, true)
	c.JSON(http.StatusOK, &res)
}

func (h *UserHandler) LogoutHandler(c *gin.Context) {
	ctx := c.Request.Context()

	// Ensure POST method for security
	if c.Request.Method != http.MethodPost {
		c.JSON(http.StatusMethodNotAllowed, gin.H{
			"error": "Method not allowed. Use POST for logout",
		})
		return
	}

	// Retrieve and validate refresh token
	refreshToken, _ := c.Cookie("refresh_token")
	if refreshToken != "" {
		if err := h.service.InvalidateRefreshToken(ctx, refreshToken); err != nil {
			log.Printf("Failed to invalidate refresh token: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to complete logout. Please try again.",
			})
			return
		}

		// Audit log the logout event
		// if userID, err := h.service.GetUserIDFromToken(ctx, refreshToken); err == nil {
		// 	log.Printf("User %s logged out", userID)
		// }
	}

	// Security headers
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-XSS-Protection", "1; mode=block")

	// Clear cookies with security headers
	domain := ""
	if parsedURL, err := url.Parse(os.Getenv("APP_BASE_URL")); err == nil {
		domain = parsedURL.Hostname()
	}

	c.SetCookie("refresh_token", "", -1, "/", domain, true, true)
	c.SetCookie("auth", "", -1, "/", domain, true, true)

	// Validate and sanitize redirect URL
	baseURL := os.Getenv("APP_BASE_URL")
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
		log.Printf("Invalid APP_BASE_URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Server configuration error. Please contact support.",
		})
		return
	}

	// Construct safe redirect URL
	redirectURL := parsedBaseURL.String()
	if !strings.HasSuffix(redirectURL, "/") {
		redirectURL += "/"
	}
	redirectURL += "login"

	// Perform secure redirect
	c.Redirect(http.StatusSeeOther, redirectURL)
}

func (h *UserHandler) GoogleAuthHandler(c *gin.Context) {
	state := generateStateToken()
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exchange code", "details": err.Error()})
		return
	}

	// Fetch user info from Google.
	type GoogleUserInfo struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	client := oauth2.GoogleOAuthConfig.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user info", "details": err.Error()})
		return
	}
	defer resp.Body.Close()

	var googleUser GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&googleUser); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse user info", "details": err.Error()})
		return
	}

	// Build DTO.
	req := GoogleAuthRequest{
		GoogleID: googleUser.Sub,
		Email:    googleUser.Email,
		Name:     googleUser.Name,
	}

	// Look up the user.

	user, err := h.service.GetUserByGoogleID(ctx, req.GoogleID)
	if err == nil {
		// User found by Google ID – continue.
	} else if !errors.Is(err, domain.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to find user by Google ID", "details": err.Error()})
		return
	} else {
		// Not found by Google ID – try finding by email.
		user, err = h.service.GetUserByEmail(ctx, req.Email)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to find user by email", "details": err.Error()})
			return
		}
		if err != nil && errors.Is(err, domain.ErrNotFound) {
			// User not found by email – create new user.
			user, err = h.service.CreateUser(ctx, req.Email, req.Name, nil, &req.GoogleID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user", "details": err.Error()})
				return
			}
		} else {
			// User found by email – update with Google ID.
			user, err = h.service.UpdateGoogleID(ctx, user.ID.String(), req.GoogleID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Google ID", "details": err.Error()})
				return
			}
		}
	}

	// Generate tokens.
	_, refreshToken, err := h.service.GenerateTokens(ctx, user.ID.String(), h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token", "details": err.Error()})
		return
	}

	// Set refresh token cookie and redirect.
	c.SetCookie("refresh_token", refreshToken, 7*24*3600, "/", "", true, true)
	c.Redirect(http.StatusFound, os.Getenv("REDIRECT_LOGIN_SUCCESSFUL"))
}

func (h *UserHandler) RefreshTokenHandler(c *gin.Context) {
	refreshToken, err := c.Cookie("refresh_token")
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "No refresh token provided"})
		return
	}

	newAccessToken, userDetails, err := h.service.RefreshToken(c.Request.Context(), refreshToken, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"accessToken": newAccessToken,
		"user":        userDetails,
	})
}

func generateStateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}
