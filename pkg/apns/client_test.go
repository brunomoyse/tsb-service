package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tsb-service/pkg/apns/apnstest"

	"github.com/sideshow/apns2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// writeP8 writes an ECDSA P-256 PKCS#8 key in the .p8 format Apple issues.
func writeP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
	return path
}

// newTestClient builds a real Client (via NewClient, so JWT auth is exercised)
// and points its two environments at the given fake servers.
func newTestClient(t *testing.T, preferProd bool, prod, dev *apnstest.Server) *Client {
	t.Helper()
	c, err := NewClient(writeP8(t), "KEYID12345", "TEAMID1234", "be.test.app", preferProd)
	require.NoError(t, err)
	c.prod.Host, c.prod.HTTPClient = prod.URL, prod.Client()
	c.dev.Host, c.dev.HTTPClient = dev.URL, dev.Client()
	return c
}

func TestNewClient(t *testing.T) {
	t.Run("builds prod and dev clients with the right topics", func(t *testing.T) {
		c, err := NewClient(writeP8(t), "KEY", "TEAM", "be.test.app", true)
		require.NoError(t, err)
		require.Equal(t, apns2.HostProduction, c.prod.Host)
		require.Equal(t, apns2.HostDevelopment, c.dev.Host)
		require.True(t, c.preferProd)
		require.Equal(t, "be.test.app", c.alertTopic)
		require.Equal(t, "be.test.app.push-type.liveactivity", c.liveActivityTopic)
		require.Equal(t, "KEY", c.prod.Token.KeyID)
		require.Equal(t, "TEAM", c.dev.Token.TeamID)
	})

	t.Run("missing key file", func(t *testing.T) {
		_, err := NewClient(filepath.Join(t.TempDir(), "absent.p8"), "K", "T", "b", true)
		require.ErrorContains(t, err, "load APNs auth key")
	})

	t.Run("file that is not PEM", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "bad.p8")
		require.NoError(t, os.WriteFile(p, []byte("not a key"), 0o600))
		_, err := NewClient(p, "K", "T", "b", true)
		require.ErrorContains(t, err, "load APNs auth key")
	})

	t.Run("RSA key is rejected", func(t *testing.T) {
		rk, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		der, err := x509.MarshalPKCS8PrivateKey(rk)
		require.NoError(t, err)
		p := filepath.Join(t.TempDir(), "rsa.p8")
		require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
		_, err = NewClient(p, "K", "T", "b", true)
		require.ErrorContains(t, err, "load APNs auth key")
	})
}

func TestIsWrongEnvironmentReason(t *testing.T) {
	for _, r := range []string{apns2.ReasonBadDeviceToken, "BadEnvironmentKeyInToken", apns2.ReasonBadCertificateEnvironment} {
		require.True(t, isWrongEnvironmentReason(r), r)
	}
	for _, r := range []string{"", apns2.ReasonUnregistered, apns2.ReasonExpiredToken, apns2.ReasonTooManyRequests} {
		require.False(t, isWrongEnvironmentReason(r), r)
	}
}

func TestSendAlert(t *testing.T) {
	t.Run("sends the alert payload to the preferred (production) endpoint with JWT auth", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)

		err := c.SendAlert("devtok123", "Order confirmed", "Your order is confirmed", map[string]string{"orderId": "o-1"})
		require.NoError(t, err)

		reqs := prod.Requests()
		require.Len(t, reqs, 1)
		require.Empty(t, dev.Requests(), "no fallback on success")
		r := reqs[0]
		require.Equal(t, "/3/device/devtok123", r.Path)
		require.Equal(t, "be.test.app", r.Header.Get("apns-topic"))
		require.Equal(t, "alert", r.Header.Get("apns-push-type"))
		require.True(t, strings.HasPrefix(r.Header.Get("authorization"), "bearer "), r.Header.Get("authorization"))
		require.Equal(t, map[string]any{
			"aps": map[string]any{
				"alert": map[string]any{"title": "Order confirmed", "body": "Your order is confirmed"},
				"sound": "default",
			},
			"orderId": "o-1",
		}, r.Payload)
	})

	t.Run("prefers the sandbox endpoint when not production", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, false, prod, dev)
		require.NoError(t, c.SendAlert("t", "a", "b", nil))
		require.Len(t, dev.Requests(), 1)
		require.Empty(t, prod.Requests())
	})

	t.Run("wrong-environment reply retries once on the other endpoint", func(t *testing.T) {
		for _, reason := range []string{apns2.ReasonBadDeviceToken, "BadEnvironmentKeyInToken", apns2.ReasonBadCertificateEnvironment} {
			prod, dev := apnstest.New(t, 400, reason), apnstest.New(t, 200, "")
			c := newTestClient(t, true, prod, dev)
			require.NoError(t, c.SendAlert("sandbox-token", "a", "b", nil), reason)
			require.Len(t, prod.Requests(), 1, reason)
			require.Len(t, dev.Requests(), 1, "retry must hit the other environment: "+reason)
		}
	})

	t.Run("token invalid on both environments is reported as ErrTokenInvalid", func(t *testing.T) {
		prod, dev := apnstest.New(t, 400, apns2.ReasonBadDeviceToken), apnstest.New(t, 400, apns2.ReasonBadDeviceToken)
		c := newTestClient(t, true, prod, dev)
		require.ErrorIs(t, c.SendAlert("dead", "a", "b", nil), ErrTokenInvalid)
		require.Len(t, prod.Requests(), 1)
		require.Len(t, dev.Requests(), 1, "exactly one retry, no loop")
	})

	t.Run("Unregistered and ExpiredToken are final: ErrTokenInvalid without a retry", func(t *testing.T) {
		for _, c := range []struct {
			status int
			reason string
		}{{410, apns2.ReasonUnregistered}, {410, apns2.ReasonExpiredToken}} {
			prod, dev := apnstest.New(t, c.status, c.reason), apnstest.New(t, 200, "")
			cl := newTestClient(t, true, prod, dev)
			require.ErrorIs(t, cl.SendAlert("gone", "a", "b", nil), ErrTokenInvalid, c.reason)
			require.Empty(t, dev.Requests(), "dead token must not be retried: "+c.reason)
		}
	})

	t.Run("other rejections are logged and swallowed", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		undo := zap.ReplaceGlobals(zap.New(core))
		defer undo()

		prod, dev := apnstest.New(t, 429, apns2.ReasonTooManyRequests), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		require.NoError(t, c.SendAlert("t", "a", "b", nil))
		require.Empty(t, dev.Requests())

		entries := logs.FilterMessage("APNs alert push not sent").All()
		require.Len(t, entries, 1)
		require.Equal(t, int64(429), entries[0].ContextMap()["status"])
		require.Equal(t, apns2.ReasonTooManyRequests, entries[0].ContextMap()["reason"])
	})

	t.Run("network error is wrapped and not retried as token-invalid", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		prod.Close()
		err := c.SendAlert("t", "a", "b", nil)
		require.ErrorContains(t, err, "push alert notification")
		require.NotErrorIs(t, err, ErrTokenInvalid)
		require.Empty(t, dev.Requests(), "transport error is not a wrong-environment signal")
	})

	t.Run("network error on the retry leg is surfaced", func(t *testing.T) {
		prod, dev := apnstest.New(t, 400, apns2.ReasonBadDeviceToken), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		dev.Close()
		err := c.SendAlert("t", "a", "b", nil)
		require.ErrorContains(t, err, "push alert notification")
		require.NotErrorIs(t, err, ErrTokenInvalid)
	})

	t.Run("custom data keys sit beside aps", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		require.NoError(t, c.SendAlert("t", "a", "b", map[string]string{"k1": "v1", "k2": "v2"}))
		p := prod.Requests()[0].Payload
		require.Equal(t, "v1", p["k1"])
		require.Equal(t, "v2", p["k2"])
		require.Contains(t, p, "aps")
	})
}

