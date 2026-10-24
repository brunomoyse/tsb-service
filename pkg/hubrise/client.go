// Package hubrise is a client for the HubRise REST API (https://www.hubrise.com/developers) and the
// OAuth 2.0 flow that connects a location to it.
//
// The client is deliberately small and defensive: every call goes through a per connection
// Limiter (HubRise caps a connection at 500 requests a minute, 2,500 an hour and 10,000 a day, and
// 10 a minute on catalog and order list reads), failures come back as one typed *Error classified
// by what the caller should do (401/403 pause the pipeline, 422 park the item, 429/5xx/timeouts
// retry), redirects to another host are refused so the access token can never be sent elsewhere,
// and the token never appears in an error, a log field or a printed value.
//
// pkg/hubrise/hubrisetest is an in-memory HubRise for tests.
package hubrise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIBaseURL is the HubRise API.
	DefaultAPIBaseURL = "https://api.hubrise.com/v1"
	// DefaultTimeout bounds one request when the caller does not supply an http.Client.
	DefaultTimeout = 20 * time.Second

	// maxResponseBytes bounds what is read of a JSON response (a large catalog is a few MB).
	maxResponseBytes = 32 << 20
	// maxImageBytes bounds a downloaded image (HubRise caps uploads at 1 MB).
	maxImageBytes = 8 << 20
	// maxRedirects is how many same host redirects are followed.
	maxRedirects = 3

	// defaultPause is how long a 429 without Retry-After stops the connection.
	defaultPause = 30 * time.Second
)

// secret holds an access token and never shows it: it prints and marshals as "[REDACTED]".
type secret string

func (secret) String() string               { return "[REDACTED]" }
func (secret) GoString() string             { return "[REDACTED]" }
func (secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte("[REDACTED]")) }
func (secret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (secret) MarshalText() ([]byte, error) { return []byte("[REDACTED]"), nil }

// Client calls the HubRise API of one connection (one location) with its access token. It is safe
// for concurrent use.
type Client struct {
	base      *url.URL
	token     secret
	http      *http.Client
	limiter   *Limiter
	userAgent string
	now       func() time.Time
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient sets the HTTP client (the default has a 20 s timeout). The client is copied and
// its redirect policy replaced by the host pinning one.
func WithHTTPClient(h *http.Client) ClientOption { return func(c *Client) { c.http = h } }

// WithLimiter sets the limiter. Clients of the same connection must share one; the default is a
// private DefaultLimits limiter, which is only right for a long lived Client.
func WithLimiter(l *Limiter) ClientOption { return func(c *Client) { c.limiter = l } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) ClientOption { return func(c *Client) { c.userAgent = ua } }

// New returns a Client for the API at baseURL (an absolute http(s) URL, no trailing slash needed)
// authenticated with token.
func New(baseURL, token string, opts ...ClientOption) (*Client, error) {
	base, err := parseBase(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("hubrise: empty access token")
	}
	c := &Client{base: base, token: secret(token), userAgent: "tsb-service", now: time.Now}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: DefaultTimeout}
	}
	c.http = pinHost(c.http, base)
	if c.limiter == nil {
		c.limiter = NewLimiter(DefaultLimits())
	}
	return c, nil
}

// String implements fmt.Stringer without the token.
func (c Client) String() string { return "hubrise.Client{" + c.base.String() + "}" }

// Format implements fmt.Formatter, so no verb (%v, %+v, %#v, %s) can print the unexported token
// field, which fmt would otherwise reach by reflection.
func (c Client) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(c.String())) }

func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("hubrise: %q is not an absolute http(s) URL", raw)
	}
	return u, nil
}

// pinHost returns a copy of h that refuses to follow a redirect to another host or scheme, so the
// access token (a custom header, which net/http would forward) can never leave the pinned host.
func pinHost(h *http.Client, base *url.URL) *http.Client {
	pinned := *h
	pinned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != base.Host || req.URL.Scheme != base.Scheme {
			return fmt.Errorf("%w: %s", errRedirectRefused, req.URL.Host)
		}
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &pinned
}

// call describes one API request.
type call struct {
	method string
	path   string // below the base URL, already escaped
	query  url.Values
	bucket Bucket
	// in is marshalled as the JSON body when not nil; out receives the decoded response.
	in  any
	out any
	// raw, when not nil, is sent as the body as is with rawType as its Content-Type (an image
	// upload); it takes precedence over in.
	raw     []byte
	rawType string
}

