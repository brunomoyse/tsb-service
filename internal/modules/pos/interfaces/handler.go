package interfaces

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/pos/application"
)

type Handler struct {
	svc *application.Service
}

func NewHandler(svc *application.Service) *Handler {
	return &Handler{svc: svc}
}

// ---- DTOs

type deviceLoginDTO struct {
	DeviceID  string `json:"deviceId"  binding:"required,uuid"`
	Timestamp int64  `json:"timestamp" binding:"required"`
	Nonce     string `json:"nonce"     binding:"required,min=16,max=128"`
	HMAC      string `json:"hmac"      binding:"required"`
}

type accessTokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
	DeviceID    string `json:"deviceId"`
}

type fcmTokenDTO struct {
	DeviceID  string `json:"deviceId"  binding:"required,uuid"`
	FCMToken  string `json:"fcmToken"  binding:"required"`
	Timestamp int64  `json:"timestamp" binding:"required"`
	Nonce     string `json:"nonce"     binding:"required"`
	HMAC      string `json:"hmac"      binding:"required"`
}

// ---- endpoints

// DeviceLogin trades an HMAC-signed proof of possession for an access token.
// No human user is involved; the device IS the principal.
func (h *Handler) DeviceLogin(c *gin.Context) {
	var body deviceLoginDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deviceID, err := uuid.Parse(body.DeviceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deviceId"})
		return
	}
	tok, err := h.svc.DeviceLogin(c.Request.Context(), application.DeviceLoginInput{
		DeviceID:  deviceID,
		Timestamp: body.Timestamp,
		Nonce:     body.Nonce,
		HMAC:      body.HMAC,
	})
	if err != nil {
		respondAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, accessTokenResponse{
		AccessToken: tok.Token,
		ExpiresIn:   tok.ExpiresIn,
		DeviceID:    tok.DeviceID.String(),
	})
}

// UpdateFCMToken registers or refreshes the FCM push token for a device.
// HMAC-signed identically to DeviceLogin.
func (h *Handler) UpdateFCMToken(c *gin.Context) {
	var body fcmTokenDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deviceID, err := uuid.Parse(body.DeviceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deviceId"})
		return
	}
	if err := h.svc.UpdateDeviceFCMToken(c.Request.Context(), application.FCMTokenInput{
		DeviceID:  deviceID,
		FCMToken:  body.FCMToken,
		Timestamp: body.Timestamp,
		Nonce:     body.Nonce,
		HMAC:      body.HMAC,
	}); err != nil {
		respondAuthError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func respondAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, application.ErrDeviceNotEnrolled),
		errors.Is(err, application.ErrDeviceRevoked),
		errors.Is(err, application.ErrInvalidHMAC),
		errors.Is(err, application.ErrStaleRequest):
		c.JSON(http.StatusForbidden, gin.H{"error": "device not authorized"})
	default:
		zap.L().Warn("pos auth error", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
