// Package hubrisetest is an in-memory HubRise for tests of code that talks to the HubRise API: the
// order push, the status sync, the catalog import, the inventory sync and the OAuth connection.
//
// It serves the endpoints the integration uses (the OAuth token and revoke, GET /location, the
// catalog, the images, orders with private_ref lookups, the customer list, the inventory, the
// callback registration and its events) and follows the rules of the real API that the integration depends on:
//
//   - the order total is calculated (items + charges - discounts), never taken from the request,
//   - private_ref is only visible to the client that created the order,
//   - a callback registration naming an unknown resource or event is a 422,
//   - a client never receives the events of its own writes,
//   - private_ref of a customer is only visible to the client that created it,
//   - the list endpoints are limited to 10 requests a minute on top of the connection wide limits,
//   - a request body with a field the API does not know is a 422,
//   - a revoked token is a 401.
//
// The side of the world that is not the system under test (the EPOS, another sales channel) is
// played through the control methods: InjectOrder, SetOrderStatus, ReplaceCatalog, SetInventory.
// Faults (a 401, a 429, a 5xx, a timeout, a response dropped after the request took effect) are
// injected with AddFault, every request is logged (Requests), and Dispatch posts the queued events
// to the registered callback URL signed like HubRise signs them.
//
// It is plain code that lives next to the client (so the client tests and the tests of the
// workers share it) and, like apnstest and fakeupstream, is excluded from the coverage gate.
package hubrisetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/timezone"
)

// The identities of the mock. Token is the system under test, EPOSToken the other side.
const (
	// Token is the access token of the system under test (client "tsb-webshop").
	Token = "tsb-access-token"
	// EPOSToken is the access token of the other client (the EPOS "pili-pos"), used by the control
	// methods to play the rest of the world.
	EPOSToken = "epos-access-token"

	// ClientID and ClientSecret are the OAuth client of the system under test.
	ClientID     = "tsb-client-id.clients.hubrise.com"
	ClientSecret = "tsb-client-secret"

	AccountID      = "15n44"
	AccountName    = "Tokyo Sushi (mock)"
	LocationID     = "15n44-1"
	LocationName   = "Liege"
	CatalogID      = "3p5j6"
	CatalogName    = "Menu"
	CustomerListID = "cl1x9"
	CustomerList   = "Clients"
)

const maxBody = 32 << 20

// Option configures a Server.
type Option func(*Server)

// WithClock sets the time source of the mock (timestamps, expiries, rate limit windows).
func WithClock(now func() time.Time) Option { return func(s *Server) { s.clock = now } }

// WithStrictTransitions makes final order statuses (completed, rejected, cancelled,
// delivery_failed) immutable and forbids going back to new. HubRise documents no transition
// graph, so the default allows any documented status after any other.
func WithStrictTransitions() Option { return func(s *Server) { s.strict = true } }

// Limits are the rate limits of the mock, per access token. A value <= 0 disables the window.
type Limits struct {
	PerMinute, PerHour, PerDay int
	// HeavyPerMinute is the shared bucket of GET /catalogs/:id and the list endpoints (orders and
	// customers).
	HeavyPerMinute int
}

// DefaultLimits are HubRise's documented limits.
func DefaultLimits() Limits {
	return Limits{PerMinute: 500, PerHour: 2500, PerDay: 10000, HeavyPerMinute: 10}
}

type client struct {
	token    string
	name     string
	callback *hubrise.Callback
	events   []hubrise.Event
	// scopes are the permissions the token was granted ("catalog.write"); nil means all of them.
	scopes []string
}

type storedOrder struct {
	order hubrise.Order
	owner string
}

type image struct {
	meta hubrise.Image
	data []byte
	// attached is true once a catalog PUT referenced the image (it is then never purged).
	attached bool
}

