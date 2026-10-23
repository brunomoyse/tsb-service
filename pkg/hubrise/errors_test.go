package hubrise

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKindForStatus(t *testing.T) {
	for status, want := range map[int]Kind{
		401: KindUnauthorized, 403: KindForbidden, 404: KindNotFound,
		400: KindInvalid, 415: KindInvalid, 422: KindInvalid,
		429: KindRateLimited, 500: KindServer, 502: KindServer, 503: KindServer,
		409: KindUnknown, 302: KindUnknown,
	} {
		assert.Equal(t, want, kindForStatus(status), "status %d", status)
	}
}

func TestStatusErrorParsesTheHubRiseEnvelope(t *testing.T) {
	body := `{"message":"Validation failed","error_type":"unprocessable_entity","errors":[{"field":"price","message":"'abc' is not a valid monetary amount"}]}`
	e := newStatusError("POST /location/orders", 422, http.Header{}, []byte(body), time.Now())
	assert.Equal(t, KindInvalid, e.Kind)
	assert.Equal(t, 422, e.Status)
	assert.Equal(t, "unprocessable_entity", e.ErrorType)
	require.Len(t, e.Fields, 1)
	assert.Equal(t, "price", e.Fields[0].Field)
	assert.Contains(t, e.Error(), "POST /location/orders")
	assert.Contains(t, e.Error(), "invalid (422)")
	assert.Contains(t, e.Error(), "price: 'abc' is not a valid monetary amount")
	assert.False(t, e.Retryable())
	assert.False(t, e.ConnectionLevel())
}

func TestStatusErrorWithANonJSONBody(t *testing.T) {
	e := newStatusError("GET /x", 502, http.Header{}, []byte("  <html>bad gateway</html> "), time.Now())
	assert.Equal(t, KindServer, e.Kind)
	assert.Equal(t, "<html>bad gateway</html>", e.Message)
	assert.True(t, e.Retryable())

	e = newStatusError("GET /x", 500, http.Header{}, nil, time.Now())
	assert.Empty(t, e.Message)
	assert.Equal(t, "hubrise: GET /x: server (500)", e.Error())

	long := newStatusError("GET /x", 500, http.Header{}, []byte(strings.Repeat("a", 2000)), time.Now())
	assert.Len(t, long.Message, maxErrorMessage+3)
}

func TestStatusErrorScrubsSecrets(t *testing.T) {
	body := `{"message":"token tok-123 is bad","error_type":"unauthorized","errors":[{"field":"tok-123","message":"tok-123"}]}`
	e := newStatusError("GET /location", 401, http.Header{}, []byte(body), time.Now(), "tok-123")
	assert.NotContains(t, e.Error(), "tok-123")
	assert.Contains(t, e.Error(), "[REDACTED]")
	assert.True(t, e.ConnectionLevel())
	assert.ErrorIs(t, e, KindUnauthorized)
	assert.NotErrorIs(t, e, KindForbidden)
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, 30*time.Second, ParseRetryAfter("30", now))
	assert.Equal(t, 30*time.Second, ParseRetryAfter(" 30 ", now))
	assert.Equal(t, time.Duration(0), ParseRetryAfter("", now))
	assert.Equal(t, time.Duration(0), ParseRetryAfter("-5", now))
	assert.Equal(t, time.Duration(0), ParseRetryAfter("soon", now))
	assert.Equal(t, 90*time.Second, ParseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now))
	assert.Equal(t, time.Duration(0), ParseRetryAfter(now.Add(-time.Hour).UTC().Format(http.TimeFormat), now))

	h := http.Header{"Retry-After": {"12"}}
	e := newStatusError("GET /x", 429, h, nil, now)
	assert.Equal(t, KindRateLimited, e.Kind)
	assert.Equal(t, 12*time.Second, e.RetryAfter)
	assert.True(t, e.Retryable())
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestTransportErrorClassification(t *testing.T) {
	live := context.Background()
	canceled, cancel := context.WithCancel(live)
	cancel()
	expired, cancel2 := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancel2()

	var _ net.Error = timeoutErr{}
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		want Kind
	}{
		"refused":        {live, errors.New("dial tcp: connection refused"), KindNetwork},
		"reset":          {live, errors.New("EOF"), KindNetwork},
		"client timeout": {live, timeoutErr{}, KindTimeout},
		"deadline":       {live, context.DeadlineExceeded, KindTimeout},
		"ctx deadline":   {expired, errors.New("anything"), KindTimeout},
		"canceled":       {canceled, errors.New("anything"), KindCanceled},
		"cause canceled": {live, context.Canceled, KindCanceled},
		"redirect":       {live, errRedirectRefused, KindRedirect},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTransportError(tc.ctx, "GET /x", tc.err)
			assert.Equal(t, tc.want, e.Kind)
			assert.ErrorIs(t, e, tc.err, "the cause stays reachable")
			assert.Equal(t, tc.want == KindTimeout || tc.want == KindNetwork, e.Retryable())
		})
	}
}

