package application_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// fixedClients is a ClientProvider that always returns one client.
type fixedClients struct{ c *pkghubrise.Client }

func (f fixedClients) Client(context.Context, *domain.Connection) (*pkghubrise.Client, error) {
	return f.c, nil
}

// hookTransport runs a function before a request is sent, to act "while the send is in flight".
type hookTransport struct {
	next   http.RoundTripper
	before func(*http.Request)
}

func (h hookTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.before != nil {
		h.before(r)
	}
	return h.next.RoundTrip(r)
}

// env is a migrated database with the standard fixtures, the HubRise mock, a connected and enabled
// HubRise connection pointing at it, and the order push built on the real repositories.
type env struct {
	t     *testing.T
	db    *sqlx.DB
	td    *testhelpers.TestDatabase
	fx    *testhelpers.TestFixtures
	srv   *hubrisetest.Server
	clock *hubtest.FakeClock
	mod   *hubrise.Module
	push  *hubrise.Push
	opts  hubrise.PushOptions
}

type envOption func(*envConfig)

type envConfig struct {
	seed      []hubtest.SeedOption
	noConn    bool
	clients   func(*env) application.ClientProvider
	push      hubrise.PushOptions
	srvOpts   []hubrisetest.Option
	startTime time.Time
	// sharedClock makes the mock read the shop's fake clock, so the timestamps of its events and the
	// ones the shop stamps can be compared.
	sharedClock bool
}

func withSharedClock() envOption { return func(c *envConfig) { c.sharedClock = true } }

// withSeed adds options to the seeded connection.
func withSeed(o ...hubtest.SeedOption) envOption {
	return func(c *envConfig) { c.seed = append(c.seed, o...) }
}

func withoutConnection() envOption { return func(c *envConfig) { c.noConn = true } }

func withClients(f func(*env) application.ClientProvider) envOption {
	return func(c *envConfig) { c.clients = f }
}

func withPush(o hubrise.PushOptions) envOption { return func(c *envConfig) { c.push = o } }

func newEnv(t *testing.T, options ...envOption) *env {
	t.Helper()
	cfg := &envConfig{startTime: time.Now().Truncate(time.Second)}
	for _, o := range options {
		o(cfg)
	}
	td := testhelpers.SetupTestDatabase(t)
	e := &env{t: t, db: td.DB, td: td, fx: testhelpers.SeedTestData(t, td.DB), clock: hubtest.NewFakeClock(cfg.startTime)}
	if cfg.sharedClock {
		cfg.srvOpts = append(cfg.srvOpts, hubrisetest.WithClock(e.clock.Now))
	}
	e.srv = hubrisetest.New(t, cfg.srvOpts...)

	hcfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: domain.Redacted(hubrisetest.ClientSecret),
		TokenKey: hubtest.TestTokenKey, APIBaseURL: e.srv.APIBaseURL(), OAuthBaseURL: e.srv.OAuthBaseURL(),
	}
	var err error
	e.mod, err = hubrise.NewModule(hcfg, td.DB, e.clock)
	require.NoError(t, err)

	if !cfg.noConn {
		seed := append([]hubtest.SeedOption{
			hubtest.WithToken(hubrisetest.Token),
			hubtest.WithPushOrdersSince(e.clock.Now().Add(-time.Minute)),
			// No customer list unless a test asks for one: orders carry the guest object, as before.
			hubtest.WithCustomerList("", ""),
		}, cfg.seed...)
		hubtest.SeedConnection(t, td.DB, seed...)
	}
	e.opts = cfg.push
	if cfg.clients != nil {
		e.opts.Clients = cfg.clients(e)
	} else {
		e.opts.Clients = fixedClients{c: e.srv.NewClient(t)}
	}
	e.push = e.mod.NewPush(e.opts)
	return e
}

// rebuild rebuilds the push (a second set of workers on the same database, or new options).
func (e *env) rebuild(o hubrise.PushOptions) *hubrise.Push {
	if o.Clients == nil {
		o.Clients = e.opts.Clients
	}
	return e.mod.NewPush(o)
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	_, err := e.db.ExecContext(e.t.Context(), q, args...)
	require.NoError(e.t, err, q)
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.db.GetContext(e.t.Context(), &n, q, args...), q)
	return n
}

// orderSpec describes an order to insert. The zero value is a cash pickup order of one Salmon Sushi
// at 12.50 EUR, pending, placed from the web.
type orderSpec struct {
	status      string
	orderType   string
	online      bool
	paid        bool
	test        bool
	channel     *string
	total       string // default: the lines plus the fees, rounded
	deliveryFee string
	takeaway    string
	coupon      string
	couponCode  string
	note        string
	cash        string
	lines       []lineSpec
	createdAgo  time.Duration
	preferred   *time.Time
	extras      string
}

type lineSpec struct {
	product    uuid.UUID
	qty        int
	total      string
	vat        string
	selections []selSpec
}

type selSpec struct {
	group, choice uuid.UUID
	qty           int
}

