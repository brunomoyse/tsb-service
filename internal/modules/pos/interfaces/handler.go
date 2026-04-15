package interfaces

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/pos/application"
	"tsb-service/pkg/utils"
)

type Handler struct {
	svc *application.Service
}

func NewHandler(svc *application.Service) *Handler {
	return &Handler{svc: svc}
}

// ---- DTOs

type enrollDTO struct {
	Serial string `json:"serial" binding:"required"`
	Label  string `json:"label"  binding:"required"`
}

type enrollResponse struct {
	DeviceID     string `json:"deviceId"`
	DeviceSecret string `json:"deviceSecret"`
}

type rrnLoginDTO struct {
	DeviceID  string `json:"deviceId"  binding:"required,uuid"`
	RRN       string `json:"rrn"       binding:"required,len=11"`
	PIN       string `json:"pin"       binding:"required,min=4,max=6"`
	Timestamp int64  `json:"timestamp" binding:"required"`
	Nonce     string `json:"nonce"     binding:"required"`
	HMAC      string `json:"hmac"      binding:"required"`
}

type refreshDTO struct {
	DeviceID     string `json:"deviceId"     binding:"required,uuid"`
	RefreshToken string `json:"refreshToken" binding:"required"`
	Timestamp    int64  `json:"timestamp"    binding:"required"`
	Nonce        string `json:"nonce"        binding:"required"`
	HMAC         string `json:"hmac"         binding:"required"`
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	UserID       string `json:"userId"`
	IsAdmin      bool   `json:"isAdmin"`
}

// ---- endpoints

// Enroll requires a valid Zitadel admin JWT (StrictAuth + admin check upstream).
func (h *Handler) Enroll(c *gin.Context) {
	var body enrollDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !utils.GetIsAdmin(c.Request.Context()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin required"})
		return
	}
	adminID, err := uuid.Parse(utils.GetUserID(c.Request.Context()))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no admin user"})
		return
	}

	result, err := h.svc.EnrollDevice(c.Request.Context(), body.Serial, body.Label, adminID)
	if err != nil {
		zap.L().Warn("enrollDevice failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "enroll failed"})
		return
	}
	c.JSON(http.StatusOK, enrollResponse{
		DeviceID:     result.DeviceID.String(),
		DeviceSecret: result.DeviceSecret,
	})
}

// RrnLogin is the daily shop-floor sign-in; HMAC-signed, no Zitadel session.
func (h *Handler) RrnLogin(c *gin.Context) {
	var body rrnLoginDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deviceID, err := uuid.Parse(body.DeviceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deviceId"})
		return
	}

	// isAdmin is a property of the user, not the session; we let the service
	// compute it from Zitadel roles in a later pass. For now, defer to false
	// and rely on the admin flag being cached on the user row if needed.
	tokens, err := h.svc.RrnLogin(c.Request.Context(), application.RrnLoginInput{
		DeviceID:  deviceID,
		RRN:       body.RRN,
		PIN:       body.PIN,
		Timestamp: body.Timestamp,
		Nonce:     body.Nonce,
		HMAC:      body.HMAC,
	}, false)
	if err != nil {
		respondAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTokenResponse(tokens))
}

// Refresh trades a valid refresh token for a new access/refresh pair.
func (h *Handler) Refresh(c *gin.Context) {
	var body refreshDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deviceID, err := uuid.Parse(body.DeviceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deviceId"})
		return
	}
	tokens, err := h.svc.Refresh(c.Request.Context(), application.RefreshInput{
		DeviceID:     deviceID,
		RefreshToken: body.RefreshToken,
		Timestamp:    body.Timestamp,
		Nonce:        body.Nonce,
		HMAC:         body.HMAC,
	}, false)
	if err != nil {
		respondAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTokenResponse(tokens))
}

func respondAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidPin),
		errors.Is(err, application.ErrNoSuchUser):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
	case errors.Is(err, application.ErrPinLocked):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "PIN temporarily locked"})
	case errors.Is(err, application.ErrDeviceNotEnrolled),
		errors.Is(err, application.ErrDeviceRevoked),
		errors.Is(err, application.ErrInvalidHMAC),
		errors.Is(err, application.ErrStaleRequest):
		c.JSON(http.StatusForbidden, gin.H{"error": "device not authorized"})
	case errors.Is(err, application.ErrRefreshExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "refresh token invalid"})
	default:
		zap.L().Warn("pos auth error", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

func toTokenResponse(t *application.TokenPair) tokenResponse {
	return tokenResponse{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresIn:    t.ExpiresIn,
		UserID:       t.UserID.String(),
		IsAdmin:      t.IsAdmin,
	}
}
