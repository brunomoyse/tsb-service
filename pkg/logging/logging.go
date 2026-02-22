package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"tsb-service/pkg/utils"
)

type contextKeyType string

const requestIDKey contextKeyType = "request_id"

// Setup initializes the global zap logger. format: "json" or "text". level: "debug", "info", "warn", "error".
func Setup(level, format string) {
	var lvl zapcore.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = zapcore.DebugLevel
	case "warn":
		lvl = zapcore.WarnLevel
	case "error":
		lvl = zapcore.ErrorLevel
	default:
		lvl = zapcore.InfoLevel
	}

	var encoder zapcore.Encoder
	if format == "json" {
		encoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	} else {
		cfg := zap.NewDevelopmentEncoderConfig()
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05.000")
		encoder = zapcore.NewConsoleEncoder(cfg)
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), lvl)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	zap.ReplaceGlobals(logger)
}

// Sync flushes any buffered log entries. Call before exit.
func Sync() {
	_ = zap.L().Sync()
}

// FromContext returns a logger enriched with request_id and user_id from ctx.
func FromContext(ctx context.Context) *zap.Logger {
	l := zap.L()
	if rid := GetRequestID(ctx); rid != "" {
		l = l.With(zap.String("request_id", rid))
	}
	if uid := utils.GetUserID(ctx); uid != "" {
		l = l.With(zap.String("user_id", uid))
	}
	return l
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