func TestSendLiveActivity(t *testing.T) {
	t.Run("sends a liveactivity push with the dedicated topic and high priority", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)

		state := map[string]any{"title": "Preparing", "subtitle": "soon", "progress": 0.5}
		require.NoError(t, c.SendLiveActivity("la-token", state, "update"))

		reqs := prod.Requests()
		require.Len(t, reqs, 1)
		r := reqs[0]
		require.Equal(t, "/3/device/la-token", r.Path)
		require.Equal(t, "be.test.app.push-type.liveactivity", r.Header.Get("apns-topic"))
		require.Equal(t, "liveactivity", r.Header.Get("apns-push-type"))
		require.Equal(t, "10", r.Header.Get("apns-priority"))

		aps, ok := r.Payload["aps"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "update", aps["event"])
		require.Equal(t, state, aps["content-state"])
		require.Greater(t, aps["timestamp"], float64(1_700_000_000), "unix timestamp expected")
	})

	t.Run("end event is forwarded", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		require.NoError(t, c.SendLiveActivity("t", map[string]any{}, "end"))
		require.Equal(t, "end", prod.Requests()[0].Payload["aps"].(map[string]any)["event"])
	})

	t.Run("wrong-environment retry", func(t *testing.T) {
		prod, dev := apnstest.New(t, 400, apns2.ReasonBadDeviceToken), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		require.NoError(t, c.SendLiveActivity("t", nil, "update"))
		require.Len(t, dev.Requests(), 1)
	})

	t.Run("dead token", func(t *testing.T) {
		for _, reason := range []string{apns2.ReasonBadDeviceToken, apns2.ReasonUnregistered, apns2.ReasonExpiredToken} {
			prod, dev := apnstest.New(t, 410, reason), apnstest.New(t, 410, reason)
			c := newTestClient(t, true, prod, dev)
			require.ErrorIs(t, c.SendLiveActivity("t", nil, "update"), ErrTokenInvalid, reason)
		}
	})

	t.Run("other rejections are logged and swallowed", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		undo := zap.ReplaceGlobals(zap.New(core))
		defer undo()
		prod, dev := apnstest.New(t, 500, "InternalServerError"), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		require.NoError(t, c.SendLiveActivity("t", nil, "update"))
		entries := logs.FilterMessage("APNs live activity push not sent").All()
		require.Len(t, entries, 1)
		require.Equal(t, "update", entries[0].ContextMap()["event"])
	})

	t.Run("network error is wrapped", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		prod.Close()
		err := c.SendLiveActivity("t", nil, "update")
		require.ErrorContains(t, err, "push live activity notification")
	})

	t.Run("unmarshalable content state is a marshal error", func(t *testing.T) {
		prod, dev := apnstest.New(t, 200, ""), apnstest.New(t, 200, "")
		c := newTestClient(t, true, prod, dev)
		err := c.SendLiveActivity("t", map[string]any{"bad": make(chan int)}, "update")
		require.ErrorContains(t, err, "marshal live activity payload")
		require.Empty(t, prod.Requests())
	})
}
