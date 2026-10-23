// Package interfaces holds the HTTP side of the HubRise integration: the callback endpoint HubRise
// posts its events to.
package interfaces

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// maxBody caps the callback body. HubRise events are a few kilobytes; the order states are the
// largest.
const maxBody = 1 << 20

// FailureLimiter rate limits by key; false means the key went over its allowance.
// middleware.RateLimiter satisfies it.
type FailureLimiter interface {
	AllowKey(key string) bool
}

// CallbackHandler serves POST /hubrise/callback.
type CallbackHandler struct {
	receiver *application.CallbackReceiver
	failures FailureLimiter
}

// NewCallbackHandler returns the handler. failures counts the requests whose signature does not
// verify (30 a minute per client address in production); it may be nil to count nothing. Requests
// with a valid signature never touch it, so a burst of retries from HubRise, which loses an event
// after six of them, cannot be answered with 429 because of other traffic.
func NewCallbackHandler(receiver *application.CallbackReceiver, failures FailureLimiter) *CallbackHandler {
	return &CallbackHandler{receiver: receiver, failures: failures}
}

// Register mounts the endpoint on the API group: POST /hubrise/callback.
func (h *CallbackHandler) Register(api gin.IRoutes) { api.POST("/hubrise/callback", h.Callback) }

// Callback verifies, stores and acknowledges one event. HubRise treats 200 to 499 as delivered and
// retries on 5xx or a timeout, so only a failure of ours (the database) is answered with 500.
func (h *CallbackHandler) Callback(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "unreadable or too large body"})
		return
	}
	_, err = h.receiver.Receive(c.Request.Context(), body, c.GetHeader(hubrise.SignatureHeader))
	switch {
	case err == nil:
		c.Status(http.StatusOK)
	case errors.Is(err, application.ErrBadSignature):
		if h.failures != nil && !h.failures.AllowKey(c.ClientIP()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
	case errors.Is(err, application.ErrBadEvent):
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "malformed event"})
	case errors.Is(err, application.ErrNotConnected):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not connected"})
	case errors.Is(err, application.ErrWrongLocation):
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "unknown location"})
	default:
		logging.FromContext(c.Request.Context()).Error("hubrise: cannot store a callback event", zap.Error(err))
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
