package interfaces

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/pos/application"
	"tsb-service/pkg/utils"
)

type Handler struct {
	svc          *application.Service
	userinfoURL  string // Zitadel userinfo endpoint for admin check
	internalURL  string // Optional Docker-internal Zitadel URL
	externalHost string // External host header for internal requests
}

func NewHandler(svc *application.Service, userinfoURL, internalURL, externalHost string) *Handler {
	return &Handler{
		svc:          svc,
		userinfoURL:  userinfoURL,
		internalURL:  internalURL,
		externalHost: externalHost,
	}
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

type fcmTokenDTO struct {
	DeviceID  string `json:"deviceId"  binding:"required,uuid"`
	FCMToken  string `json:"fcmToken"  binding:"required"`
	Timestamp int64  `json:"timestamp" binding:"required"`
	Nonce     string `json:"nonce"     binding:"required"`
	HMAC      string `json:"hmac"      binding:"required"`
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
	// Zitadel native-app JWT access tokens don't include project role claims.
	// Check admin status via the Zitadel userinfo endpoint instead.
	token := c.GetHeader("Authorization")
	if !h.isAdminViaUserinfo(token) {
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

	// All POS staff with an RRN+PIN are treated as admin for order management.
	// Fine-grained POS roles (cashier vs manager) can be added later via a
	// role column on the users table.
	tokens, err := h.svc.RrnLogin(c.Request.Context(), application.RrnLoginInput{
		DeviceID:  deviceID,
		RRN:       body.RRN,
		PIN:       body.PIN,
		Timestamp: body.Timestamp,
		Nonce:     body.Nonce,
		HMAC:      body.HMAC,
	}, true)
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
	}, true)
	if err != nil {
		respondAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTokenResponse(tokens))
}

// UpdateFCMToken registers or refreshes the FCM push token for a device.
// The request is HMAC-signed identically to RrnLogin/Refresh.
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

// isAdminViaUserinfo calls the Zitadel /oidc/v1/userinfo endpoint with the
// bearer token and checks if any of the project role claims contain "admin".
// This is a network call but only happens during device enrollment (once per device).
func (h *Handler) isAdminViaUserinfo(authHeader string) bool {
	if authHeader == "" {
		return false
	}
	url := h.userinfoURL
	if h.internalURL != "" {
		url = h.internalURL + "/oidc/v1/userinfo"
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		zap.L().Warn("failed to build userinfo request", zap.Error(err))
		return false
	}
	req.Header.Set("Authorization", authHeader)
	if h.externalHost != "" {
		req.Host = h.externalHost
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		zap.L().Warn("userinfo request failed", zap.Error(err))
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			zap.L().Warn("failed to close userinfo response body", zap.Error(err))
		}
	}()
	if resp.StatusCode != 200 {
		zap.L().Warn("userinfo non-200", zap.Int("status", resp.StatusCode))
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	var info map[string]interface{}
	if err := json.Unmarshal(body, &info); err != nil {
		return false
	}
	// Check all claim keys matching urn:zitadel:iam:org:project:*:roles for "admin"
	for k, v := range info {
		if len(k) > 30 && k[:30] == "urn:zitadel:iam:org:project:" {
			if roles, ok := v.(map[string]interface{}); ok {
				if _, hasAdmin := roles["admin"]; hasAdmin {
					return true
				}
			}
		}
	}
	// Also check the generic claim
	if roles, ok := info["urn:zitadel:iam:org:project:roles"].(map[string]interface{}); ok {
		if _, hasAdmin := roles["admin"]; hasAdmin {
			return true
		}
	}
	zap.L().Warn("admin role not found in userinfo", zap.String("keys", fmt.Sprintf("%v", keys(info))))
	return false
}

func keys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
