package graphql_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/time/rate"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
	addressInfrastructure "tsb-service/internal/modules/address/infrastructure"
	assistantApplication "tsb-service/internal/modules/assistant/application"
	assistantDomain "tsb-service/internal/modules/assistant/domain"
	couponApplication "tsb-service/internal/modules/coupon/application"
	couponInfrastructure "tsb-service/internal/modules/coupon/infrastructure"
	notificationApplication "tsb-service/internal/modules/notification/application"
	notificationInfrastructure "tsb-service/internal/modules/notification/infrastructure"
	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentInfrastructure "tsb-service/internal/modules/payment/infrastructure"
	posApplication "tsb-service/internal/modules/pos/application"
	posDomain "tsb-service/internal/modules/pos/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantInfrastructure "tsb-service/internal/modules/restaurant/infrastructure"
	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"
	"tsb-service/internal/shared/middleware"
	"tsb-service/pkg/apns"
	"tsb-service/pkg/db"
	es "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/fcm"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/utils"
)

// The environment of the resolver coverage tests: the real services over a real Postgres (like
// setupTestContext) plus every boundary the resolvers reach beyond the database, replaced by a
// fake that records what it was asked: Mollie, APNs, FCM, the Zitadel identity API, Google Places,
// the WeChat agent and an SMTP server for the transactional e-mails.

// ---- SMTP --------------------------------------------------------------------------------------

type sentMail struct {
	From string
	To   []string
	Data string
}

type fakeSMTP struct {
	mu   sync.Mutex
	mail []sentMail
}

var (
	smtpOnce   sync.Once
	sharedSMTP = &fakeSMTP{}
)

// startFakeSMTP points the e-mail package at an in-process SMTP server, once for the whole test
// binary (the e-mail backend is process-wide state).
func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	smtpOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		host, port, err := net.SplitHostPort(ln.Addr().String())
		require.NoError(t, err)
		require.NoError(t, os.Setenv("SMTP_HOST", host))
		require.NoError(t, os.Setenv("SMTP_PORT", port))
		require.NoError(t, os.Setenv("SCW_SENDER_EMAIL", "noreply@example.test"))
		require.NoError(t, os.Setenv("SCW_SENDER_NAME", "Test shop"))
		require.NoError(t, es.InitService())
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go sharedSMTP.serve(conn)
			}
		}()
	})
	return sharedSMTP
}

func (s *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	say := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	say("220 fake smtp")
	var cur sentMail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			cur = sentMail{From: strings.TrimSpace(line[len("MAIL FROM:"):])}
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			rcpt := strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
			cur.To = append(cur.To, strings.ToLower(rcpt))
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				body.WriteString(l)
			}
			cur.Data = body.String()
			s.mu.Lock()
			s.mail = append(s.mail, cur)
			s.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// mailTo lists the messages sent to the address so far.
