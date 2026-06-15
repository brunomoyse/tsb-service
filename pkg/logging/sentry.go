package logging

import (
	"time"

	"github.com/getsentry/sentry-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// skipSentryKey marks a log entry whose Sentry event is already raised by the
// caller (e.g. the GraphQL error presenter captures the exception itself with
// richer scope). The bridge core skips these to avoid double events.
const skipSentryKey = "_skip_sentry"

// SkipSentry is attached to an .Error(...) call that has already reported the
// error to Sentry on its own, so the zap→Sentry bridge does not capture it a
// second time.
var SkipSentry = zap.Bool(skipSentryKey, true)

// sentryCore forwards every Error-level-and-above zap entry to Sentry, so any
// .Error(...) call anywhere in the service raises an alert — not just GraphQL
// resolver errors and panics, which were the only paths wired before.
//
// It is installed unconditionally; CaptureEvent is a no-op until sentry.Init
// binds a client (i.e. only when SENTRY_DSN is set), so local/dev runs are
// unaffected and the bridge costs nothing there.
type sentryCore struct {
	zapcore.LevelEnabler
	fields []zapcore.Field
}

func newSentryCore() *sentryCore {
	// Error and above only — Warn-level entries (expected client conditions
	// such as UNAUTHENTICATED, invalid OTP code, user-not-found lookups) must
	// not page anyone.
	return &sentryCore{LevelEnabler: zapcore.ErrorLevel}
}

func (c *sentryCore) With(fields []zapcore.Field) zapcore.Core {
	clone := *c
	clone.fields = make([]zapcore.Field, 0, len(c.fields)+len(fields))
	clone.fields = append(clone.fields, c.fields...)
	clone.fields = append(clone.fields, fields...)
	return &clone
}

func (c *sentryCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *sentryCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	hub := sentry.CurrentHub()
	if hub.Client() == nil {
		return nil // Sentry not configured (no SENTRY_DSN) — no-op.
	}

	// Flatten accumulated + call-site fields into a map for Sentry extras.
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}
	if skip, _ := enc.Fields[skipSentryKey].(bool); skip {
		return nil
	}
	delete(enc.Fields, skipSentryKey)

	event := sentry.NewEvent()
	event.Message = ent.Message
	event.Logger = ent.LoggerName
	event.Timestamp = ent.Time
	if ent.Stack != "" {
		enc.Fields["stacktrace"] = ent.Stack
	}
	// sentry-go v0.46 dropped Event.Extra; structured fields live under Contexts.
	event.Contexts["log"] = sentry.Context(enc.Fields)
	if ent.Level >= zapcore.DPanicLevel {
		event.Level = sentry.LevelFatal
	} else {
		event.Level = sentry.LevelError
	}

	// Promote request/user identifiers (injected by FromContext) to tags so
	// events are searchable and grouped per user.
	if rid, ok := enc.Fields["request_id"].(string); ok && rid != "" {
		event.Tags["request_id"] = rid
	}
	if uid, ok := enc.Fields["user_id"].(string); ok && uid != "" {
		event.Tags["user_id"] = uid
		event.User = sentry.User{ID: uid}
	}
	if ent.Caller.Defined {
		event.Tags["caller"] = ent.Caller.TrimmedPath()
	}

	hub.CaptureEvent(event)
	return nil
}

func (c *sentryCore) Sync() error {
	sentry.Flush(2 * time.Second)
	return nil
}
