package interfaces

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tsb-service/internal/modules/hubrise_webshop/application"
	"tsb-service/internal/modules/hubrise_webshop/domain"
)

// AdminHandler exposes admin-only REST endpoints for dashboard use.
type AdminHandler struct {
	connRepo domain.ConnectionRepository
	syncRepo domain.CatalogSyncStateRepository
	pusher   *application.CatalogPusher
}

func NewAdminHandler(
	connRepo domain.ConnectionRepository,
	syncRepo domain.CatalogSyncStateRepository,
	pusher *application.CatalogPusher,
) *AdminHandler {
	return &AdminHandler{
		connRepo: connRepo,
		syncRepo: syncRepo,
		pusher:   pusher,
	}
}

// Status returns the current HubRise connection + catalog sync state.
// GET /api/v1/hubrise/webshop/status
func (h *AdminHandler) Status(c *gin.Context) {
	ctx := c.Request.Context()
	conn, err := h.connRepo.GetByClient(ctx, application.ClientName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	sync, err := h.syncRepo.Get(ctx, application.ClientName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{
		"connected": conn != nil,
	}
	if conn != nil {
		resp["account_id"] = conn.AccountID
		resp["location_id"] = conn.LocationID
		resp["catalog_id"] = conn.CatalogID
		resp["customer_list_id"] = conn.CustomerListID
		resp["scope"] = conn.Scope
	}
	if sync != nil {
		resp["last_pushed_version"] = sync.LastPushedVersion
		resp["last_pushed_at"] = sync.LastPushedAt
		resp["last_push_status"] = sync.LastPushStatus
		resp["last_error"] = sync.LastError
	}
	c.JSON(http.StatusOK, resp)
}

// PushCatalog manually triggers a catalog push.
// POST /api/v1/hubrise/webshop/catalog/push
func (h *AdminHandler) PushCatalog(c *gin.Context) {
	if err := h.pusher.Push(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "pushed"})
}

// Disconnect removes the stored OAuth token.
// POST /api/v1/hubrise/webshop/disconnect
func (h *AdminHandler) Disconnect(c *gin.Context) {
	if err := h.connRepo.Delete(c.Request.Context(), application.ClientName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "disconnected"})
}