func TestTransportErrorScrubsSecretsButKeepsTheCause(t *testing.T) {
	cause := errors.New(`Get "https://x/y?t=tok-123": dial tcp: refused`)
	e := newTransportError(context.Background(), "GET /y", cause, "tok-123")
	assert.NotContains(t, e.Error(), "tok-123")
	assert.NotContains(t, e.Unwrap().Error(), "tok-123")
	assert.ErrorIs(t, e, cause)
}

func TestKindHelpers(t *testing.T) {
	assert.Equal(t, KindServer, KindOf(&Error{Kind: KindServer}))
	assert.Equal(t, KindNotFound, KindOf(errors.Join(errors.New("x"), &Error{Kind: KindNotFound})))
	assert.Equal(t, KindUnknown, KindOf(errors.New("plain")))
	assert.Equal(t, "hubrise: rate_limited", KindRateLimited.Error())
	assert.Equal(t, "unknown", Kind(99).String())
	assert.Equal(t, "unknown", Kind(-1).String())
	for k := KindUnknown; k <= KindCanceled; k++ {
		assert.NotEmpty(t, k.String())
	}
	assert.True(t, (&Error{Kind: KindForbidden}).ConnectionLevel())
	assert.False(t, (&Error{Kind: KindCanceled}).Retryable())
	assert.False(t, (&Error{Kind: KindProtocol}).Retryable())
	assert.False(t, (&Error{Kind: KindRedirect}).Retryable())
}

func TestScrubAndTruncate(t *testing.T) {
	assert.Equal(t, "a [REDACTED] b", scrub("a s3cret b", "s3cret"))
	assert.Equal(t, "unchanged", scrub("unchanged", ""))
	assert.Equal(t, "short", truncate("short", 10))
}

func TestStatusErrorScrubsBeforeItCuts(t *testing.T) {
	const token = "tok-abcdefghijklmnop"
	// The token straddles byte 500: cutting first would leave "tok-abcde" behind.
	text := strings.Repeat("x", maxErrorMessage-5) + token + " tail"
	e := newStatusError("GET /x", 500, http.Header{}, []byte(text), time.Now(), token)
	assert.NotContains(t, e.Message, "tok-", "no prefix of the token survives")
	e = newStatusError("GET /x", 500, http.Header{}, []byte(strings.Repeat("x", 100)+token), time.Now(), token)
	assert.Contains(t, e.Message, "[REDACTED]")

	body := `{"message":"` + text + `","errors":[{"field":"` + text + `","message":"` + text + `"}]}`
	e = newStatusError("GET /x", 422, http.Header{}, []byte(body), time.Now(), token)
	assert.NotContains(t, e.Error(), "tok-")
	require.Len(t, e.Fields, 1)
	assert.NotContains(t, e.Fields[0].Field, "tok-")
	assert.NotContains(t, e.Fields[0].Message, "tok-")
}