// Server is the in-memory HubRise.
type Server struct {
	// URL is the root of the server when it was started by New.
	URL string

	http *httptest.Server
	mux  *http.ServeMux

	mu     sync.Mutex
	clock  func() time.Time
	strict bool

	clients map[string]*client
	revoked map[string]bool
	codes   map[string]bool
	seq     int

	orders  map[string]*storedOrder
	orderID []string

	customers  map[string]*storedCustomer
	customerID []string

	catalog   hubrise.Catalog
	inventory []hubrise.InventoryEntry
	images    map[string]image
	imageSeq  []string

	// rawData is the catalog content as stored after a PUT (or ReplaceCatalogRaw), with the keys the
	// typed structs do not model. nil until then: the typed catalog is served.
	rawData json.RawMessage
	// accountLevel makes the catalog an account catalog: a location token may read it but not
	// modify it (401).
	accountLevel bool
	puts         int
	// codeScopes remembers the scope each authorization code was asked with.
	codeScopes map[string]string

	// target overrides what the token endpoint says the authorization gives access to.
	target *OAuthTarget

	faults  []*Fault
	log     []Request
	limits  Limits
	windows map[string][]time.Time
	heavy   map[string][]time.Time
}

// New starts a mock HubRise on a local port, stopped when the test ends.
func New(tb testing.TB, opts ...Option) *Server {
	tb.Helper()
	s := NewHandler(opts...)
	s.http = httptest.NewServer(s)
	s.URL = s.http.URL
	tb.Cleanup(s.http.Close)
	return s
}

// NewHandler returns a mock that is an http.Handler, not yet listening (for a standalone dev
// server). Use New in tests.
func NewHandler(opts ...Option) *Server {
	s := &Server{
		clock:      time.Now,
		clients:    map[string]*client{},
		revoked:    map[string]bool{},
		codes:      map[string]bool{},
		codeScopes: map[string]string{},
		orders:     map[string]*storedOrder{},
		customers:  map[string]*storedCustomer{},
		images:     map[string]image{},
		limits:     DefaultLimits(),
		windows:    map[string][]time.Time{},
		heavy:      map[string][]time.Time{},
	}
	s.clients[Token] = &client{token: Token, name: "tsb-webshop"}
	s.clients[EPOSToken] = &client{token: EPOSToken, name: "pili-pos"}
	for _, o := range opts {
		o(s)
	}
	s.catalog = s.freshCatalog()
	s.routes()
	return s
}

// APIBaseURL is the value for HUBRISE_API_BASE_URL.
func (s *Server) APIBaseURL() string { return s.URL + "/v1" }

// OAuthBaseURL is the value for HUBRISE_OAUTH_BASE_URL.
func (s *Server) OAuthBaseURL() string { return s.URL + "/oauth2/v1" }

// HTTPClient is the http.Client to use against the server.
func (s *Server) HTTPClient() *http.Client {
	if s.http == nil {
		return http.DefaultClient
	}
	return s.http.Client()
}

// NewClient returns a hubrise.Client for the system under test with no rate limiting (pass
// hubrise.WithLimiter to change that).
func (s *Server) NewClient(tb testing.TB, opts ...hubrise.ClientOption) *hubrise.Client {
	tb.Helper()
	all := append([]hubrise.ClientOption{
		hubrise.WithHTTPClient(s.HTTPClient()),
		hubrise.WithLimiter(hubrise.NewLimiter(hubrise.Limits{})),
	}, opts...)
	c, err := hubrise.New(s.APIBaseURL(), Token, all...)
	if err != nil {
		tb.Fatalf("hubrisetest: new client: %v", err)
	}
	return c
}

// NewOAuth returns the OAuth flow of the system under test against the mock.
func (s *Server) NewOAuth(tb testing.TB) *hubrise.OAuth {
	tb.Helper()
	o, err := hubrise.NewOAuth(s.OAuthBaseURL(), ClientID, ClientSecret, hubrise.WithOAuthHTTPClient(s.HTTPClient()))
	if err != nil {
		tb.Fatalf("hubrisetest: new oauth: %v", err)
	}
	return o
}

// now is the mock's clock in the restaurant's time zone.
func (s *Server) now() time.Time { return s.clock().In(timezone.Location) }

// nextID returns a fresh id with the prefix.
func (s *Server) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s%05d", prefix, s.seq)
}

