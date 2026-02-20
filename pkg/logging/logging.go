package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"

	"tsb-service/pkg/utils"
)

type contextKeyType string

const requestIDKey contextKeyType = "request_id"

// Setup initializes the global slog logger. format: "json" or "text". level: "debug", "info", "warn", "error".
func Setup(level, format string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var base slog.Handler
	if format == "json" {
		base = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		base = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(&contextHandler{base: base})
	slog.SetDefault(logger)
}

// contextHandler wraps a slog.Handler to auto-extract request_id and user_id from context.
type contextHandler struct {
	base slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if rid := GetRequestID(ctx); rid != "" {
		r.AddAttrs(slog.String("request_id", rid))
	}
	if uid := utils.GetUserID(ctx); uid != "" {
		r.AddAttrs(slog.String("user_id", uid))
	}
	return h.base.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{base: h.base.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{base: h.base.WithGroup(name)}
}

// GenerateRequestID returns a 16-character hex string from crypto/rand.
func GenerateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

// SetRequestID stores a request ID in the context.
func SetRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// GetRequestID retrieves the request ID from the context.
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
