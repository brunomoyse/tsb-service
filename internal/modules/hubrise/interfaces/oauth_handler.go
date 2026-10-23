package interfaces

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// The values of the hubrise query parameter the browser lands on the dashboard with. They name the
// outcome only: never a code, a token or an error text.
const (
	OutcomeConnected     = "connected"
	OutcomeRelinkNeeded  = "relink_required"
	OutcomeDenied        = "denied"
	OutcomeInvalidState  = "invalid_state"
	OutcomeNotAuthorized = "authorization_failed"
	OutcomeError         = "error"
)

// OAuthHandler serves GET /hubrise/oauth/callback, where HubRise sends the browser back after the
// admin approved (or refused) the connection.
type OAuthHandler struct {
	admin        *application.Admin
	dashboardURL string
	failures     FailureLimiter
}

// NewOAuthHandler returns the handler. dashboardURL is where the browser is sent afterwards, with
// ?hubrise=<outcome> (APP_DASHBOARD_URL, or the dashboard's HubRise page). failures counts the
// callbacks whose state is unknown (30 a minute per client address in production); it may be nil.
func NewOAuthHandler(admin *application.Admin, dashboardURL string, failures FailureLimiter) *OAuthHandler {
	return &OAuthHandler{admin: admin, dashboardURL: dashboardURL, failures: failures}
}

// Register mounts the endpoint on the API group: GET /hubrise/oauth/callback. It is public (the
// browser lands on it with no bearer token); the one-time state bound to the admin who started the
// connection is the authentication.
func (h *OAuthHandler) Register(api gin.IRoutes) { api.GET("/hubrise/oauth/callback", h.Callback) }

// Callback completes the connection and redirects to the dashboard.
func (h *OAuthHandler) Callback(c *gin.Context) {
	q := c.Request.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	switch {
	case state == "" || (code == "" && q.Get("error") == ""):
		h.redirect(c, h.failedState(c))
		return
	case q.Get("error") != "": // the admin refused, or HubRise did
		h.redirect(c, OutcomeDenied)
		return
	}
	// The client address goes into the audit entry of the connection.
	ctx := utils.SetClientIP(c.Request.Context(), c.ClientIP())
	res, err := h.admin.CompleteConnection(ctx, state, code)
	switch {
	case err == nil && res.RelinkRequired:
		h.redirect(c, OutcomeRelinkNeeded)
	case err == nil:
		h.redirect(c, OutcomeConnected)
	case errors.Is(err, domain.ErrInvalidState):
		h.redirect(c, h.failedState(c))
	case errors.Is(err, domain.ErrAuthorizationFailed):
		logging.FromContext(ctx).Warn("hubrise: authorization failed", zap.Error(err))
		h.redirect(c, OutcomeNotAuthorized)
	default:
		logging.FromContext(ctx).Error("hubrise: cannot complete the connection", zap.Error(err))
		h.redirect(c, OutcomeError)
	}
}

// failedState counts an unusable state against the client's address and says what to redirect
// with. Past the limit the request is not redirected but refused.
func (h *OAuthHandler) failedState(c *gin.Context) string {
	if h.failures != nil && !h.failures.AllowKey(c.ClientIP()) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		return ""
	}
	return OutcomeInvalidState
}

func (h *OAuthHandler) redirect(c *gin.Context, outcome string) {
	if outcome == "" { // already answered (rate limited)
		return
	}
	target, err := url.Parse(h.dashboardURL)
	if err != nil || target.Host == "" {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "the dashboard address is not configured"})
		return
	}
	q := target.Query()
	q.Set("hubrise", outcome)
	target.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, target.String())
}
