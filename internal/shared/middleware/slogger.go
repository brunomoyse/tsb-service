package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// SlogRequestLogger replaces gin.Logger() with structured request logging.
func SlogRequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		duration := time.Since(start)
		status := c.Writer.Status()
		path := c.Request.URL.Path

		attrs := []any{
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"duration", duration.String(),
			"client_ip", c.ClientIP(),
		}

		ctx := c.Request.Context()

		// Health check at debug level to reduce noise
		if path == "/api/v1/up" {
			slog.DebugContext(ctx, "request completed", attrs...)
			return
		}

		switch {
		case status >= 500:
			slog.ErrorContext(ctx, "request completed", attrs...)
		case status >= 400:
			slog.WarnContext(ctx, "request completed", attrs...)
		default:
			slog.InfoContext(ctx, "request completed", attrs...)
		}
	}
}