func (e *env) order(spec orderSpec) uuid.UUID {
	e.t.Helper()
	if spec.status == "" {
		spec.status = "PENDING"
	}
	if spec.orderType == "" {
		spec.orderType = "PICKUP"
	}
	if len(spec.lines) == 0 {
		spec.lines = []lineSpec{{product: e.fx.SalmonSushi.ID, qty: 1, total: "12.50", vat: "6"}}
	}
	if spec.total == "" {
		spec.total = "12.50"
	}
	nz := func(s string) string {
		if s == "" {
			return "0"
		}
		return s
	}
	var extras any
	if spec.extras != "" {
		extras = spec.extras
	}
	fee := "0"
	if spec.online {
		fee = "0.32"
	}
	var id uuid.UUID
	require.NoError(e.t, e.db.GetContext(e.t.Context(), &id, `
		INSERT INTO orders (user_id, order_status, order_type, is_online_payment, is_test, channel, total_price,
			delivery_fee, takeaway_discount, coupon_discount, coupon_code, order_note, cash_payment_amount,
			transaction_fee, preferred_ready_time, order_extra, language,
			street_name, house_number, box_number, postcode, municipality_name, address_lat, address_lng, address_extra)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8::text, '0')::numeric, $9, $10, NULLIF($11::text, ''), NULLIF($12::text, ''),
			NULLIF($13::text, '')::numeric, $14, $15, $16::jsonb, 'fr',
			'Rue du Test', '12', '3', '4000', 'Liège', 50.6326, 5.5797, 'Second floor')
		RETURNING id`,
		e.fx.RegularUser.ID, spec.status, spec.orderType, spec.online, spec.test, spec.channel, spec.total,
		nz(spec.deliveryFee), nz(spec.takeaway), nz(spec.coupon), spec.couponCode, spec.note, spec.cash,
		fee, spec.preferred, extras))
	if spec.createdAgo > 0 {
		e.exec(`UPDATE orders SET created_at = $2 WHERE id = $1`, id, e.clock.Now().Add(-spec.createdAgo))
	}
	for _, l := range spec.lines {
		vat := l.vat
		if vat == "" {
			vat = "6"
		}
		var lineID uuid.UUID
		require.NoError(e.t, e.db.GetContext(e.t.Context(), &lineID, `
			INSERT INTO order_product (order_id, product_id, unit_price, quantity, total_price, vat_rate_applied)
			VALUES ($1, $2, $3::numeric / $4::int, $4::int, $3::numeric, $5::numeric) RETURNING id`, id, l.product, l.total, l.qty, vat))
		for _, s := range l.selections {
			e.exec(`INSERT INTO order_product_choices (order_product_id, product_choice_group_id, product_choice_id, quantity)
				VALUES ($1, $2, $3, $4)`, lineID, s.group, s.choice, s.qty)
		}
	}
	if spec.online {
		status := "open"
		if spec.paid {
			status = "paid"
		}
		e.exec(`INSERT INTO mollie_payments (mollie_payment_id, status, order_id, amount) VALUES ($1, $2, $3, $4)`,
			"tr_"+strings.ReplaceAll(uuid.NewString(), "-", "")[:10], status, id, spec.total)
	}
	return id
}

// setStatus changes the local status the way the order service does: the row, then the observer.
func (e *env) setStatus(id uuid.UUID, status string) {
	e.t.Helper()
	e.exec(`UPDATE orders SET order_status = $2 WHERE id = $1`, id, status)
	var hist uuid.UUID
	require.NoError(e.t, e.db.GetContext(e.t.Context(), &hist,
		`INSERT INTO order_status_history (order_id, status) VALUES ($1, $2) RETURNING id`, id, status))
	require.NoError(e.t, e.push.Pusher.OrderStatusChanged(e.t.Context(), id, status, hist))
}

// linkProduct gives a product (and its choices) HubRise refs.
func (e *env) linkProduct(productID uuid.UUID, ref string) {
	e.t.Helper()
	l := &domain.CatalogLink{
		EntityType: domain.EntityProduct, HubriseKey: ref, HubriseRef: ref, Origin: domain.OriginManual,
	}
	l.SetLocalID(productID)
	require.NoError(e.t, e.mod.CatalogLinks.Upsert(e.t.Context(), l))
}

func (e *env) linkChoice(choiceID uuid.UUID, key, ref string) {
	e.t.Helper()
	l := &domain.CatalogLink{EntityType: domain.EntityChoice, HubriseKey: key, HubriseRef: ref, Origin: domain.OriginManual}
	l.SetLocalID(choiceID)
	require.NoError(e.t, e.mod.CatalogLinks.Upsert(e.t.Context(), l))
}

// link returns the order link, failing the test when there is none.
func (e *env) link(id uuid.UUID) *domain.OrderLink {
	e.t.Helper()
	l, err := e.mod.Orders.Get(e.t.Context(), id)
	require.NoError(e.t, err)
	return l
}

