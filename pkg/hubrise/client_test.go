package hubrise_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
)

const secretToken = "tok-very-secret-123"

// stub starts a server running h and returns a client for it that is not rate limited.
func stub(t *testing.T, h http.HandlerFunc, opts ...hubrise.ClientOption) (*hubrise.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	opts = append([]hubrise.ClientOption{hubrise.WithLimiter(hubrise.NewLimiter(hubrise.Limits{}))}, opts...)
	c, err := hubrise.New(srv.URL+"/v1", secretToken, opts...)
	require.NoError(t, err)
	return c, srv
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestNewValidatesItsInput(t *testing.T) {
	for _, base := range []string{"", "api.hubrise.com/v1", "ftp://x/v1", "http://", "http://bad host/"} {
		_, err := hubrise.New(base, "tok")
		assert.Error(t, err, "%q accepted", base)
	}
	_, err := hubrise.New("https://api.hubrise.com/v1", "  ")
	assert.Error(t, err)
	c, err := hubrise.New(" https://api.hubrise.com/v1/ ", "tok")
	require.NoError(t, err)
	assert.Equal(t, "hubrise.Client{https://api.hubrise.com/v1}", c.String())
}

func TestTheTokenNeverShowsWhateverTheFormatting(t *testing.T) {
	c, err := hubrise.New("https://api.hubrise.com/v1", secretToken)
	require.NoError(t, err)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		assert.NotContains(t, fmt.Sprintf(verb, c), secretToken, verb)
		assert.NotContains(t, fmt.Sprintf(verb, *c), secretToken, verb)
	}

	o, err := hubrise.NewOAuth("https://manager.hubrise.com/oauth2/v1", "cid", "csecret-xyz")
	require.NoError(t, err)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assert.NotContains(t, fmt.Sprintf(verb, o), "csecret-xyz", verb)
		assert.NotContains(t, fmt.Sprintf(verb, *o), "csecret-xyz", verb)
	}

	tok := hubrise.TokenResponse{AccessToken: secretToken, LocationID: "l1"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assert.NotContains(t, fmt.Sprintf(verb, tok), secretToken, verb)
	}
}

func TestRequestShape(t *testing.T) {
	var got *http.Request
	var body string
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		var sb strings.Builder
		_, _ = fmt.Fprint(&sb, r.Header.Get("Content-Type"))
		body = sb.String()
		respond(200, `{"id":"o1"}`)(w, r)
	}, hubrise.WithUserAgent("tsb-test/1"))

	_, err := c.GetOrder(context.Background(), "a/b c?d")
	require.NoError(t, err)
	assert.Equal(t, secretToken, got.Header.Get("X-Access-Token"))
	assert.Equal(t, "application/json", got.Header.Get("Accept"))
	assert.Equal(t, "tsb-test/1", got.Header.Get("User-Agent"))
	assert.Equal(t, "/v1/location/orders/a%2Fb%20c%3Fd", got.URL.EscapedPath())
	assert.Empty(t, got.Header.Get("Content-Type"), "no content type without a body")
	assert.Empty(t, got.URL.RawQuery)

	_, err = c.CreateOrder(context.Background(), &hubrise.Order{Status: hubrise.StatusNew})
	require.NoError(t, err)
	assert.Equal(t, "application/json", body)
	assert.Equal(t, http.MethodPost, got.Method)
}

