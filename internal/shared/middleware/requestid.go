package middleware

import (
	"github.com/gin-gonic/gin"

	"tsb-service/pkg/logging"
)

// RequestIDMiddleware generates a unique request ID, stores it in context, and sets the X-Request-ID response header.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := logging.GenerateRequestID()
		ctx := logging.SetRequestID(c.Request.Context(), rid)
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Request-ID", rid)
		c.Next()
	}
}
