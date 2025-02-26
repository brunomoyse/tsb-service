// internal/user/handler/handler.go
package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"

	"tsb-service/internal/user"
	"tsb-service/internal/user/service"
	"tsb-service/pkg/oauth2"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service service.UserService
}

func NewHandler(s service.UserService) *Handler {
	return &Handler{service: s}
}

func (h *Handler) SignUp(c *gin.Context) {
	var input user.UserRegister
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.service.SignUp(input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *Handler) SignIn(c *gin.Context) {
	var input user.UserLogin
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dbUser, accessToken, refreshToken, err := h.service.SignIn(input)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	c.SetCookie("refresh_token", refreshToken, 7*24*3600, "/", "", true, true)
	c.JSON(http.StatusOK, gin.H{
		"accessToken": accessToken,
		"user": gin.H{
			"id":    dbUser.ID,
			"name":  dbUser.Name,
			"email": dbUser.Email,
		},
	})
}

func (h *Handler) HandleGoogleAuthLogin(c *gin.Context) {
	state := generateStateToken()
	c.SetCookie("oauth_state", state, 60, "/", "", true, true)
	c.Redirect(http.StatusFound, oauth2.GetGoogleAuthURL(state))
}

func (h *Handler) HandleGoogleAuthCallback(c *gin.Context) {
	state := c.Query("state")
	storedState, err := c.Cookie("oauth_state")
	if err != nil || state != storedState {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid state parameter"})
		return
	}

	code := c.Query("code")
	token, err := oauth2.GoogleOAuthConfig.Exchange(context.Background(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exchange code"})
		return
	}

	client := oauth2.GoogleOAuthConfig.Client(context.Background(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user info"})
		return
	}
	defer resp.Body.Close()

	var userInfo map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&userInfo)

	googleUser := user.GoogleUser{
		GoogleID: userInfo["sub"].(string),
		Email:    userInfo["email"].(string),
		Name:     userInfo["name"].(string),
	}

	dbUser, err := h.service.HandleGoogleUser(googleUser)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to handle Google user"})
		return
	}

	_, refreshToken, err := h.service.GenerateTokens(dbUser.ID.String())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.SetCookie("refresh_token", refreshToken, 60*60*24*7, "/", "", true, true)
	c.Redirect(http.StatusFound, os.Getenv("REDIRECT_LOGIN_SUCCESSFUL"))
}

func (h *Handler) RefreshToken(c *gin.Context) {
	refreshToken, err := c.Cookie("refresh_token")
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "No refresh token provided"})
		return
	}

	newAccessToken, userDetails, err := h.service.RefreshToken(refreshToken)
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
