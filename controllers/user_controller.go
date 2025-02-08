package controllers

import (
	"net/http"
	"time"
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

	// Generate a new access token with a 15-minute expiration
	newAccessTokenExpiration := time.Now().Add(15 * time.Minute).Unix()

	newAccessClaims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Unix(newAccessTokenExpiration, 0)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Subject:   claims.Subject, // User ID from refresh token
	}

	// Create the new access token
	newAccessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newAccessClaims)
	newAccessTokenString, err := newAccessToken.SignedString([]byte(secretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate new access token"})
		return
	}

	// Return the new access token
	c.JSON(http.StatusOK, gin.H{"accessToken": newAccessTokenString})
}