// SetLimits changes the rate limits.
func (s *Server) SetLimits(l Limits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = l
}

// ---- request logging and fault injection ----

// Request is one API request the mock received.
type Request struct {
	Method string
	// Path is the path below the root, "/v1/location/orders".
	Path  string
	Query string
	// Client is the name of the client that sent it ("tsb-webshop"), empty without a valid token.
	Client string
	Body   json.RawMessage
	// Status is what the mock answered; 0 when it answered nothing (timeout fault).
	Status int
	// Dropped is true when the request took effect but its response was never sent.
	Dropped bool
}

// Requests returns every request received so far, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// RequestsTo returns the requests with the method and a path starting with prefix.
func (s *Server) RequestsTo(method, pathPrefix string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if (method == "" || r.Method == method) && strings.HasPrefix(r.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

// ClearRequests forgets the request log.
func (s *Server) ClearRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = nil
}

// Fault makes matching requests misbehave.
type Fault struct {
	// Method and Path select the requests: an empty Method matches any, Path is a prefix of the
	// path below the root ("/v1/location/orders"); empty matches any API path.
	Method, Path string
	// Times is how many requests it applies to; 0 is every matching request.
	Times int

	// Status answers with this HubRise error (401, 403, 404, 422, 429, 500...) without processing
	// the request.
	Status int
	// RetryAfter is sent as the Retry-After header of a 429.
	RetryAfter time.Duration
	// Delay waits before answering (a slow HubRise).
	Delay time.Duration
	// Timeout holds the request until the client gives up, answering nothing.
	Timeout bool
	// DropAfter processes the request normally, then closes the connection without answering: the
	// request took effect but the caller never learns it (the case that needs a private_ref lookup).
	DropAfter bool
	// OnStart, when set, is called as soon as a matching request arrives, before any Delay or
	// Timeout: a test can act while the request is in flight.
	OnStart func()
}

// AddFault injects a fault.
func (s *Server) AddFault(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &f)
}

// ClearFaults removes every fault.
func (s *Server) ClearFaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = nil
}

// takeFault returns the first fault matching the request and consumes one use of it.
func (s *Server) takeFault(method, path string) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.faults {
		if (f.Method != "" && f.Method != method) || !strings.HasPrefix(path, f.Path) {
			continue
		}
		cp := *f
		if f.Times > 0 {
			f.Times--
			if f.Times == 0 {
				s.faults = append(s.faults[:i:i], s.faults[i+1:]...)
			}
		}
		return &cp
	}
	return nil
}

// ---- serving ----

// ServeHTTP implements http.Handler: it runs the fault, auth and rate limit layers around the
// routes and logs the exchange.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	r.Body = io.NopCloser(bytes.NewReader(body))

	entry := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
	if len(bytes.TrimSpace(body)) > 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		entry.Body = json.RawMessage(bytes.Clone(body))
	}
	isAPI := strings.HasPrefix(r.URL.Path, "/v1/")
	if isAPI {
		s.mu.Lock()
		if c := s.clients[r.Header.Get("X-Access-Token")]; c != nil {
			entry.Client = c.name
		}
		s.mu.Unlock()
	}

	var fault *Fault
	if isAPI {
		fault = s.takeFault(r.Method, r.URL.Path)
	}
	if fault != nil {
		if fault.OnStart != nil {
			fault.OnStart()
		}
		if fault.Timeout {
			<-r.Context().Done()
			s.record(entry)
			return
		}
		if fault.Delay > 0 {
			select {
			case <-time.After(fault.Delay):
			case <-r.Context().Done():
				s.record(entry)
				return
			}
		}
		if fault.Status != 0 {
			rec := httptest.NewRecorder()
			if fault.RetryAfter > 0 {
				rec.Header().Set("Retry-After", fmt.Sprintf("%d", int(fault.RetryAfter.Seconds())))
			}
			writeAPIError(rec, fault.Status, fmt.Sprintf("injected fault %d", fault.Status))
			entry.Status = rec.Code
			s.record(entry)
			flush(w, rec)
			return
		}
	}

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, r)
	entry.Status = rec.Code
	if fault != nil && fault.DropAfter {
		entry.Dropped = true
		s.record(entry)
		drop(w)
		return
	}
	s.record(entry)
	flush(w, rec)
}

