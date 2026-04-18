package middleware

import (
	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-gonic/gin"

	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// SentryContext propagates request_id and user_id (when present) to the
// Sentry hub scope so they appear as tags on every captured event. Run it
// after RequestIDMiddleware, and auth middleware for user_id to be set.
func SentryContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		if hub := sentrygin.GetHubFromContext(c); hub != nil {
			hub.ConfigureScope(func(scope *sentry.Scope) {
				if rid := logging.GetRequestID(c.Request.Context()); rid != "" {
					scope.SetTag("request_id", rid)
				}
				if uid := utils.GetUserID(c.Request.Context()); uid != "" {
					scope.SetUser(sentry.User{ID: uid})
				}
			})
		}
		c.Next()
	}
}