func (s *fakeSMTP) mailTo(addr string) []sentMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sentMail
	for _, m := range s.mail {
		for _, to := range m.To {
			if to == strings.ToLower(addr) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// waitMailTo waits until n messages reached the address and returns them.
func (s *fakeSMTP) waitMailTo(t *testing.T, addr string, n int) []sentMail {
	t.Helper()
	require.Eventually(t, func() bool { return len(s.mailTo(addr)) >= n }, 20*time.Second, 20*time.Millisecond,
		"expected %d e-mails to %s, got %d", n, addr, len(s.mailTo(addr)))
	return s.mailTo(addr)
}

// ---- Mollie ------------------------------------------------------------------------------------

// covMollie is api.mollie.com for the order flows: it creates payments, refunds them and cancels
// them, and keeps every call. A failure can be switched on per call kind.
type covMollie struct {
	Server *httptest.Server

	mu       sync.Mutex
	calls    []string
	seq      int
	failPay  bool
	failRef  bool
	failCanc bool
}

func newCovMollie(t *testing.T) *covMollie {
	t.Helper()
	m := &covMollie{}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Server.Close)
	return m
}

func (m *covMollie) setFail(pay, refund, cancel bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failPay, m.failRef, m.failCanc = pay, refund, cancel
}

func (m *covMollie) callsMatching(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (m *covMollie) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.calls = append(m.calls, r.Method+" "+r.URL.Path)
	failPay, failRef, failCanc := m.failPay, m.failRef, m.failCanc
	m.seq++
	seq := m.seq
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/hal+json")
	refuse := func() {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"title":"Unprocessable Entity","detail":"refused","_links":{}}`))
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/payments":
		if failPay {
			refuse()
			return
		}
		var req struct {
			Amount map[string]string `json:"amount"`
		}
		_ = json.Unmarshal(body, &req)
		id := fmt.Sprintf("tr_cov%06d", seq)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource": "payment", "id": id, "status": "open", "mode": "test", "isCancelable": true,
			"createdAt": time.Now().UTC().Format(time.RFC3339), "amount": req.Amount,
			"_links": map[string]any{"checkout": map[string]string{"href": "https://www.mollie.com/checkout/" + id, "type": "text/html"}},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refunds"):
		if failRef {
			refuse()
			return
		}
		var req struct {
			Amount map[string]string `json:"amount"`
		}
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": "refund", "id": fmt.Sprintf("re_cov%06d", seq), "amount": req.Amount, "status": "pending"})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/payments/"):
		if failCanc {
			refuse()
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/payments/")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": "payment", "id": id, "status": "canceled"})
	default:
		http.NotFound(w, r)
	}
}

// ---- push --------------------------------------------------------------------------------------

type pushReq struct {
	Token   string
	Payload map[string]any
}

// fakeAPNs is Apple's push endpoint: the device token "dead-ios" is answered as unregistered, and
// "refused-ios" as a payload the server refuses for another reason.
type fakeAPNs struct {
	Server *httptest.Server
	mu     sync.Mutex
	reqs   []pushReq
}

func newFakeAPNs(t *testing.T) *fakeAPNs {
	t.Helper()
	f := &fakeAPNs{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		token := strings.TrimPrefix(r.URL.Path, "/3/device/")
		f.mu.Lock()
		f.reqs = append(f.reqs, pushReq{Token: token, Payload: payload})
		f.mu.Unlock()
		switch token {
		case "dead-ios":
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"reason":"Unregistered"}`)
		case "refused-ios":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"reason":"PayloadEmpty"}`)
		case "broken-ios":
			// A connection that dies: the client reports a transport error.
			hj, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeAPNs) client() *apns.Client {
	return apns.NewWithEndpoint(f.Server.URL, f.Server.Client(), "be.test.app")
}

func (f *fakeAPNs) pushesTo(token string) []pushReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pushReq
	for _, r := range f.reqs {
		if r.Token == token {
			out = append(out, r)
		}
	}
	return out
}

// fakeFCM is Google's push endpoint: "dead-android" is answered as unregistered, "refused-android"
// as a permission failure.
type fakeFCM struct {
	Server *httptest.Server
	mu     sync.Mutex
	reqs   []pushReq
}

func newFakeFCM(t *testing.T) *fakeFCM {
	t.Helper()
	f := &fakeFCM{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Message map[string]any `json:"message"`
		}
		_ = json.Unmarshal(raw, &body)
		token, _ := body.Message["token"].(string)
		f.mu.Lock()
		f.reqs = append(f.reqs, pushReq{Token: token, Payload: body.Message})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch token {
		case "dead-android":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"x","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`)
		case "refused-android":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"x"}}`)
		default:
			_, _ = io.WriteString(w, `{"name":"projects/test/messages/1"}`)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeFCM) client(t *testing.T) *fcm.Client {
	t.Helper()
	c, err := fcm.NewWithEndpoint(t.Context(), "test", f.Server.URL)
	require.NoError(t, err)
	return c
}

func (f *fakeFCM) pushesTo(token string) []pushReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pushReq
	for _, r := range f.reqs {
		if r.Token == token {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeFCM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// ---- other boundaries --------------------------------------------------------------------------

type fakeZitadel struct {
	mu      sync.Mutex
	deleted []string
	failDel error
}

func (z *fakeZitadel) FetchUserInfo(context.Context, string) (string, string, string, error) {
	return "", "", "", nil
}

func (z *fakeZitadel) DeleteUser(_ context.Context, id string) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.failDel != nil {
		return z.failDel
	}
	z.deleted = append(z.deleted, id)
	return nil
}

type fakeGoogle struct {
	suggestions []addressDomain.Suggestion
	autoErr     error
	place       *addressDomain.AddressCache
	placeErr    error
}

func (g *fakeGoogle) Autocomplete(context.Context, string, string, string) ([]addressDomain.Suggestion, error) {
	return g.suggestions, g.autoErr
}

func (g *fakeGoogle) PlaceDetails(context.Context, string, string, string) (*addressDomain.AddressCache, error) {
	if g.placeErr != nil {
		return nil, g.placeErr
	}
	if g.place == nil {
		return nil, fmt.Errorf("no place")
	}
	cp := *g.place
	return &cp, nil
}

func (g *fakeGoogle) ComputeRoute(context.Context, float64, float64) (int, int, error) {
	return 1500, 420, nil
}

// fakeAgent is the WeChat assistant's agent.
type fakeAgent struct {
	conn     assistantDomain.Connection
	connErr  error
	login    assistantDomain.Login
	loginErr error
	discErr  error
}

func (a *fakeAgent) Connection(context.Context) (assistantDomain.Connection, error) {
	return a.conn, a.connErr
}

func (a *fakeAgent) StartLogin(context.Context, bool) (assistantDomain.Login, error) {
	return a.login, a.loginErr
}

func (a *fakeAgent) LoginStatus(context.Context, string) (assistantDomain.Login, error) {
	return a.login, a.loginErr
}

func (a *fakeAgent) Disconnect(context.Context) (assistantDomain.Connection, error) {
	return a.conn, a.discErr
}

type fakePosDevices struct {
	posDomain.DeviceRepository
	tokens []string
	err    error
}

func (f *fakePosDevices) FindActiveFCMTokens(context.Context) ([]string, error) {
	return f.tokens, f.err
}

// ---- the environment ---------------------------------------------------------------------------

type covOptions struct {
	// Push installs the APNs and FCM clients (pointed at the fakes).
	Push bool
	// Agent is the WeChat agent; nil leaves the assistant unconfigured.
	Agent *fakeAgent
	// PosTokens are the FCM tokens of the active POS handhelds.
	PosTokens []string
	// PosErr makes the POS token lookup fail.
	PosErr error
	// EnforceOrderingHours turns the opening-hours gate on.
	EnforceOrderingHours bool
}

type covEnv struct {
	*TestContext
	Mollie   *covMollie
	APNs     *fakeAPNs
	FCM      *fakeFCM
	Zitadel  *fakeZitadel
	Google   *fakeGoogle
	Notif    notificationApplication.NotificationService
	Mail     *fakeSMTP
	Pos      *fakePosDevices
	Limiters struct{ Coupon, Public *middleware.RateLimiter }
}

func setupCovEnv(t *testing.T, opts covOptions) *covEnv {
	t.Helper()
	t.Setenv("APP_BASE_URL", "https://shop.example.test")
	t.Setenv("MOLLIE_WEBHOOK_URL", "https://api.example.test/api/v1/payments/webhook")

	e := &covEnv{
		Mollie:  newCovMollie(t),
		APNs:    newFakeAPNs(t),
		FCM:     newFakeFCM(t),
		Zitadel: &fakeZitadel{},
		Google:  &fakeGoogle{},
		Mail:    startFakeSMTP(t),
		Pos:     &fakePosDevices{tokens: opts.PosTokens, err: opts.PosErr},
	}

	testDB := testhelpers.SetupTestDatabase(t)
	fixtures := testhelpers.SeedTestData(t, testDB.DB)
	pool := &db.DBPool{Customer: testDB.DB, Admin: testDB.DB}

	mollieCfg := mollie.NewAPITestingConfig(true)
	mollieClient, err := mollie.NewClient(nil, mollieCfg)
	require.NoError(t, err)
	base, err := url.Parse(e.Mollie.Server.URL + "/")
	require.NoError(t, err)
	mollieClient.BaseURL = base
	require.NoError(t, mollieClient.WithAuthenticationValue("test_dummy_token"))

	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)

	addressService := addressApplication.NewAddressService(addressInfrastructure.NewAddressCacheRepository(pool), e.Google, "fr")
	couponService := couponApplication.NewCouponService(couponInfrastructure.NewCouponRepository(pool))
	orderService := orderApplication.NewOrderService(orderInfrastructure.NewOrderRepository(pool), couponService)
	productService := productApplication.NewProductService(productInfrastructure.NewProductRepository(pool))
	restaurantService := restaurantApplication.NewRestaurantService(
		restaurantInfrastructure.NewRestaurantRepository(pool),
		restaurantInfrastructure.NewScheduleOverrideRepository(pool), !opts.EnforceOrderingHours)
	userService := userApplication.NewUserService(userInfrastructure.NewUserRepository(pool), e.Zitadel)
	paymentService := paymentApplication.NewPaymentService(paymentInfrastructure.NewPaymentRepository(pool), *mollieClient, orderService, userService, productService)
	e.Notif = notificationApplication.NewNotificationService(notificationInfrastructure.NewNotificationRepository(pool))

	e.Limiters.Coupon = middleware.NewRateLimiter(rate.Every(time.Hour), 3)
	e.Limiters.Public = middleware.NewRateLimiter(rate.Every(time.Hour), 2)

	r := &resolver.Resolver{
		Broker:                broker,
		AddressService:        addressService,
		CouponService:         couponService,
		NotificationService:   e.Notif,
		OrderService:          orderService,
		PaymentService:        paymentService,
		ProductService:        productService,
		RestaurantService:     restaurantService,
		UserService:           userService,
		PosService:            posApplication.NewService(posApplication.DefaultConfig([]byte("pos-test-secret")), e.Pos),
		CouponValidateLimiter: e.Limiters.Coupon,
		PublicQueryLimiter:    e.Limiters.Public,
	}
	if opts.Push {
		r.APNsClient = e.APNs.client()
		r.FCMClient = e.FCM.client(t)
	}
	if opts.Agent != nil {
		r.AssistantService = assistantApplication.NewService(opts.Agent, broker, nil, nil)
	}

	client := testhelpers.NewGraphQLTestClient(r, testhelpers.TestJWTSecret)
	t.Cleanup(client.Close)
	e.TestContext = &TestContext{DB: testDB, Resolver: r, Client: client, Fixtures: fixtures}
	return e
}

// ctxFor is a request context for a direct call of a resolver: the caller, the language and the
// data loaders, as the HTTP layer would set them up.
func (e *covEnv) ctxFor(userID string, admin bool, lang string) context.Context {
	r := e.Resolver
	ctx := t0()
	ctx = productApplication.AttachDataLoaders(ctx, r.ProductService)
	ctx = paymentApplication.AttachDataLoaders(ctx, r.PaymentService)
	ctx = orderApplication.AttachDataLoaders(ctx, r.OrderService)
	ctx = userApplication.AttachDataLoaders(ctx, r.UserService)
	if userID != "" {
		ctx = utils.SetUserID(ctx, userID)
		ctx = utils.SetIsAdmin(ctx, admin)
	}
	if lang != "" {
		ctx = utils.SetLang(ctx, lang)
	}
	return ctx
}

// t0 is a root context (a function so the helpers above read like the rest of the file).
func t0() context.Context { return context.Background() }

// customer seeds a customer whose notification e-mails are on (the e-mail address is returned too).
func (e *covEnv) customerWithMail(t *testing.T, label string) (id uuid.UUID, email, token string) {
	t.Helper()
	id, token = testhelpers.SeedCustomer(t, e.DB.DB, label)
	_, err := e.DB.DB.ExecContext(t.Context(), `UPDATE users SET notify_order_updates = true WHERE id = $1`, id)
	require.NoError(t, err)
	require.NoError(t, e.DB.DB.GetContext(t.Context(), &email, `SELECT email FROM users WHERE id = $1`, id))
	return id, email, token
}

// captureLogs routes the global zap logger into an observer for the test.
func captureLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)
	return logs
}

// waitLog waits for a log line with the message.
func waitLog(t *testing.T, logs *observer.ObservedLogs, msg string) {
	t.Helper()
	require.Eventually(t, func() bool { return logs.FilterMessage(msg).Len() > 0 }, 20*time.Second, 20*time.Millisecond, "log %q never written", msg)
}
