package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"tsb-service/pkg/utils"
)

// captureStdout runs fn with os.Stdout redirected and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	require.NoError(t, w.Close())
	return <-done
}

func restoreGlobalLogger(t *testing.T) {
	t.Helper()
	restore := zap.ReplaceGlobals(zap.L())
	t.Cleanup(restore)
}

func TestSetupLevels(t *testing.T) {
	restoreGlobalLogger(t)
	cases := []struct {
		level     string
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
		wantError bool
	}{
		{"debug", true, true, true, true},
		{"DEBUG", true, true, true, true},
		{"info", false, true, true, true},
		{"", false, true, true, true},
		{"nonsense", false, true, true, true},
		{"warn", false, false, true, true},
		{"error", false, false, false, true},
	}
	for _, tc := range cases {
		t.Run("level "+tc.level, func(t *testing.T) {
			Setup(tc.level, "json")
			core := zap.L().Core()
			assert.Equal(t, tc.wantDebug, core.Enabled(zapcore.DebugLevel))
			assert.Equal(t, tc.wantInfo, core.Enabled(zapcore.InfoLevel))
			assert.Equal(t, tc.wantWarn, core.Enabled(zapcore.WarnLevel))
			assert.Equal(t, tc.wantError, core.Enabled(zapcore.ErrorLevel))
		})
	}
}

func TestSetupFormats(t *testing.T) {
	restoreGlobalLogger(t)

	t.Run("json writes one structured object per line", func(t *testing.T) {
		out := captureStdout(t, func() {
			Setup("info", "json")
			zap.L().Info("hello", zap.String("k", "v"))
			Sync()
		})
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &line), out)
		assert.Equal(t, "hello", line["msg"])
		assert.Equal(t, "v", line["k"])
		assert.Equal(t, "info", line["level"])
	})

	t.Run("anything else is the human readable console format", func(t *testing.T) {
		out := captureStdout(t, func() {
			Setup("info", "text")
			zap.L().Info("hello", zap.String("k", "v"))
			Sync()
		})
		assert.Contains(t, out, "hello")
		assert.Contains(t, out, `"k": "v"`)
		assert.NotContains(t, out, `"msg"`, "not JSON")
		assert.Contains(t, out, "\x1b[", "levels are colored in console mode")
	})

	t.Run("debug lines are dropped below the configured level", func(t *testing.T) {
		out := captureStdout(t, func() {
			Setup("warn", "json")
			zap.L().Info("quiet")
			zap.L().Warn("loud")
			Sync()
		})
		assert.NotContains(t, out, "quiet")
		assert.Contains(t, out, "loud")
	})

	t.Run("errors carry a stack trace", func(t *testing.T) {
		out := captureStdout(t, func() {
			Setup("info", "json")
			zap.L().Error("boom")
			Sync()
		})
		assert.Contains(t, out, `"stacktrace"`)
	})
}

func TestFromContextEnrichesWithRequestAndUser(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	FromContext(context.Background()).Info("bare")
	ctx := SetRequestID(context.Background(), "req-1")
	FromContext(ctx).Info("with request")
	ctx = utils.SetUserID(ctx, "user-1")
	FromContext(ctx).Info("with both")
	FromContext(utils.SetUserID(context.Background(), "user-2")).Info("user only")

	all := logs.All()
	require.Len(t, all, 4)
	assert.Empty(t, all[0].ContextMap())
	assert.Equal(t, map[string]any{"request_id": "req-1"}, all[1].ContextMap())
	assert.Equal(t, map[string]any{"request_id": "req-1", "user_id": "user-1"}, all[2].ContextMap())
	assert.Equal(t, map[string]any{"user_id": "user-2"}, all[3].ContextMap())
}

func TestRequestIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := GenerateRequestID()
		require.Len(t, id, 16)
		require.Regexp(t, "^[0-9a-f]{16}$", id)
		seen[id] = true
	}
	assert.Len(t, seen, 100, "request ids are random")

	assert.Empty(t, GetRequestID(context.Background()))
	assert.Equal(t, "abc", GetRequestID(SetRequestID(context.Background(), "abc")))
	assert.Empty(t, GetRequestID(context.WithValue(context.Background(), requestIDKey, 42)), "a value of the wrong type is ignored")
}

func TestSentryCoreWithoutAClient(t *testing.T) {
	// Without sentry.Init (no DSN) the bridge must be inert: no panic, no error.
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(nil)
	defer hub.BindClient(prev)

	c := newSentryCore()
	require.NoError(t, c.Write(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "x"}, nil))
	require.NoError(t, c.Sync())
}

func TestSentryCoreEventShape(t *testing.T) {
	tr := &captureTransport{}
	require.NoError(t, sentry.Init(sentry.ClientOptions{Dsn: "https://test@test.ingest.sentry.io/1", Transport: tr}))
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	restoreGlobalLogger(t)
	Setup("info", "json")

	ctx := utils.SetUserID(SetRequestID(context.Background(), "req-7"), "user-7")
	FromContext(ctx).Error("handler failed", zap.String("order_id", "o-1"))
	zap.L().DPanic("invariant broken")
	sentry.Flush(0)

	events := map[string]*sentry.Event{}
	for _, e := range tr.all() {
		events[e.Message] = e
	}
	require.Contains(t, events, "handler failed")
	e := events["handler failed"]
	assert.Equal(t, sentry.LevelError, e.Level)
	assert.Equal(t, "req-7", e.Tags["request_id"])
	assert.Equal(t, "user-7", e.Tags["user_id"])
	assert.Equal(t, "user-7", e.User.ID)
	assert.NotEmpty(t, e.Tags["caller"], "the log call site is a tag")
	logCtx := e.Contexts["log"]
	assert.Equal(t, "o-1", logCtx["order_id"], "structured fields travel with the event")
	assert.NotContains(t, logCtx, skipSentryKey)
	assert.Contains(t, logCtx, "stacktrace")

	require.Contains(t, events, "invariant broken")
	assert.Equal(t, sentry.LevelFatal, events["invariant broken"].Level, "DPanic and above are fatal")

	assert.NoError(t, newSentryCore().Sync())
}
