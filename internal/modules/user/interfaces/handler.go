package interfaces

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"

	"tsb-service/internal/modules/user/application"

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

func (h *UserHandler) GoogleAuthHandler(c *gin.Context) {
	state := generateStateToken()
	c.SetCookie("oauth_state", state, 60, "/", "", true, true)
	c.Redirect(http.StatusFound, oauth2.GetGoogleAuthURL(state))
}

func (h *UserHandler) GoogleAuthCallbackHandler(c *gin.Context) {
	ctx := c.Request.Context()

	// Validate state parameter.
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

	// Define a struct that matches the JSON returned by Google's userinfo endpoint.
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

	req := GoogleAuthRequest{
		GoogleID: googleUser.Sub,
		Email:    googleUser.Email,
		Name:     googleUser.Name,
	}

	// Try to find an existing user by Google ID.
	user, err := h.service.GetUserByGoogleID(ctx, req.GoogleID)
	if err != nil {
		// If not found by GoogleID, try by email.
		user, err = h.service.GetUserByEmail(ctx, req.Email)
		if err != nil {
			// If still not found, create a new user.
			user, err = h.service.CreateUser(ctx, req.Email, req.Name, nil, &req.GoogleID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user", "details": err.Error()})
				return
			}
		} else {
			// If found by email, update the Google ID.
			user, err = h.service.UpdateGoogleID(ctx, user.ID.String(), req.GoogleID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Google ID", "details": err.Error()})
				return
			}
		}
	}

	// Generate tokens for the user.
	_, refreshToken, err := h.service.GenerateTokens(ctx, user.ID.String(), h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token", "details": err.Error()})
		return
	}

	// Set the refresh token as a secure cookie and redirect.
	c.SetCookie("refresh_token", refreshToken, 60*60*24*7, "/", "", true, true)
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
