package feedback

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

type FeedbackRequest struct {
	Name         string `json:"name" binding:"required,max=100"`
	Email        string `json:"email" binding:"required,email,max=255"`
	ServiceType  string `json:"serviceType" binding:"required,oneof=takeaway dine-in delivery"`
	FeedbackType string `json:"feedbackType" binding:"required,oneof=improvement complaint compliment"`
	Message      string `json:"message" binding:"required,min=10,max=2000"`
	Website      string `json:"website"` // honeypot — should always be empty
}

func HandleFeedback(c *gin.Context) {
	log := logging.FromContext(c.Request.Context())

	var req FeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_input"})
		return
	}

	// Honeypot check: if the hidden field is filled, it's a bot.
	// Return 200 to not tip off the bot.
	if req.Website != "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	// Sanitize inputs
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Message = strings.TrimSpace(req.Message)

	// Extract language from context
	lang := utils.GetLang(c.Request.Context())

	if err := scaleway.SendFeedbackEmail(req.Name, req.Email, req.ServiceType, req.FeedbackType, req.Message, lang); err != nil {
		log.Error("failed to send feedback email", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "email_failed"})
		return
	}

	log.Info("feedback submitted",
		zap.String("service_type", req.ServiceType),
		zap.String("feedback_type", req.FeedbackType),
	)

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
