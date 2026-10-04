package fcm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"tsb-service/pkg/fcm/fcmtest"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// newTestClient wires a Client to the fake FCM endpoint (no real credentials), the way the
// resolvers' tests do.
func newTestClient(t *testing.T, srv *fcmtest.Server) *Client {
	t.Helper()
	c, err := NewWithEndpoint(t.Context(), "tsb-test", srv.URL)
	require.NoError(t, err)
	return c
}

const okResp = fcmtest.OK

func fcmError(status, code string) string { return fcmtest.Error(status, code) }

func TestSendAlert(t *testing.T) {
	t.Run("posts a high-priority notification message to the project endpoint", func(t *testing.T) {
		srv := fcmtest.New(t, 200, okResp)
		c := newTestClient(t, srv)

		require.NoError(t, c.SendAlert("reg-token", "Order confirmed", "Thanks!", map[string]string{"orderId": "o-1"}))

		require.Equal(t, []string{"/projects/tsb-test/messages:send"}, srv.Paths())
		msg := srv.Requests()[0].Payload
		require.Equal(t, "reg-token", msg["token"])
		require.Equal(t, map[string]any{"title": "Order confirmed", "body": "Thanks!"}, msg["notification"])
		require.Equal(t, map[string]any{"orderId": "o-1"}, msg["data"])
		require.Equal(t, map[string]any{
			"priority":     "high",
			"notification": map[string]any{"sound": "default", "channel_id": "orders"},
		}, msg["android"])
	})

	t.Run("unregistered token maps to ErrTokenInvalid", func(t *testing.T) {
		c := newTestClient(t, fcmtest.New(t, 404, fcmError("NOT_FOUND", "UNREGISTERED")))
		require.ErrorIs(t, c.SendAlert("dead", "t", "b", nil), ErrTokenInvalid)
	})

	t.Run("invalid argument maps to ErrTokenInvalid", func(t *testing.T) {
		c := newTestClient(t, fcmtest.New(t, 400, fcmError("INVALID_ARGUMENT", "INVALID_ARGUMENT")))
		require.ErrorIs(t, c.SendAlert("malformed", "t", "b", nil), ErrTokenInvalid)
	})

	t.Run("other API errors are logged and wrapped", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		defer zap.ReplaceGlobals(zap.New(core))()

		c := newTestClient(t, fcmtest.New(t, 403, fcmError("PERMISSION_DENIED", "SENDER_ID_MISMATCH")))
		err := c.SendAlert("t", "t", "b", nil)
		require.ErrorContains(t, err, "send FCM notification")
		require.NotErrorIs(t, err, ErrTokenInvalid)
		require.Len(t, logs.FilterMessage("FCM push not sent").All(), 1)
	})
}

func TestSendDataMessage(t *testing.T) {
	t.Run("sends a data-only message (no notification block)", func(t *testing.T) {
		srv := fcmtest.New(t, 200, okResp)
		c := newTestClient(t, srv)

		data := map[string]string{"event": "update", "progressValue": "50"}
		require.NoError(t, c.SendDataMessage("reg-token", data))

		msg := srv.Requests()[0].Payload
		require.Equal(t, "reg-token", msg["token"])
		require.Equal(t, map[string]any{"event": "update", "progressValue": "50"}, msg["data"])
		require.NotContains(t, msg, "notification", "data message must be silent")
		require.Equal(t, map[string]any{"priority": "high"}, msg["android"])
	})

	t.Run("unregistered and invalid tokens map to ErrTokenInvalid", func(t *testing.T) {
		c := newTestClient(t, fcmtest.New(t, 404, fcmError("NOT_FOUND", "UNREGISTERED")))
		require.ErrorIs(t, c.SendDataMessage("dead", map[string]string{"a": "b"}), ErrTokenInvalid)

		c = newTestClient(t, fcmtest.New(t, 400, fcmError("INVALID_ARGUMENT", "INVALID_ARGUMENT")))
		require.ErrorIs(t, c.SendDataMessage("bad", map[string]string{"a": "b"}), ErrTokenInvalid)
	})

	t.Run("other API errors are logged and wrapped", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		defer zap.ReplaceGlobals(zap.New(core))()

		c := newTestClient(t, fcmtest.New(t, 403, fcmError("PERMISSION_DENIED", "SENDER_ID_MISMATCH")))
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
