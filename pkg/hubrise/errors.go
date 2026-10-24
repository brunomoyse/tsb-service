package hubrise

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind classifies a failed HubRise call by what the caller should do about it. It implements error
// so that errors.Is(err, hubrise.KindUnauthorized) works on any error returned by this package.
type Kind int

// The kinds of failure.
const (
	// KindUnknown is a response this package has no better class for (an unexpected status).
	KindUnknown Kind = iota
	// KindUnauthorized is a 401: the token was revoked, the connection blocked, or the account
	// suspended for non payment. Connection level: retrying cannot help, the user must reconnect.
	KindUnauthorized
	// KindForbidden is a 403: a missing scope or a disabled location. Connection level too.
	KindForbidden
	// KindNotFound is a 404 for a resource.
	KindNotFound
	// KindInvalid is a 400, 415 or 422: the request itself is wrong, retrying the same payload
	// cannot help.
	KindInvalid
	// KindRateLimited is a 429, or the local limiter refusing to spend more of the budget. Retry
	// after Error.RetryAfter.
	KindRateLimited
	// KindServer is a 5xx.
	KindServer
	// KindTimeout is a request that did not complete in time (client timeout or context deadline).
	KindTimeout
	// KindNetwork is a transport failure: connection refused, reset, response dropped.
	KindNetwork
	// KindProtocol is a response that is not what the API documents (undecodable body, too large).
	KindProtocol
	// KindRedirect is a redirect to another host, which the client refuses to follow.
	KindRedirect
	// KindCanceled is a call abandoned because the caller's context was canceled.
	KindCanceled
)

var kindNames = [...]string{
	KindUnknown:      "unknown",
	KindUnauthorized: "unauthorized",
	KindForbidden:    "forbidden",
	KindNotFound:     "not_found",
	KindInvalid:      "invalid",
	KindRateLimited:  "rate_limited",
	KindServer:       "server",
	KindTimeout:      "timeout",
	KindNetwork:      "network",
	KindProtocol:     "protocol",
	KindRedirect:     "redirect",
	KindCanceled:     "canceled",
}

// String returns the kind's name.
func (k Kind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return "unknown"
	}
	return kindNames[k]
}

// Error implements error, for errors.Is matching.
func (k Kind) Error() string { return "hubrise: " + k.String() }

// FieldError is one entry of the errors array of a HubRise 422.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is the single error type of this package. It never contains the access token or the
// client secret, in any field.
type Error struct {
	Kind Kind
	// Op is the call, "POST /location/orders".
	Op string
	// Status is the HTTP status, 0 when there was no response.
	Status int
	// ErrorType is HubRise's error_type ("unprocessable_entity", "too_many_requests"...).
	ErrorType string
	// Message is HubRise's message, or the description of a local failure.
	Message string
	// Fields are the per field details of a 422.
	Fields []FieldError
	// RetryAfter is how long to wait before retrying a 429 (Retry-After), 0 when unknown.
	RetryAfter time.Duration
	// Err is the underlying cause (a transport error, a context error).
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("hubrise: ")
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Kind.String())
	if e.Status != 0 {
		b.WriteString(" (")
		b.WriteString(strconv.Itoa(e.Status))
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	for _, f := range e.Fields {
		b.WriteString("; ")
		b.WriteString(f.Field)
		b.WriteString(": ")
		b.WriteString(f.Message)
	}
	return b.String()
}

// Unwrap returns the underlying cause, so errors.Is(err, context.DeadlineExceeded) keeps working.
func (e *Error) Unwrap() error { return e.Err }

// Is matches a Kind: errors.Is(err, hubrise.KindForbidden).
func (e *Error) Is(target error) bool {
	k, ok := target.(Kind)
	return ok && k == e.Kind
}

// Retryable reports whether the same request may succeed later: rate limiting, server errors,
// timeouts and transport failures. A dropped response is retryable too, but the caller must check
// whether the request had taken effect first (the order push does, through private_ref).
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindRateLimited, KindServer, KindTimeout, KindNetwork:
		return true
	default:
		return false
	}
}

