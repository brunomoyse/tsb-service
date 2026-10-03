package apperr

import (
	"errors"
	"fmt"
	"maps"
)

// Error is an error with a stable Code. Its Error() text is the English message (logs, old clients).
type Error struct {
	Code   Code
	cause  error
	params map[string]any
}

// New builds an error with a fixed message.
func New(code Code, message string) *Error {
	return &Error{Code: code, cause: errors.New(message)}
}

// Newf builds an error from a format; like fmt.Errorf, a %w operand stays reachable with errors.Is/As
// (so context.Canceled and friends keep working through the wrapper).
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, cause: fmt.Errorf(format, args...)}
}

// With attaches a parameter that is exposed in the response's `extensions` (e.g. "field",
// "productId", "minimum"). Only put values there that are safe to show the customer.
func (e *Error) With(key string, value any) *Error {
	params := make(map[string]any, len(e.params)+1)
	maps.Copy(params, e.params)
	params[key] = value
	return &Error{Code: e.Code, cause: e.cause, params: params}
}

func (e *Error) Error() string { return e.cause.Error() }

func (e *Error) Unwrap() error { return e.cause }

// Extensions is what goes into the GraphQL error's `extensions`: the parameters plus `code`.
func (e *Error) Extensions() map[string]any {
	ext := make(map[string]any, len(e.params)+1)
	maps.Copy(ext, e.params)
	ext["code"] = string(e.Code)
	return ext
}

// From returns the *Error in err's chain, if any.
func From(err error) (*Error, bool) {
	if appErr, ok := errors.AsType[*Error](err); ok {
		return appErr, true
	}
	return nil, false
}
