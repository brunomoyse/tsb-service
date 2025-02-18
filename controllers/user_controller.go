package controllers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
	"tsb-service/config"
	"tsb-service/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

func SignUp(c *gin.Context) {
	// Get the JSON body
	var json models.UserRegister

	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create the user
	user, err := models.SignUp(json)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

func SignIn(c *gin.Context) {
	// Parse JSON request body
	var json models.UserLogin

	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Authenticate the user
	user, accessToken, refreshToken, err := models.AuthenticateUser(json)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	// Set refresh token as an HTTP-only cookie
	c.SetCookie("refresh_token", refreshToken, 7*24*3600, "/auth/refresh", "", true, true)

	// Return access token and user info
	c.JSON(http.StatusOK, gin.H{
		"accessToken": accessToken,
		"user": gin.H{
			"id":    user.ID,
			"name":  user.Name,
			"email": user.Email,
		},
	})
}

func HandleGoogleAuthLogin(c *gin.Context) {
	state := generateStateToken()

	// Store state token in a secure HTTP-only cookie
	c.SetCookie("oauth_state", state, 60, "/", "", true, true)

	// Generate Google login URL with state parameter
	authURL := config.GetGoogleAuthURL(state)
	c.Redirect(http.StatusFound, authURL)
}

// HandleGoogleAuthCallback handles Google's OAuth callback
func HandleGoogleAuthCallback(c *gin.Context) {
	// Get the state parameter returned by Google
	state := c.Query("state")

	// Get the stored state token from the cookie
	storedState, err := c.Cookie("oauth_state")
	if err != nil || state != storedState {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid state parameter"})
		return
	}

	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No code provided"})
		return
	}

	// Exchange authorization code for tokens
	token, err := config.GoogleOAuthConfig.Exchange(context.Background(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exchange code"})
		return
	}

	// Fetch user info from Google
	client := config.GoogleOAuthConfig.Client(context.Background(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user info"})
		return
	}
	defer resp.Body.Close()

	var userInfo map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&userInfo)

	// Parse Google user info
	googleUser := models.GoogleUser{
		GoogleID: userInfo["sub"].(string),
		Email:    userInfo["email"].(string),
		Name:     userInfo["name"].(string),
	}

	// Fetch or create user in the database
	user, err := models.HandleGoogleUser(googleUser)
	if err != nil {
		fmt.Println("Error HandleGoogleUser:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to handle Google user"})
		return
	}

	// Generate JWT access & refresh tokens
	_, refreshToken, err := models.GenerateJWT(user.ID.String())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	// Store refresh token in a Secure HTTP-only cookie
	c.SetCookie("refresh_token", refreshToken, 60*60*24*7, "/", "", true, true) // 7 days

	// Redirect user to frontend (without exposing access token in URL)
	frontendURL := os.Getenv("REDIRECT_LOGIN_SUCCESSFUL") // "http://localhost:3000/menu"
	c.Redirect(http.StatusFound, frontendURL)
}

// RefreshTokenHandler renews the access token using the refresh token from the HTTP-only cookie
func RefreshTokenHandler(c *gin.Context, secretKey string) {
	// Get refresh token from HTTP-only cookie
	refreshToken, err := c.Cookie("refresh_token")
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "No refresh token provided"})
		return
	}

	// Parse the refresh token
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(secretKey), nil
	})

	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired refresh token"})
		return
	}

	// Extract user ID from refresh token
	userID := claims.Subject
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token: missing user ID"})
		return
	}

	// Fetch user from database
	user, err := models.GetUserById(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
		return
	}

	// Generate a new access token with a 15-minute expiration
	newAccessTokenExpiration := time.Now().Add(15 * time.Minute).Unix()

	newAccessClaims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Unix(newAccessTokenExpiration, 0)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Subject:   userID, // Keep user ID in the token
	}

	// Create the new access token
	newAccessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newAccessClaims)
	newAccessTokenString, err := newAccessToken.SignedString([]byte(secretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate new access token"})
		return
	}

	// Return the new access token along with user details
	c.JSON(http.StatusOK, gin.H{
		"accessToken": newAccessTokenString,
		"user": gin.H{
			"id":    user.ID,
			"name":  user.Name,
			"email": user.Email,
		},
	})
}

func generateStateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}