func (e *env) outboxRows(id uuid.UUID, kind domain.OutboxKind) []domain.OutboxItem {
	e.t.Helper()
	var items []domain.OutboxItem
	require.NoError(e.t, e.db.SelectContext(e.t.Context(), &items, `
		SELECT id, kind, order_id, dedupe_key, payload, state, attempts, leased_until, next_attempt_at, last_error,
		       created_at, sent_at, parked_at
		FROM hubrise_outbox WHERE order_id = $1 AND kind = $2 ORDER BY id`, id, kind))
	return items
}

func (e *env) drain() int {
	e.t.Helper()
	n, err := e.push.Worker.DrainOnce(e.t.Context())
	require.NoError(e.t, err)
	return n
}

// drainAll drains until a claim comes back empty.
func (e *env) drainAll() {
	e.t.Helper()
	for range 50 {
		if e.drain() == 0 {
			return
		}
	}
	e.t.Fatal("the outbox never emptied")
}

// ready queues the push of an order.
func (e *env) ready(id uuid.UUID) {
	e.t.Helper()
	require.NoError(e.t, e.push.Pusher.OrderReady(e.t.Context(), id))
}

func (e *env) posts() []hubrisetest.Request {
	return e.srv.RequestsTo(http.MethodPost, "/v1/location/orders")
}

func (e *env) patches() []hubrisetest.Request {
	return e.srv.RequestsTo(http.MethodPatch, "/v1/location/orders")
}

// remote returns the HubRise order created for a local order, found by private_ref as our client sees it.
func (e *env) remote(id uuid.UUID) pkghubrise.Order {
	e.t.Helper()
	o, ok := e.srv.OrderByPrivateRef(id.String())
	require.True(e.t, ok, "HubRise has no order for %s", id)
	return o
}

// scheduleFunc says whether the ordering hours covered an instant.
type scheduleFunc func(at time.Time) bool

func (f scheduleFunc) OrderingOpenAt(_ context.Context, at time.Time) (bool, error) {
	return f(at), nil
}

type alertRecorder struct{ subjects []string }

func (a *alertRecorder) Alert(_ context.Context, subject, _ string) error {
	a.subjects = append(a.subjects, subject)
	return nil
}

// buildForTest builds the HubRise order of a local order the way the push does, without any ref.
func buildForTest(t *testing.T, e *env, id uuid.UUID) (*pkghubrise.Order, bool) {
	t.Helper()
	snap, err := e.mod.Source.Snapshot(t.Context(), id)
	require.NoError(t, err)
	return application.BuildOrder(application.BuildCreatePayload(snap), application.SendEnv{
		OrderID: id, Now: e.clock.Now(), Status: pkghubrise.StatusNew, DeliveryRef: "DELIVERY", PaymentFeeRef: "PAYMENT_FEE",
		SkuRef: func(uuid.UUID) string { return "" }, OptionRef: func(uuid.UUID) string { return "" },
	})
}

type clientProvider = application.ClientProvider

type brokenClients struct{}

func (brokenClients) Client(context.Context, *domain.Connection) (*pkghubrise.Client, error) {
	return nil, domain.ErrNotConnected
}

func hubriseWorker(callTimeout time.Duration) hubrise.PushOptions {
	return hubrise.PushOptions{Worker: application.WorkerConfig{CallTimeout: callTimeout}}
}

func ptr[T any](v T) *T { return &v }

// hideRemote deletes the HubRise order of a local order, so the shop meets a 404 for it.
func hideRemote(t *testing.T, e *env, id uuid.UUID) {
	t.Helper()
	o, ok := e.srv.OrderByPrivateRef(id.String())
	require.True(t, ok)
	require.True(t, e.srv.DeleteOrder(o.ID))
}

// rawRow inserts a hand-made outbox row that is due now (by the fake clock) and returns nothing.
func (e *env) rawRow(kind string, orderID *uuid.UUID, payload string, state ...string) {
	e.t.Helper()
	st := "pending"
	if len(state) > 0 {
		st = state[0]
	}
	e.exec(`INSERT INTO hubrise_outbox (kind, order_id, payload, state, next_attempt_at, created_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $5)`, kind, orderID, payload, st, e.clock.Now())
}

func infraConnections(db *sqlx.DB) domain.ConnectionRepository {
	return infrastructure.NewConnectionRepository(db)
}
func infraSource(db *sqlx.DB) domain.OrderSource      { return infrastructure.NewOrderSource(db) }
func infraEvents(db *sqlx.DB) domain.EventRepository  { return infrastructure.NewEventRepository(db) }
func infraOutbox(db *sqlx.DB) domain.OutboxRepository { return infrastructure.NewOutboxRepository(db) }
func infraForeign(db *sqlx.DB) domain.ForeignOrderRepository {
	return infrastructure.NewForeignOrderRepository(db)
}
func infraStates(db *sqlx.DB) domain.OAuthStateRepository {
	return infrastructure.NewOAuthStateRepository(db)
}
func infraOrders(db *sqlx.DB) domain.OrderLinkRepository {
	return infrastructure.NewOrderLinkRepository(db)
}
