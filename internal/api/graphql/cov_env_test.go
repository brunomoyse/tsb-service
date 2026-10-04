package graphql_test

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"tsb-service/pkg/apns/apnstest"
	"tsb-service/pkg/fcm/fcmtest"

	"tsb-service/pkg/email/smtptest"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
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
	"tsb-service/pkg/fcm"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/utils"
)

// The environment of the resolver coverage tests: the real services over a real Postgres (like
// setupTestContext) plus every boundary the resolvers reach beyond the database, replaced by a
// fake that records what it was asked: Mollie, APNs, FCM, the Zitadel identity API, Google Places,
// the WeChat agent and an SMTP server for the transactional e-mails.

// ---- SMTP --------------------------------------------------------------------------------------

// sharedSMTP is the one fake SMTP server of the test binary. The e-mail backend is process-wide
// state, so it is installed once, unconditionally, by TestMain (main_test.go): no test depends on
// which test configured it first. Tests use addresses of their own (newPushCustomer, ...).
var sharedSMTP *smtptest.Server

// startFakeSMTP is the server every covEnv reads its mail from.
func startFakeSMTP(t *testing.T) *smtptest.Server {
	t.Helper()
	require.NotNil(t, sharedSMTP, "TestMain installs the fake SMTP server")
	return sharedSMTP
}

// ---- Mollie ------------------------------------------------------------------------------------

// The Mollie fake is testhelpers.MollieStub (payments, refunds and cancellations, with recorded
// calls and bodies and failures that can be switched on per kind of call).

// ---- push --------------------------------------------------------------------------------------

// The push fakes are pkg/apns/apnstest and pkg/fcm/fcmtest servers that answer by device token: a
// token starting with "dead-" is unregistered, "refused-" is refused for another reason and (APNs
// only) "broken-" is a dropped connection.

func apnsClient(f *apnstest.Server) *apns.Client {
	return apns.NewWithEndpoint(f.URL, f.Client(), "be.test.app")
}

