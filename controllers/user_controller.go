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
	// Get the JSON body
	var json models.UserLogin

	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Authenticate the user
	user, err := models.SignIn(json)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

// RefreshToken handles generating a new access token using the refresh token
func RefreshToken(c *gin.Context, secretKey string) {
	var req models.RefreshTokenRequest

	// Bind the JSON request to the struct
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	refreshToken := req.RefreshToken

	// Parse the refresh token
	claims := &models.Claims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(secretKey), nil
	})

	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired refresh token"})
		return
	}

	// Generate a new access token with a 15-minute expiration
	newAccessTokenExpiration := time.Now().Add(15 * time.Minute).Unix()

	newAccessClaims := models.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Unix(newAccessTokenExpiration, 0)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   claims.Subject,
		},
	}

	// Create the new access token
	newAccessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newAccessClaims)
	newAccessTokenString, err := newAccessToken.SignedString([]byte(secretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate new access token"})
		return
	}

	// Return the new access token in the response
	c.JSON(http.StatusOK, models.TokenResponse{
		AccessToken:  newAccessTokenString,
		RefreshToken: refreshToken,
	})
}
