package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"tsb-service/pkg/types"
	"tsb-service/pkg/utils"
)

// OptionalAuthMiddleware parses a JWT if present, and on success
// stores the userID in the request context. It never aborts.
func OptionalAuthMiddleware(secretKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var tokenStr string

		// 1) Try cookie first
		if cookie, err := c.Cookie("access_token"); err == nil && cookie != "" {
			tokenStr = cookie
		} else if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			// 2) Fallback to Authorization header
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		}

		if tokenStr != "" {
			claims := &types.JwtClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (any, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return []byte(secretKey), nil
			})

			if err == nil && token.Valid && claims.Subject != "" {
				ctxWithUser := utils.SetUserID(c.Request.Context(), claims.Subject)
				ctxWithUser = utils.SetIsAdmin(ctxWithUser, claims.IsAdmin)
				c.Request = c.Request.WithContext(ctxWithUser)

				// (optional) also set in Gin if you ever need c.Get("userID")
				c.Set(string(utils.UserIDKey), claims.Subject)
			}
		}

		c.Next()
	}
}
