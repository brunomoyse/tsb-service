package interfaces

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/pos/application"
	"tsb-service/internal/modules/pos/domain"
)

type fakeDevices struct {
	domain.DeviceRepository
	device  *domain.Device
	findErr error
	fcm     string
	fcmErr  error
}

func (f *fakeDevices) FindByID(context.Context, uuid.UUID) (*domain.Device, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.device, nil
}
func (f *fakeDevices) TouchLastSeen(context.Context, uuid.UUID) error { return nil }
func (f *fakeDevices) UpdateFCMToken(_ context.Context, _ uuid.UUID, tok string) error {
	f.fcm = tok
	return f.fcmErr
}

type env struct {
	router *gin.Engine
	repo   *fakeDevices
	id     uuid.UUID
	key    []byte
}

func newEnv(t *testing.T) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)
	sum := sha256.Sum256([]byte("device-secret"))
	id := uuid.New()
	repo := &fakeDevices{device: &domain.Device{ID: id, DeviceSecretHash: hex.EncodeToString(sum[:])}}
	svc := application.NewService(application.DefaultConfig([]byte("0123456789abcdef0123456789abcdef")), repo)
	h := NewHandler(svc)
	r := gin.New()
	r.POST("/pos/auth/device-login", h.DeviceLogin)
	r.PATCH("/pos/devices/fcm-token", h.UpdateFCMToken) // PATCH, as cmd/app/main.go registers it
	return &env{router: r, repo: repo, id: id, key: sum[:]}
}

func (e *env) sign(payload string) string {
	m := hmac.New(sha256.New, e.key)
	m.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (e *env) post(path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	switch b := body.(type) {
	case string:
		buf.WriteString(b)
	default:
		_ = json.NewEncoder(&buf).Encode(b)
	}
	method := http.MethodPost
	if path == fcmTokenPath {
		method = http.MethodPatch
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

const nonce = "0123456789abcdef-nonce"

const fcmTokenPath = "/pos/devices/fcm-token"

func (e *env) loginBody(ts int64) map[string]any {
	return map[string]any{
		"deviceId": e.id.String(), "timestamp": ts, "nonce": nonce,
		"hmac": e.sign(fmt.Sprintf("%s|%d|%s", e.id, ts, nonce)),
	}
}

func (e *env) fcmBody(ts int64, token string) map[string]any {
	return map[string]any{
		"deviceId": e.id.String(), "fcmToken": token, "timestamp": ts, "nonce": "n",
		"hmac": e.sign(fmt.Sprintf("%s|%s|%d|%s", e.id, token, ts, "n")),
	}
}

func TestDeviceLoginHandler(t *testing.T) {
	now := func() int64 { return time.Now().UnixMilli() }

	t.Run("a valid proof returns the access token", func(t *testing.T) {
		e := newEnv(t)
		rec := e.post("/pos/auth/device-login", e.loginBody(now()))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			AccessToken string `json:"accessToken"`
			ExpiresIn   int64  `json:"expiresIn"`
			DeviceID    string `json:"deviceId"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.NotEmpty(t, out.AccessToken)
		assert.Equal(t, int64(8*3600), out.ExpiresIn)
		assert.Equal(t, e.id.String(), out.DeviceID)
	})

	t.Run("malformed bodies are 400", func(t *testing.T) {
		e := newEnv(t)
		for name, body := range map[string]any{
			"not json":       "{",
			"missing fields": map[string]any{"deviceId": e.id.String()},
			"bad uuid":       map[string]any{"deviceId": "nope", "timestamp": 1, "nonce": nonce, "hmac": "x"},
			"short nonce":    map[string]any{"deviceId": e.id.String(), "timestamp": 1, "nonce": "short", "hmac": "x"},
		} {
			rec := e.post("/pos/auth/device-login", body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		}
	})

	t.Run("authentication failures are 403 with the same opaque message", func(t *testing.T) {
		e := newEnv(t)
		stale := e.post("/pos/auth/device-login", e.loginBody(now()-int64(time.Hour/time.Millisecond)))
		badSig := e.loginBody(now())
		badSig["hmac"] = base64.StdEncoding.EncodeToString([]byte("nope"))
		bad := e.post("/pos/auth/device-login", badSig)

		// NOTE(product decision pending): a database outage is answered like an unknown device
		// (403 "device not authorized"), so a handheld cannot tell an outage from a lost enrolment.
		// See also application TestDeviceLogin. Pinned as it is today.
		e.repo.findErr = errors.New("db down")
		unknown := e.post("/pos/auth/device-login", e.loginBody(now()))
		e.repo.findErr = nil
		at := time.Now()
		e.repo.device.RevokedAt = &at
		revoked := e.post("/pos/auth/device-login", e.loginBody(now()))

		for name, rec := range map[string]*httptest.ResponseRecorder{"stale": stale, "bad signature": bad, "unknown": unknown, "revoked": revoked} {
			assert.Equal(t, http.StatusForbidden, rec.Code, name)
			assert.JSONEq(t, `{"error":"device not authorized"}`, rec.Body.String(), name)
		}
	})
}

func TestUpdateFCMTokenHandler(t *testing.T) {
	now := func() int64 { return time.Now().UnixMilli() }

	t.Run("stores the token and answers 204", func(t *testing.T) {
		e := newEnv(t)
		rec := e.post("/pos/devices/fcm-token", e.fcmBody(now(), "fcm-xyz"))
		assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.Equal(t, "fcm-xyz", e.repo.fcm)
	})

	t.Run("malformed bodies are 400", func(t *testing.T) {
		e := newEnv(t)
		assert.Equal(t, http.StatusBadRequest, e.post("/pos/devices/fcm-token", "{").Code)
		assert.Equal(t, http.StatusBadRequest, e.post("/pos/devices/fcm-token", map[string]any{
			"deviceId": "nope", "fcmToken": "t", "timestamp": 1, "nonce": "n", "hmac": "h",
		}).Code)
		assert.Empty(t, e.repo.fcm)
	})

	t.Run("an unauthenticated device is 403 and nothing is stored", func(t *testing.T) {
		e := newEnv(t)
		body := e.fcmBody(now(), "fcm-xyz")
		body["fcmToken"] = "fcm-other"
		rec := e.post("/pos/devices/fcm-token", body)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, e.repo.fcm)
	})

	t.Run("a storage failure is 500 without leaking the error", func(t *testing.T) {
		e := newEnv(t)
		e.repo.fcmErr = errors.New("password authentication failed for user")
		rec := e.post("/pos/devices/fcm-token", e.fcmBody(now(), "fcm-xyz"))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.JSONEq(t, `{"error":"internal error"}`, rec.Body.String())
	})
}