func TestStatusClassification(t *testing.T) {
	envelope := func(typ, msg string) string {
		return fmt.Sprintf(`{"error_type":%q,"message":%q}`, typ, msg)
	}
	for _, tc := range []struct {
		status     int
		body       string
		header     http.Header
		want       hubrise.Kind
		retryable  bool
		connection bool
	}{
		{401, envelope("unauthorized", "The connection has been blocked"), nil, hubrise.KindUnauthorized, false, true},
		{403, envelope("forbidden", "missing scope"), nil, hubrise.KindForbidden, false, true},
		{404, envelope("not_found", "no such order"), nil, hubrise.KindNotFound, false, false},
		{400, envelope("unprocessable_entity", "bad code"), nil, hubrise.KindInvalid, false, false},
		{415, envelope("unsupported_media_type", "json only"), nil, hubrise.KindInvalid, false, false},
		{422, `{"message":"Validation failed","error_type":"unprocessable_entity","errors":[{"field":"price","message":"'abc' is not a valid monetary amount"}]}`, nil, hubrise.KindInvalid, false, false},
		{429, envelope("too_many_requests", "slow down"), http.Header{"Retry-After": {"42"}}, hubrise.KindRateLimited, true, false},
		{500, envelope("internal_error", "oops"), nil, hubrise.KindServer, true, false},
		{503, "<html>maintenance</html>", nil, hubrise.KindServer, true, false},
		{409, "", nil, hubrise.KindUnknown, false, false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header()[k] = v
				}
				respond(tc.status, tc.body)(w, r)
			})
			_, err := c.GetLocation(context.Background())
			var he *hubrise.Error
			require.ErrorAs(t, err, &he)
			assert.Equal(t, tc.want, he.Kind)
			assert.Equal(t, tc.status, he.Status)
			assert.Equal(t, "GET /location", he.Op)
			assert.Equal(t, tc.retryable, he.Retryable())
			assert.Equal(t, tc.connection, he.ConnectionLevel())
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.want, hubrise.KindOf(err))
			if tc.status == 429 {
				assert.Equal(t, 42*time.Second, he.RetryAfter)
			}
			if tc.status == 422 {
				require.Len(t, he.Fields, 1)
				assert.Equal(t, "price", he.Fields[0].Field)
			}
		})
	}
}

func TestErrorsNeverContainTheToken(t *testing.T) {
	// A server that echoes the credential it was sent, in the body and in the transport.
	c, srv := stub(t, func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Access-Token")
		respond(401, fmt.Sprintf(`{"error_type":"unauthorized","message":"bad token %s","errors":[{"field":"%s","message":"%s"}]}`, tok, tok, tok))(w, r)
	})
	_, err := c.GetLocation(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretToken)
	assert.Contains(t, err.Error(), "[REDACTED]")
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.NotContains(t, fmt.Sprintf("%+v %#v", he, he), secretToken)

	srv.Close()
	_, err = c.GetLocation(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretToken)
	assert.Equal(t, hubrise.KindNetwork, hubrise.KindOf(err))
}

func TestARedirectToAnotherHostIsRefusedAndTheTokenNeverLeaves(t *testing.T) {
	var elsewhereHit bool
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereHit = true
		leaked = r.Header.Get("X-Access-Token")
		respond(200, `{"id":"x"}`)(w, r)
	}))
	t.Cleanup(other.Close)

	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	})
	_, err := c.GetLocation(context.Background())
	require.Error(t, err)
	assert.Equal(t, hubrise.KindRedirect, hubrise.KindOf(err))
	assert.False(t, elsewhereHit, "the other host must never be called")
	assert.Empty(t, leaked)
	assert.NotContains(t, err.Error(), secretToken)
}

func TestASameHostRedirectIsFollowedButNotForever(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/moved" {
			respond(200, `{"id":"loc"}`)(w, r)
			return
		}
		http.Redirect(w, r, "/v1/moved", http.StatusFound)
	})
	loc, err := c.GetLocation(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "loc", loc.ID)

	loop, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	})
	_, err = loop.GetLocation(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestTimeoutsAndCancellation(t *testing.T) {
	release := make(chan struct{})
	slow, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}, hubrise.WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))
	t.Cleanup(func() { close(release) })

	_, err := slow.GetLocation(context.Background())
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindTimeout, he.Kind)
	assert.True(t, he.Retryable())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	slow2, _ := stub(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	_, err = slow2.GetLocation(ctx)
	assert.Equal(t, hubrise.KindTimeout, hubrise.KindOf(err))
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = slow2.GetLocation(ctx2)
	assert.Equal(t, hubrise.KindCanceled, hubrise.KindOf(err))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestAnUndecodableResponseIsAProtocolError(t *testing.T) {
	c, _ := stub(t, respond(200, `<html>not json</html>`))
	_, err := c.GetLocation(context.Background())
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindProtocol, he.Kind)
	assert.Equal(t, 200, he.Status)
	assert.False(t, he.Retryable())
}

