package mcp

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tsb-service/pkg/utils"
)

// RequireAdmin is the Gin middleware mounted on /api/v1/mcp. It runs
// after the OIDC verifier has populated the context, and rejects any
// request whose verified principal does not carry the admin role.
//
// The same admin check is centralised here (rather than per-tool)
// because every tool exposes admin-only data — a non-admin caller
// should never get a JSON-RPC handshake at all.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		if utils.GetUserID(ctx) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "UNAUTHENTICATED: bearer token required",
			})
			return
		}
		if !utils.GetIsAdmin(ctx) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "FORBIDDEN: admin role required",
			})
			return
		}
		c.Next()
	}
}

// WrapHTTPHandler converts a net/http handler into a Gin handler while
// preserving the request context (which carries userID, isAdmin, lang,
// request_id). The MCP streamable transport is a stdlib handler, but
// the router is Gin.
func WrapHTTPHandler(h http.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