func fcmClient(t *testing.T, f *fcmtest.Server) *fcm.Client {
	t.Helper()
	c, err := fcm.NewWithEndpoint(t.Context(), "test", f.URL)
	require.NoError(t, err)
	return c
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

// FindByID answers any device id as an enrolled one.
func (f *fakePosDevices) FindByID(_ context.Context, id uuid.UUID) (*posDomain.Device, error) {
	return &posDomain.Device{ID: id}, nil
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
	opts covOptions
	*TestContext
	Mollie   *testhelpers.MollieStub
	APNs     *apnstest.Server
	FCM      *fcmtest.Server
	Zitadel  *fakeZitadel
	Google   *fakeGoogle
	Notif    notificationApplication.NotificationService
	Mail     *smtptest.Server
	Pos      *fakePosDevices
	Limiters struct{ Coupon, Public *middleware.RateLimiter }
}

func setupCovEnv(t *testing.T, opts covOptions) *covEnv {
	t.Helper()
	t.Setenv("APP_BASE_URL", "https://shop.example.test")
	t.Setenv("MOLLIE_WEBHOOK_URL", "https://api.example.test/api/v1/payments/webhook")

	e := &covEnv{
		opts:    opts,
		Mollie:  testhelpers.NewMollieStub(t),
		APNs:    apnstest.ByToken(t),
		FCM:     fcmtest.ByToken(t),
		Zitadel: &fakeZitadel{},
		Google:  &fakeGoogle{},
		Mail:    startFakeSMTP(t),
		Pos:     &fakePosDevices{tokens: opts.PosTokens, err: opts.PosErr},
	}
	e.Limiters.Coupon = middleware.NewRateLimiter(rate.Every(time.Hour), 3)
	e.Limiters.Public = middleware.NewRateLimiter(rate.Every(time.Hour), 2)

	testDB := testhelpers.SetupTestDatabase(t)
	fixtures := testhelpers.SeedTestData(t, testDB.DB)

	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	r := e.wire(t, &db.DBPool{Customer: testDB.DB, Admin: testDB.DB}, broker)
	e.Notif = r.NotificationService

	client := testhelpers.NewGraphQLTestClient(r, testhelpers.TestJWTSecret)
	t.Cleanup(client.Close)
	e.TestContext = &TestContext{DB: testDB, Resolver: r, Client: client, Fixtures: fixtures}
	return e
}

// wire builds a resolver with the real services over the pool and the environment's fakes.
func (e *covEnv) wire(t *testing.T, pool *db.DBPool, broker *pubsub.Broker) *resolver.Resolver {
	t.Helper()
	mollieClient, err := mollie.NewClient(nil, mollie.NewAPITestingConfig(true))
	require.NoError(t, err)
	base, err := url.Parse(e.Mollie.Server.URL + "/")
	require.NoError(t, err)
	mollieClient.BaseURL = base
	require.NoError(t, mollieClient.WithAuthenticationValue("test_dummy_token"))

	addressService := addressApplication.NewAddressService(addressInfrastructure.NewAddressCacheRepository(pool), e.Google, "fr")
	couponService := couponApplication.NewCouponService(couponInfrastructure.NewCouponRepository(pool))
	orderService := orderApplication.NewOrderService(orderInfrastructure.NewOrderRepository(pool), couponService)
	productService := productApplication.NewProductService(productInfrastructure.NewProductRepository(pool))
	restaurantService := restaurantApplication.NewRestaurantService(
		restaurantInfrastructure.NewRestaurantRepository(pool),
		restaurantInfrastructure.NewScheduleOverrideRepository(pool), !e.opts.EnforceOrderingHours)
	userService := userApplication.NewUserService(userInfrastructure.NewUserRepository(pool), e.Zitadel)
	paymentService := paymentApplication.NewPaymentService(paymentInfrastructure.NewPaymentRepository(pool), *mollieClient, orderService, userService, productService)

	r := &resolver.Resolver{
		Broker:                broker,
		AddressService:        addressService,
		CouponService:         couponService,
		NotificationService:   notificationApplication.NewNotificationService(notificationInfrastructure.NewNotificationRepository(pool)),
		OrderService:          orderService,
		PaymentService:        paymentService,
		ProductService:        productService,
		RestaurantService:     restaurantService,
		UserService:           userService,
		PosService:            posApplication.NewService(posApplication.DefaultConfig([]byte("pos-test-secret")), e.Pos),
		CouponValidateLimiter: e.Limiters.Coupon,
		PublicQueryLimiter:    e.Limiters.Public,
	}
	if e.opts.Push {
		r.APNsClient = apnsClient(e.APNs)
		r.FCMClient = fcmClient(t, e.FCM)
	}
	if e.opts.Agent != nil {
		r.AssistantService = assistantApplication.NewService(e.opts.Agent, broker, nil, nil)
	}
	return r
}

// brokenResolver is a resolver whose database connection is already closed: every query fails,
// which reaches the "the store is down" branch of whatever it calls.
func (e *covEnv) brokenResolver(t *testing.T) *resolver.Resolver {
	t.Helper()
	conn := e.DB.ClosedConnection(t)
	return e.wire(t, &db.DBPool{Customer: conn, Admin: conn}, pubsub.NewBroker())
}

// ctxFor is a request context for a direct call of a resolver: the caller, the language and the
// data loaders, as the HTTP layer would set them up.
func (e *covEnv) ctxFor(userID string, admin bool, lang string) context.Context {
	return loadersFor(e.Resolver, userID, admin, lang)
}

// loadersFor is ctxFor for any resolver (the loaders read through that resolver's services).
func loadersFor(r *resolver.Resolver, userID string, admin bool, lang string) context.Context {
	ctx := context.Background()
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

// ctxForCancel is ctxFor on top of a cancellable parent (subscriptions end with their context).
func (e *covEnv) ctxForCancel(parent context.Context, userID string, admin bool, lang string) context.Context {
	ctx := e.ctxFor(userID, admin, lang)
	return &mergedCtx{Context: parent, values: ctx}
}

// mergedCtx takes cancellation from one context and the values from another.
type mergedCtx struct {
	context.Context
	values context.Context
}

func (m *mergedCtx) Value(key any) any {
	if v := m.values.Value(key); v != nil {
		return v
	}
	return m.Context.Value(key)
}

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