func TestAnEmptySuccessBodyAndNotModifiedLeaveTheTargetAlone(t *testing.T) {
	c, _ := stub(t, respond(http.StatusNotModified, ""))
	page, err := c.ListCallbackEvents(context.Background(), 0, "")
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Empty(t, page.NextCursor)

	empty, _ := stub(t, respond(200, ""))
	loc, err := empty.GetLocation(context.Background())
	require.NoError(t, err)
	assert.Empty(t, loc.ID)
}

func TestRequestThatCannotBeEncodedIsAProtocolError(t *testing.T) {
	c, _ := stub(t, respond(200, `{}`))
	_, err := c.CreateOrder(context.Background(), &hubrise.Order{CustomFields: []byte("{broken")})
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindProtocol, he.Kind)
	assert.Equal(t, "POST /location/orders", he.Op)
}

// autoClock is a limiter clock where waiting advances time instead of blocking.
type autoClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *autoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *autoClock) After(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch, func() {}
}

func TestA429PausesTheWholeConnection(t *testing.T) {
	clock := &autoClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	limiter := hubrise.NewLimiter(hubrise.Limits{}, hubrise.WithClock(clock))
	calls := 0
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("Retry-After", "7")
			respond(429, `{"error_type":"too_many_requests"}`)(w, r)
		case 2:
			respond(429, `{"error_type":"too_many_requests"}`)(w, r) // no Retry-After
		default:
			respond(200, `{"id":"loc"}`)(w, r)
		}
	}, hubrise.WithLimiter(limiter))

	_, err := c.GetLocation(context.Background())
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
	assert.Empty(t, clock.waits, "the first call went out at once")

	_, err = c.GetLocation(context.Background()) // waits 7 s first, then gets a 429 without Retry-After
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
	require.Len(t, clock.waits, 1)
	assert.Equal(t, 7*time.Second, clock.waits[0])

	_, err = c.GetLocation(context.Background()) // waits the default 30 s
	require.NoError(t, err)
	require.Len(t, clock.waits, 2)
	assert.Equal(t, 30*time.Second, clock.waits[1])
}

func TestTheLimiterIsConsultedWithTheContextClassAndBucket(t *testing.T) {
	clock := &autoClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	limiter := hubrise.NewLimiter(hubrise.Limits{PerHour: 10, BackgroundShare: 0.5}, hubrise.WithClock(clock))
	c, _ := stub(t, respond(200, `{"id":"loc","data":{}}`), hubrise.WithLimiter(limiter))

	bg := hubrise.Background(context.Background())
	for range 5 {
		_, err := c.GetLocation(bg)
		require.NoError(t, err)
	}
	_, err := c.GetLocation(bg)
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindRateLimited, he.Kind)
	assert.Equal(t, "GET /location", he.Op)
	assert.Greater(t, he.RetryAfter, time.Duration(0))

	_, err = c.GetLocation(context.Background())
	require.NoError(t, err, "critical calls keep their budget")
}

func TestALimiterWaitEndedByTheContext(t *testing.T) {
	limiter := hubrise.NewLimiter(hubrise.Limits{PerMinute: 1})
	c, _ := stub(t, respond(200, `{}`), hubrise.WithLimiter(limiter))
	_, err := c.GetLocation(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.GetLocation(ctx)
	assert.Equal(t, hubrise.KindTimeout, hubrise.KindOf(err))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestImageDataIsBounded(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, 9<<20))
	})
	_, err := c.GetImageData(context.Background(), "cat", "img")
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindProtocol, he.Kind)
	assert.Contains(t, he.Message, "too large")
}

func TestABodyThatBreaksMidWayIsANetworkError(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("short"))
	})
	_, err := c.GetLocation(context.Background())
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindNetwork, he.Kind)
	assert.Equal(t, 200, he.Status)
}
