package interfaces

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tsb-service/internal/modules/hubrise_webshop/application"
)

// OAuthHandler exposes the HubRise OAuth authorize + callback routes.
type OAuthHandler struct {
	svc              *application.OAuthService
	dashboardBaseURL string
}

// NewOAuthHandler wires a new handler.
func NewOAuthHandler(svc *application.OAuthService, dashboardBaseURL string) *OAuthHandler {
	return &OAuthHandler{svc: svc, dashboardBaseURL: dashboardBaseURL}
}

// Authorize redirects the admin to the HubRise authorize URL.
func (h *OAuthHandler) Authorize(c *gin.Context) {
	state := c.Query("state")
	c.Redirect(http.StatusFound, h.svc.BuildAuthorizeURL(state))
}

// Callback handles the redirect from HubRise with the auth code.
func (h *OAuthHandler) Callback(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing code"})
		return
	}

	tok, err := h.svc.ExchangeCode(c.Request.Context(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Redirect back to dashboard with connection info.
	if h.dashboardBaseURL != "" {
		c.Redirect(http.StatusFound, h.dashboardBaseURL+"/settings?hubrise=connected")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"account_name":  tok.AccountName,
		"location_name": tok.LocationName,
		"catalog_name":  tok.CatalogName,
	})
}