func (c *Client) op(cl call) string { return cl.method + " " + cl.path }

// response is a successful (2xx or 304) answer.
type response struct {
	status int
	header http.Header
	body   []byte
}

// roundTrip sends a request through the limiter and returns the raw answer, or the typed error.
func (c *Client) roundTrip(ctx context.Context, cl call, limit int64) (*response, error) {
	op := c.op(cl)
	if err := c.limiter.Wait(ctx, ClassFrom(ctx), cl.bucket); err != nil {
		return nil, limiterError(ctx, op, err)
	}

	var body io.Reader
	contentType := "application/json"
	if cl.raw != nil {
		body = bytes.NewReader(cl.raw)
		contentType = cl.rawType
	} else if cl.in != nil {
		b, err := json.Marshal(cl.in)
		if err != nil {
			return nil, &Error{Kind: KindProtocol, Op: op, Message: "encode request", Err: err}
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, cl, body)
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Op: op, Message: "build request", Err: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, newTransportError(ctx, op, err, string(c.token))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readLimited(resp.Body, limit)
	if err != nil {
		return nil, bodyError(ctx, op, resp.StatusCode, err, string(c.token))
	}
	if ok := resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotModified; !ok {
		e := newStatusError(op, resp.StatusCode, resp.Header, raw, c.now(), string(c.token))
		if e.Kind == KindRateLimited {
			c.limiter.Pause(cmpOr(e.RetryAfter, defaultPause))
		}
		return nil, e
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: raw}, nil
}

// do sends a JSON request and decodes the answer into cl.out. It returns the response headers.
func (c *Client) do(ctx context.Context, cl call) (http.Header, error) {
	r, err := c.roundTrip(ctx, cl, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if cl.out != nil && len(bytes.TrimSpace(r.body)) > 0 {
		if err := json.Unmarshal(r.body, cl.out); err != nil {
			return r.header, &Error{Kind: KindProtocol, Op: c.op(cl), Status: r.status,
				Message: "undecodable response: " + scrub(err.Error(), string(c.token)), Err: err}
		}
	}
	return r.header, nil
}

// doBinary downloads a binary body (an image).
func (c *Client) doBinary(ctx context.Context, cl call) (*ImageData, error) {
	r, err := c.roundTrip(ctx, cl, maxImageBytes)
	if err != nil {
		return nil, err
	}
	return &ImageData{ContentType: r.header.Get("Content-Type"), Data: r.body}, nil
}

func (c *Client) newRequest(ctx context.Context, cl call, body io.Reader) (*http.Request, error) {
	decoded, err := url.PathUnescape(cl.path)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = c.base.Path + decoded
	u.RawPath = c.base.EscapedPath() + cl.path
	if len(cl.query) > 0 {
		u.RawQuery = cl.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Access-Token", string(c.token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	return req, nil
}

// limiterError turns the error of Limiter.Wait into an *Error.
func limiterError(ctx context.Context, op string, err error) error {
	var he *Error
	if errors.As(err, &he) {
		cp := *he
		cp.Op = op
		return &cp
	}
	return newTransportError(ctx, op, err)
}

// bodyError classifies a failure to read a response body.
func bodyError(ctx context.Context, op string, status int, err error, secrets ...string) error {
	if errors.Is(err, errTooLarge) {
		return &Error{Kind: KindProtocol, Op: op, Status: status, Message: err.Error(), Err: err}
	}
	e := newTransportError(ctx, op, err, secrets...)
	e.Status = status
	return e
}

var errTooLarge = errors.New("response too large")

// readLimited reads at most limit bytes and fails when there are more.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w (over %d bytes)", errTooLarge, limit)
	}
	return b, nil
}

func cmpOr(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// esc escapes a path segment (an id from the database or from HubRise).
func esc(segment string) string { return url.PathEscape(segment) }

// listPage fetches one page of a list endpoint.
func listPage[T any](ctx context.Context, c *Client, cl call, cursor string) (Page[T], error) {
	q := url.Values{}
	for k, v := range cl.query {
		q[k] = v
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	cl.query = q
	var items []T
	cl.out = &items
	h, err := c.do(ctx, cl)
	if err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Items: items, NextCursor: h.Get("X-Cursor-Next")}, nil
}
