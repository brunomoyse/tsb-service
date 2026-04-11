package interfaces

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"tsb-service/internal/modules/hubrise_webshop/application"
)

// WebhookHandler accepts HubRise active callbacks. HMAC verification
// is performed using the shared client secret.
type WebhookHandler struct {
	svc          *application.WebhookService
	clientSecret []byte
}

func NewWebhookHandler(svc *application.WebhookService, clientSecret string) *WebhookHandler {
	return &WebhookHandler{svc: svc, clientSecret: []byte(clientSecret)}
}

// Handle is the Gin endpoint: POST /api/v1/hubrise/webshop/webhook
func (h *WebhookHandler) Handle(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body"})
		return
	}

	sig := c.GetHeader("X-HubRise-Hmac-SHA256")
	if len(h.clientSecret) > 0 {
		expected := computeHMAC(h.clientSecret, body)
		if !hmac.Equal([]byte(expected), []byte(sig)) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
			return
		}
	}

	if err := h.svc.Process(c.Request.Context(), body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func computeHMAC(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