func (s *Server) record(e Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, e)
}

// flush copies a recorded response to the real writer.
func flush(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// drop closes the connection without answering.
func drop(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	w.WriteHeader(http.StatusBadGateway)
}

// apiError is an error the API answers with.
type apiError struct {
	status  int
	message string
	fields  []hubrise.FieldError
}

func errStatus(status int, message string) *apiError {
	return &apiError{status: status, message: message}
}

func errField(field, message string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, message: field + ": " + message,
		fields: []hubrise.FieldError{{Field: field, Message: message}}}
}

func errFields(fields []hubrise.FieldError) *apiError {
	if len(fields) == 0 {
		return nil
	}
	return &apiError{status: http.StatusUnprocessableEntity, message: "Validation failed", fields: fields}
}

func errorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	case status == http.StatusTooManyRequests:
		return "too_many_requests"
	case status >= 500:
		return "internal_error"
	default:
		return "unprocessable_entity"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"message": message, "error_type": errorType(status)})
}

func writeErr(w http.ResponseWriter, err *apiError) {
	body := map[string]any{"message": err.message, "error_type": errorType(err.status)}
	if len(err.fields) > 0 {
		body["errors"] = err.fields
	}
	writeJSON(w, err.status, body)
}

// rawResponse is a non JSON answer (an image).
type rawResponse struct {
	contentType string
	data        []byte
}

// handler is an API handler that runs with the lock held, for an authenticated client.
type handler func(s *Server, c *client, r *http.Request, body []byte, h http.Header) (status int, out any, err *apiError)

// api wraps a handler with authentication, the content type check and the rate limits.
func (s *Server) api(h handler, heavy bool) http.HandlerFunc { return s.apiBody(h, heavy, false) }

// apiRaw is api for an endpoint whose body is not JSON (an image upload).
func (s *Server) apiRaw(h handler) http.HandlerFunc { return s.apiBody(h, false, true) }

func (s *Server) apiBody(h handler, heavy, rawBody bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()

		token := r.Header.Get("X-Access-Token")
		c := s.clients[token]
		if c == nil || s.revoked[token] {
			writeAPIError(w, http.StatusUnauthorized, "The access token is invalid or the connection has been revoked")
			return
		}
		if !rawBody && len(bytes.TrimSpace(body)) > 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeAPIError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		if wait, limited := s.rateLimited(token, heavy); limited {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
			writeAPIError(w, http.StatusTooManyRequests, "Too many requests")
			return
		}
		status, out, err := h(s, c, r, body, w.Header())
		if err != nil {
			writeErr(w, err)
			return
		}
		switch v := out.(type) {
		case nil:
			w.WriteHeader(status)
		case rawResponse:
			w.Header().Set("Content-Type", v.contentType)
			w.WriteHeader(status)
			_, _ = w.Write(v.data)
		default:
			writeJSON(w, status, out)
		}
	}
}

// rateLimited counts the request and reports whether it exceeds a window, with the time until it
// would not.
func (s *Server) rateLimited(token string, heavy bool) (time.Duration, bool) {
	now := s.clock()
	prune := func(ts []time.Time, window time.Duration) []time.Time {
		i := 0
		for i < len(ts) && !ts[i].After(now.Add(-window)) {
			i++
		}
		return ts[i:]
	}
	s.windows[token] = prune(s.windows[token], 24*time.Hour)
	s.heavy[token] = prune(s.heavy[token], time.Minute)
	count := func(ts []time.Time, window time.Duration) int {
		n := 0
		for _, t := range ts {
			if t.After(now.Add(-window)) {
				n++
			}
		}
		return n
	}
	all := s.windows[token]
	for _, w := range []struct {
		limit  int
		window time.Duration
	}{{s.limits.PerMinute, time.Minute}, {s.limits.PerHour, time.Hour}, {s.limits.PerDay, 24 * time.Hour}} {
		if w.limit > 0 && count(all, w.window) >= w.limit {
			return w.window, true
		}
	}
	if heavy && s.limits.HeavyPerMinute > 0 && len(s.heavy[token]) >= s.limits.HeavyPerMinute {
		return time.Minute, true
	}
	s.windows[token] = append(all, now)
	if heavy {
		s.heavy[token] = append(s.heavy[token], now)
	}
	return 0, false
}

