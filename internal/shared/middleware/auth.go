package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"tsb-service/pkg/utils"
)

// AuthMiddleware parses and validates a JWT (cookie or Authorization header),
// aborting the request with 401 if the token is missing or invalid.
// On success, it stores userID and isAdmin in the request context.
func AuthMiddleware(secretKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var tokenStr string

		// 1) Try cookie first
		if cookie, err := c.Cookie("access_token"); err == nil && cookie != "" {
			tokenStr = cookie
		} else if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			// 2) Fallback to Authorization header
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		}

		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}

		// 3) Parse and validate the token
		claims := &jwt.RegisteredClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secretKey), nil
		})

		// 4) Handle parsing/validation errors
		if err != nil {
			var ve *jwt.ValidationError
			if err == jwt.ErrSignatureInvalid {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token signature"})
			} else if errors.As(err, &ve) && ve.Errors&jwt.ValidationErrorExpired != 0 {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token expired"})
			} else {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			}
			return
		}

		if !token.Valid || claims.Subject == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		// 5) Store userID and isAdmin in context using shared utils
		ctx := utils.SetUserID(c.Request.Context(), claims.Subject)
		isAdmin := len(claims.Audience) > 0 && claims.Audience[0] == "admin"
		ctx = utils.SetIsAdmin(ctx, isAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Set(string(utils.UserIDKey), claims.Subject)

		c.Next()
	}
}
