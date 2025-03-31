package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

// AuthMiddleware validates JWTs for protected routes
func AuthMiddleware(secretKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr, err := c.Cookie("access_token")
		if err != nil || tokenStr == "" {
			// Optionally fallback to Authorization header
			authHeader := c.GetHeader("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}

		// Parse and validate the token
		claims := &jwt.RegisteredClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
			// Ensure the signing method is HS256
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secretKey), nil
		})

		// Handle token validation errors
		if err != nil {
			if err == jwt.ErrSignatureInvalid {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token signature"})
			} else if err.(*jwt.ValidationError).Errors&jwt.ValidationErrorExpired != 0 {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "token expired"})
			} else {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			}
			c.Abort()
			return
		}

		// Ensure token is valid
		if !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			c.Abort()
			return
		}

		// Extract user ID from the claims
		userId := claims.Subject
		if userId == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing user ID in token"})
			c.Abort()
			return
		}

		// Store the user ID in the context for later use
		type contextKey string
		const userKey contextKey = "userID"
		ctx := context.WithValue(c.Request.Context(), userKey, userId)
		c.Request = c.Request.WithContext(ctx)
		c.Set("userID", userId)

		// Continue with the request
		c.Next()
	}
}
