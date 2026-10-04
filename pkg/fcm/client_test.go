package fcm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	firebase "firebase.google.com/go/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/api/option"
)

// fakeFCM is an httptest server impersonating the FCM v1 send endpoint.
type fakeFCM struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	status int
	resp   string
}

func newFakeFCM(t *testing.T, status int, resp string) *fakeFCM {
	t.Helper()
	f := &fakeFCM{status: status, resp: resp}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.resp)
	}))
	t.Cleanup(f.Close)
	return f
}

// newTestClient wires a Client to the fake FCM endpoint (no real credentials).
func newTestClient(t *testing.T, srv *fakeFCM) *Client {
	t.Helper()
	app, err := firebase.NewApp(t.Context(), &firebase.Config{ProjectID: "tsb-test"},
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	require.NoError(t, err)
	mc, err := app.Messaging(t.Context())
	require.NoError(t, err)
	return &Client{msgClient: mc}
}

const okResp = `{"name":"projects/tsb-test/messages/0:123"}`

func fcmError(status, code string) string {
	return `{"error":{"code":0,"status":"` + status + `","message":"x","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"` + code + `"}]}}`
}

func TestSendAlert(t *testing.T) {
	t.Run("posts a high-priority notification message to the project endpoint", func(t *testing.T) {
		srv := newFakeFCM(t, 200, okResp)
		c := newTestClient(t, srv)

		require.NoError(t, c.SendAlert("reg-token", "Order confirmed", "Thanks!", map[string]string{"orderId": "o-1"}))

		require.Equal(t, []string{"/projects/tsb-test/messages:send"}, srv.paths)
		msg, ok := srv.bodies[0]["message"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "reg-token", msg["token"])
		require.Equal(t, map[string]any{"title": "Order confirmed", "body": "Thanks!"}, msg["notification"])
		require.Equal(t, map[string]any{"orderId": "o-1"}, msg["data"])
		require.Equal(t, map[string]any{
			"priority":     "high",
			"notification": map[string]any{"sound": "default", "channel_id": "orders"},
		}, msg["android"])
	})

	t.Run("unregistered token maps to ErrTokenInvalid", func(t *testing.T) {
		c := newTestClient(t, newFakeFCM(t, 404, fcmError("NOT_FOUND", "UNREGISTERED")))
		require.ErrorIs(t, c.SendAlert("dead", "t", "b", nil), ErrTokenInvalid)
	})

	t.Run("invalid argument maps to ErrTokenInvalid", func(t *testing.T) {
		c := newTestClient(t, newFakeFCM(t, 400, fcmError("INVALID_ARGUMENT", "INVALID_ARGUMENT")))
		require.ErrorIs(t, c.SendAlert("malformed", "t", "b", nil), ErrTokenInvalid)
	})

	t.Run("other API errors are logged and wrapped", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		defer zap.ReplaceGlobals(zap.New(core))()

		c := newTestClient(t, newFakeFCM(t, 403, fcmError("PERMISSION_DENIED", "SENDER_ID_MISMATCH")))
		err := c.SendAlert("t", "t", "b", nil)
		require.ErrorContains(t, err, "send FCM notification")
		require.NotErrorIs(t, err, ErrTokenInvalid)
		require.Len(t, logs.FilterMessage("FCM push not sent").All(), 1)
	})
}

func TestSendDataMessage(t *testing.T) {
	t.Run("sends a data-only message (no notification block)", func(t *testing.T) {
		srv := newFakeFCM(t, 200, okResp)
		c := newTestClient(t, srv)

		data := map[string]string{"event": "update", "progressValue": "50"}
		require.NoError(t, c.SendDataMessage("reg-token", data))

		msg := srv.bodies[0]["message"].(map[string]any)
		require.Equal(t, "reg-token", msg["token"])
		require.Equal(t, map[string]any{"event": "update", "progressValue": "50"}, msg["data"])
		require.NotContains(t, msg, "notification", "data message must be silent")
		require.Equal(t, map[string]any{"priority": "high"}, msg["android"])
	})

	t.Run("unregistered and invalid tokens map to ErrTokenInvalid", func(t *testing.T) {
		c := newTestClient(t, newFakeFCM(t, 404, fcmError("NOT_FOUND", "UNREGISTERED")))
		require.ErrorIs(t, c.SendDataMessage("dead", map[string]string{"a": "b"}), ErrTokenInvalid)

		c = newTestClient(t, newFakeFCM(t, 400, fcmError("INVALID_ARGUMENT", "INVALID_ARGUMENT")))
		require.ErrorIs(t, c.SendDataMessage("bad", map[string]string{"a": "b"}), ErrTokenInvalid)
	})

	t.Run("other API errors are logged and wrapped", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		defer zap.ReplaceGlobals(zap.New(core))()

		c := newTestClient(t, newFakeFCM(t, 403, fcmError("PERMISSION_DENIED", "SENDER_ID_MISMATCH")))
		err := c.SendDataMessage("t", map[string]string{"a": "b"})
		require.ErrorContains(t, err, "send FCM data message")
		require.NotErrorIs(t, err, ErrTokenInvalid)
		require.Len(t, logs.FilterMessage("FCM data message not sent").All(), 1)
	})
}

// writeServiceAccount writes a syntactically valid service-account key file.
// No network call is made with it: Firebase only mints tokens on first send.
func writeServiceAccount(t *testing.T, projectID string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)})
	sa := map[string]string{
		"type":           "service_account",
		"client_email":   "svc@tsb-test.iam.gserviceaccount.com",
		"private_key":    string(pemKey),
		"private_key_id": "kid",
		"token_uri":      "https://oauth2.example.invalid/token",
	}
	if projectID != "" {
		sa["project_id"] = projectID
	}
	raw, err := json.Marshal(sa)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "sa.json")
	require.NoError(t, os.WriteFile(p, raw, 0o600))
	return p
}

func mustPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return der
}

func TestNewClient(t *testing.T) {
	t.Run("builds a client from application default credentials", func(t *testing.T) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeServiceAccount(t, "tsb-test"))
		c, err := NewClient()
		require.NoError(t, err)
		require.NotNil(t, c.msgClient)
	})

	t.Run("malformed FIREBASE_CONFIG fails app initialization", func(t *testing.T) {
		t.Setenv("FIREBASE_CONFIG", "{not json")
		_, err := NewClient()
		require.ErrorContains(t, err, "initialize Firebase app")
	})

	t.Run("missing credentials file leaves no project id and fails messaging initialization", func(t *testing.T) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "absent.json"))
		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		t.Setenv("GCLOUD_PROJECT", "")
		_, err := NewClient()
		require.ErrorContains(t, err, "initialize FCM messaging client")
	})

	t.Run("credentials without a project id fail messaging initialization", func(t *testing.T) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeServiceAccount(t, ""))
		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		t.Setenv("GCLOUD_PROJECT", "")
		_, err := NewClient()
		require.ErrorContains(t, err, "initialize FCM messaging client")
	})
}