// ConnectionLevel reports whether the failure concerns the whole connection (401, 403) rather than
// the request: the pipeline should pause instead of failing the item.
func (e *Error) ConnectionLevel() bool {
	return e.Kind == KindUnauthorized || e.Kind == KindForbidden
}

// KindOf returns the kind of err, KindUnknown when err is not (and does not wrap) an *Error.
func KindOf(err error) Kind {
	var he *Error
	if errors.As(err, &he) {
		return he.Kind
	}
	return KindUnknown
}

// kindForStatus maps an HTTP status to a Kind.
func kindForStatus(status int) Kind {
	switch {
	case status == http.StatusUnauthorized:
		return KindUnauthorized
	case status == http.StatusForbidden:
		return KindForbidden
	case status == http.StatusNotFound:
		return KindNotFound
	case status == http.StatusBadRequest, status == http.StatusUnsupportedMediaType,
		status == http.StatusUnprocessableEntity:
		return KindInvalid
	case status == http.StatusTooManyRequests:
		return KindRateLimited
	case status >= 500:
		return KindServer
	default:
		return KindUnknown
	}
}

// errorBody is the JSON error envelope of HubRise.
type errorBody struct {
	Message   string       `json:"message"`
	ErrorType string       `json:"error_type"`
	Errors    []FieldError `json:"errors"`
}

// maxErrorMessage bounds what is kept of a response body in an error message.
const maxErrorMessage = 500

// newStatusError builds the Error for a non 2xx response. secrets are scrubbed from every text
// field, so a server that echoes a credential cannot leak it through an error.
func newStatusError(op string, status int, header http.Header, body []byte, now time.Time, secrets ...string) *Error {
	e := &Error{
		Kind:       kindForStatus(status),
		Op:         op,
		Status:     status,
		RetryAfter: ParseRetryAfter(header.Get("Retry-After"), now),
	}
	var eb errorBody
	if json.Unmarshal(body, &eb) == nil && (eb.ErrorType != "" || eb.Message != "" || len(eb.Errors) > 0) {
		e.ErrorType = eb.ErrorType
		e.Message = eb.Message
		e.Fields = eb.Errors
	} else if text := strings.TrimSpace(string(body)); text != "" {
		e.Message = text
	}
	// Scrub first, then cut: a secret that straddles the cut would otherwise survive as a prefix.
	e.Message = truncate(scrub(e.Message, secrets...), maxErrorMessage)
	for i := range e.Fields {
		e.Fields[i].Field = truncate(scrub(e.Fields[i].Field, secrets...), maxErrorMessage)
		e.Fields[i].Message = truncate(scrub(e.Fields[i].Message, secrets...), maxErrorMessage)
	}
	return e
}

// newTransportError classifies a failure of http.Client.Do.
func newTransportError(ctx context.Context, op string, err error, secrets ...string) *Error {
	e := &Error{Op: op, Kind: KindNetwork, Message: scrub(err.Error(), secrets...)}
	var ne net.Error
	switch {
	case errors.Is(err, errRedirectRefused):
		e.Kind = KindRedirect
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		e.Kind = KindCanceled
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout()):
		e.Kind = KindTimeout
	}
	// Keep the cause for errors.Is, but as an error whose text is scrubbed too.
	e.Err = scrubbedError{err: err, text: e.Message}
	return e
}

// scrubbedError wraps a cause with a text that has been scrubbed of secrets, while keeping the
// original reachable through Unwrap for errors.Is / errors.As.
type scrubbedError struct {
	err  error
	text string
}

func (s scrubbedError) Error() string { return s.text }
func (s scrubbedError) Unwrap() error { return s.err }

// scrub replaces every occurrence of a non empty secret in s.
func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ParseRetryAfter parses a Retry-After header: a number of seconds or an HTTP date. An empty,
// malformed or past value is 0.
func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// errRedirectRefused marks the refusal to follow a redirect to another host.
var errRedirectRefused = errors.New("redirect to another host refused")
