package interfaces

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tsb-service/internal/modules/hubrise_webshop/application"
)

// HealthHandler exposes GET /api/v1/hubrise/webshop/health.
//
// The endpoint is intentionally public (no OIDC): the cron
// `cmd/hubrise-health-check` binary and the dashboard `useHubriseHealth`
// composable both consume it. The payload contains only aggregated
// counts — no PII — so leaking it is harmless.
type HealthHandler struct {
	svc *application.HealthService
}

func NewHealthHandler(svc *application.HealthService) *HealthHandler {
	return &HealthHandler{svc: svc}
}

// Handle returns 200 for `ok`/`degraded` and 503 for `down` so that
// off-the-shelf uptime monitors (e.g. Pingdom) flag outages without
// needing to parse the JSON body.
func (h *HealthHandler) Handle(c *gin.Context) {
	snap, err := h.svc.Check(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	code := http.StatusOK
	if snap.Status == application.HealthDown {
		code = http.StatusServiceUnavailable
	}
	c.JSON(code, snap)
}