// routes registers the endpoints.
func (s *Server) routes() {
	m := http.NewServeMux()
	// OAuth.
	m.HandleFunc("GET /oauth2/v1/authorize", s.oauthAuthorize)
	m.HandleFunc("POST /oauth2/v1/token", s.oauthToken)
	m.HandleFunc("POST /oauth2/v1/revoke", s.oauthRevoke)

	// Location.
	m.HandleFunc("GET /v1/location", s.api(getLocation, false))
	// Orders.
	m.HandleFunc("POST /v1/location/orders", s.api(createOrder, false))
	m.HandleFunc("GET /v1/location/orders", s.api(listOrders, true))
	m.HandleFunc("GET /v1/location/orders/{id}", s.api(getOrder, false))
	m.HandleFunc("PATCH /v1/location/orders/{id}", s.api(patchOrder, false))
	// Customer list.
	m.HandleFunc("POST /v1/customer_lists/{list}/customers", s.api(createCustomer, false))
	m.HandleFunc("GET /v1/customer_lists/{list}/customers", s.api(listCustomers, true))
	m.HandleFunc("PATCH /v1/customer_lists/{list}/customers/{id}", s.api(patchCustomer, false))
	m.HandleFunc("POST /v1/customer_lists/{list}/customers/{id}/anonymise", s.api(anonymiseCustomer, false))
	// Catalog and images.
	m.HandleFunc("GET /v1/catalogs/{id}", s.api(getCatalog, true))
	m.HandleFunc("PUT /v1/catalogs/{id}", s.api(putCatalog, false))
	m.HandleFunc("GET /v1/catalogs/{id}/images", s.api(listImages, false))
	m.HandleFunc("POST /v1/catalogs/{id}/images", s.apiRaw(postImage))
	m.HandleFunc("GET /v1/catalogs/{id}/images/{image}", s.api(getImage, false))
	m.HandleFunc("GET /v1/catalogs/{id}/images/{image}/data", s.api(getImageData, false))
	// Inventory: unscoped, and scoped by catalog (and location).
	for _, p := range []string{"/v1/location/inventory", "/v1/catalogs/{id}/location/inventory", "/v1/catalogs/{id}/locations/{loc}/inventory"} {
		m.HandleFunc("GET "+p, s.api(getInventory, false))
		m.HandleFunc("PUT "+p, s.api(putInventory, false))
		m.HandleFunc("PATCH "+p, s.api(patchInventory, false))
	}
	// Callbacks.
	m.HandleFunc("GET /v1/callback", s.api(getCallback, false))
	m.HandleFunc("POST /v1/callback", s.api(postCallback, false))
	m.HandleFunc("DELETE /v1/callback", s.api(deleteCallback, false))
	m.HandleFunc("GET /v1/callback/events", s.api(listEvents, false))
	m.HandleFunc("DELETE /v1/callback/events/{id}", s.api(ackEvent, false))

	// Anything else is HubRise's routing error.
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"message":    "no route matches " + r.Method + " " + r.URL.Path,
			"error_type": "routing_error",
		})
	})
	s.mux = m
}

func getLocation(_ *Server, _ *client, _ *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	return http.StatusOK, map[string]any{
		"id":               LocationID,
		"account_id":       AccountID,
		"name":             LocationName,
		"catalog_id":       CatalogID,
		"customer_list_id": CustomerListID,
		"country":          "BE",
		"timezone":         map[string]any{"name": timezone.RestaurantTZ},
	}, nil
}

// checkHTTPURL fails unless raw is an absolute http(s) URL.
func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("not an http(s) URL: %q", raw)
	}
	return nil
}
