package interfaces

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"tsb-service/pkg/email/scaleway/scalewaytest"
	"tsb-service/pkg/email/smtptest"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	couponApplication "tsb-service/internal/modules/coupon/application"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/pubsub"
)

// ---------------------------------------------------------------------------
// Fakes: Mollie HTTP API, SMTP sink, repositories and collaborators. The real
// payment service, order service and webhook handler run on top of them.
// ---------------------------------------------------------------------------

type fakeMollie struct {
	mu         sync.Mutex
	status     string
	getCode    int
	getCalls   int
	refunds    int
	cancels    int
	refundCode int
	// noRemaining: the payment is reported without amountRemaining, as Mollie does for a payment it cannot refund.
	noRemaining bool
}

func (f *fakeMollie) set(status string) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

func (f *fakeMollie) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/hal+json")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/payments/"):
		f.getCalls++
		if f.getCode != 0 {
			w.WriteHeader(f.getCode)
			_, _ = w.Write([]byte(`{"status":500,"title":"err","detail":"err"}`))
			return
		}
		remaining := `,"amountRemaining":{"value":"20.00","currency":"EUR"}`
		if f.noRemaining {
			remaining = ""
		}
		_, _ = fmt.Fprintf(w, `{"resource":"payment","id":%q,"status":%q,"amount":{"value":"20.00","currency":"EUR"}%s}`,
			strings.TrimPrefix(r.URL.Path, "/v2/payments/"), f.status, remaining)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refunds"):
		if f.refundCode != 0 {
			w.WriteHeader(f.refundCode)
			return
		}
		f.refunds++
		var req struct {
			Amount struct{ Value string } `json:"amount"`
		}
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"resource":"refund","id":"re_1","amount":{"currency":"EUR","value":%q},"status":"pending"}`, req.Amount.Value)
	case r.Method == http.MethodDelete:
		f.cancels++
		_, _ = w.Write([]byte(`{"resource":"payment","id":"tr_1","status":"canceled"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeMollie) counts() (gets, refunds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls, f.refunds
}

// startSMTPSink starts a fake SMTP server and routes the e-mail backend to it for this test (the
// previous backend is restored when the test ends).
func startSMTPSink(t *testing.T) *smtptest.Server {
	t.Helper()
	s := smtptest.Start(t)
	scalewaytest.Use(t, s)
	return s
}

// memPayments is an in-memory PaymentRepository with a per-payment lock and
// failure injection.
type memPayments struct {
	mu          sync.Mutex
	payments    map[string]*paymentDomain.MolliePayment
	locks       map[string]*sync.Mutex
	lookupErr   error
	refreshErrs int // number of RefreshStatus calls that fail before succeeding
	lockErr     error
	refreshed   int
}

func (r *memPayments) lockFor(id string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	if r.locks[id] == nil {
		r.locks[id] = &sync.Mutex{}
	}
	return r.locks[id]
}

func (r *memPayments) Save(context.Context, *paymentDomain.MolliePayment) error { return nil }

func (r *memPayments) MarkAsRefund(_ context.Context, id string, amount decimal.Decimal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payments[id].AmountRefunded = amount
	return nil
}

func (r *memPayments) RefreshStatus(_ context.Context, id string, u *paymentDomain.PaymentStatusUpdate) (*uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refreshErrs > 0 {
		r.refreshErrs--
		return nil, errors.New("db down")
	}
	p, ok := r.payments[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	p.Status = u.Status
	r.refreshed++
	return &p.OrderID, nil
}

func (r *memPayments) UpdateStatusByOrderID(context.Context, uuid.UUID, paymentDomain.PaymentStatus) (*paymentDomain.MolliePayment, error) {
	return nil, errors.New("unused")
}

func (r *memPayments) FindByOrderID(_ context.Context, orderID uuid.UUID) (*paymentDomain.MolliePayment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.payments {
		if p.OrderID == orderID {
			cp := *p
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("find by order: %w", sql.ErrNoRows)
}

func (r *memPayments) FindByExternalID(_ context.Context, id string) (*paymentDomain.MolliePayment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	p, ok := r.payments[id]
	if !ok {
		return nil, fmt.Errorf("find by id: %w", sql.ErrNoRows)
	}
	cp := *p
	return &cp, nil
}

func (r *memPayments) FindByOrderIDs(context.Context, []string) (map[string][]*paymentDomain.MolliePayment, error) {
	return nil, nil
}

func (r *memPayments) WithPaymentLock(ctx context.Context, id string, fn func(context.Context) error) error {
	if r.lockErr != nil {
		return r.lockErr
	}
	m := r.lockFor(id)
	m.Lock()
	defer m.Unlock()
	return fn(ctx)
}

func (r *memPayments) status(id string) paymentDomain.PaymentStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.payments[id].Status
}

// memOrders is an OrderRepository holding one order, with a status history.
type memOrders struct {
	orderDomain.OrderRepository
	mu       sync.Mutex
	order    *orderDomain.Order
	products []orderDomain.OrderProductRaw
	history  []orderDomain.OrderStatus
	updates  int
	// failFind makes FindByID fail after it has been called findOK times.
	failFind bool
	findOK   int
	finds    int
}

func (o *memOrders) FindByID(_ context.Context, _ uuid.UUID) (*orderDomain.Order, *[]orderDomain.OrderProductRaw, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finds++
	if o.failFind && o.finds > o.findOK {
		return nil, nil, errors.New("db down")
	}
	cp := *o.order
	prods := append([]orderDomain.OrderProductRaw(nil), o.products...)
	return &cp, &prods, nil
}

func (o *memOrders) Update(_ context.Context, ord *orderDomain.Order) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.updates++
	cp := *ord
	o.order = &cp
	return nil
}

func (o *memOrders) InsertStatusHistory(_ context.Context, _ uuid.UUID, s orderDomain.OrderStatus) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.history = append(o.history, s)
	return nil
}

func (o *memOrders) snapshot() (orderDomain.OrderStatus, []orderDomain.OrderStatus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.order.OrderStatus, append([]orderDomain.OrderStatus(nil), o.history...)
}

type memCoupons struct {
	couponApplication.CouponService
	mu         sync.Mutex
	id         uuid.UUID
	decrements int
}

func (c *memCoupons) GetCouponByCode(context.Context, string) (*couponDomain.Coupon, error) {
	return &couponDomain.Coupon{ID: c.id}, nil
}

func (c *memCoupons) DecrementUsageAtomic(context.Context, uuid.UUID, uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decrements++
	return nil
}

func (c *memCoupons) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decrements
}

type memProducts struct {
	productApplication.ProductService
	err error
	mu  sync.Mutex
}

func (p *memProducts) GetProductNamesForInvoice(_ context.Context, ids []string) ([]*productDomain.ProductOrderDetails, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	code := "A1"
	var out []*productDomain.ProductOrderDetails
	for _, id := range ids {
		out = append(out, &productDomain.ProductOrderDetails{ID: uuid.MustParse(id), Code: &code, CategoryName: "Sushi", Name: "Maki", VatCategory: productDomain.VatCategoryFood})
	}
	return out, nil
}

func (p *memProducts) setErr(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

type memUsers struct {
	userApplication.UserService
	user *userDomain.User
}

func (u *memUsers) GetUserByID(context.Context, string) (*userDomain.User, error) { return u.user, nil }

type countingNotifier struct {
	mu     sync.Mutex
	pushes int
}

func (n *countingNotifier) SendNewOrderPush(*orderDomain.Order) {
	n.mu.Lock()
	n.pushes++
	n.mu.Unlock()
}

func (n *countingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.pushes
}

// topicRecorder subscribes to the topics the handler publishes on.
type topicRecorder struct {
	mu     sync.Mutex
	counts map[string]int
}

func recordTopics(broker *pubsub.Broker, orderID uuid.UUID) *topicRecorder {
	rec := &topicRecorder{counts: map[string]int{}}
	for _, topic := range []string{"orderCreated", "orderUpdated", "orderUpdated:" + orderID.String()} {
		ch := broker.Subscribe(topic)
		go func() {
			for range ch {
				rec.mu.Lock()
				rec.counts[topic]++
				rec.mu.Unlock()
			}
		}()
	}
	return rec
}

// get waits briefly for asynchronous delivery before reading.
func (r *topicRecorder) get(topic string) int {
	time.Sleep(20 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[topic]
}

type webhookEnv struct {
	t        *testing.T
	router   *gin.Engine
	mollie   *fakeMollie
	payments *memPayments
	orders   *memOrders
	coupons  *memCoupons
	products *memProducts
	users    *memUsers
	notifier *countingNotifier
	topics   *topicRecorder
	order    *orderDomain.Order
	smtp     *smtptest.Server
}

func newWebhookEnv(t *testing.T, orderStatus orderDomain.OrderStatus, paymentStatus paymentDomain.PaymentStatus) *webhookEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	couponCode := "TOKYO10"
	order := &orderDomain.Order{
		ID:              uuid.New(),
		UserID:          uuid.New(),
		OrderStatus:     orderStatus,
		OrderType:       orderDomain.OrderTypePickUp,
		IsOnlinePayment: true,
		Language:        "fr",
		TotalPrice:      decimal.RequireFromString("20.00"),
		CouponCode:      &couponCode,
	}
	fm := &fakeMollie{status: string(paymentStatus)}
	srv := httptest.NewServer(http.HandlerFunc(fm.handler))
	t.Cleanup(srv.Close)
	client, err := mollie.NewClient(srv.Client(), mollie.NewAPITestingConfig(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WithAuthenticationValue("test_dummydummydummydummydummydummy"); err != nil {
		t.Fatal(err)
	}
	client.BaseURL, _ = url.Parse(srv.URL + "/")

	payments := &memPayments{payments: map[string]*paymentDomain.MolliePayment{
		"tr_1": {MolliePaymentID: "tr_1", OrderID: order.ID, Status: paymentStatus, Amount: decimal.RequireFromString("20.00"), IsCancelable: true},
	}}
	orders := &memOrders{
		order: order,
		products: []orderDomain.OrderProductRaw{{
			ProductID: uuid.New(), Quantity: 2,
			UnitPrice: decimal.RequireFromString("10.00"), TotalPrice: decimal.RequireFromString("20.00"),
		}},
	}
	coupons := &memCoupons{id: uuid.New()}
	products := &memProducts{}
	users := &memUsers{user: &userDomain.User{ID: order.UserID, Email: "c@example.test", FirstName: "C", LastName: "D", NotifyOrderUpdates: true}}

	orderSvc := orderApplication.NewOrderService(orders, coupons)
	paySvc := paymentApplication.NewPaymentService(payments, *client, orderSvc, users, products)

	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	notifier := &countingNotifier{}
	h := NewPaymentHandler(paySvc, broker, notifier)

	router := gin.New()
	router.POST("/webhook", h.UpdatePaymentStatusHandler)

	return &webhookEnv{
		t: t, router: router, mollie: fm, payments: payments, orders: orders, coupons: coupons,
		products: products, users: users, notifier: notifier, topics: recordTopics(broker, order.ID),
		order: order, smtp: startSMTPSink(t),
	}
}

func (e *webhookEnv) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func (e *webhookEnv) deliver(id string) *httptest.ResponseRecorder {
	return e.post("id=" + id)
}

func (e *webhookEnv) expect(w *httptest.ResponseRecorder, code int, msg string) {
	e.t.Helper()
	if w.Code != code {
		e.t.Fatalf("status = %d (%s), want %d", w.Code, w.Body.String(), code)
	}
	if msg != "" && !strings.Contains(w.Body.String(), msg) {
		e.t.Fatalf("body = %s, want %q", w.Body.String(), msg)
	}
}

// ---------------------------------------------------------------------------
// Request validation
// ---------------------------------------------------------------------------

func TestWebhook_RequestValidation(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)

	t.Run("missing id is a 400", func(t *testing.T) {
		e.expect(e.post(""), http.StatusBadRequest, "")
		e.expect(e.post("other=1"), http.StatusBadRequest, "")
		e.expect(e.post("id="), http.StatusBadRequest, "")
	})
	t.Run("malformed body is a 400", func(t *testing.T) {
		e.expect(e.post("%zz=%"), http.StatusBadRequest, "")
	})
	t.Run("JSON body without a bindable id is a 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"id":123}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("id without tr_ prefix is acked and ignored without calling Mollie", func(t *testing.T) {
		e.expect(e.deliver("ord_123"), http.StatusOK, "ignored")
		e.expect(e.deliver("TR_1"), http.StatusOK, "ignored")
		if gets, _ := e.mollie.counts(); gets != 0 {
			t.Fatalf("Mollie fetched %d times", gets)
		}
	})
	t.Run("id in the query string is accepted too (Mollie form field)", func(t *testing.T) {
		e.mollie.set("open")
		req := httptest.NewRequest(http.MethodPost, "/webhook?id=tr_1", nil)
		w := httptest.NewRecorder()
		e.router.ServeHTTP(w, req)
		e.expect(w, http.StatusOK, "already processed")
	})
}

// ---------------------------------------------------------------------------
// Lookup / Mollie failures: must be retryable, never dropped
// ---------------------------------------------------------------------------

func TestWebhook_UnknownPaymentIsAckedWithoutSideEffects(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.expect(e.deliver("tr_unknown"), http.StatusOK, "unknown payment")
	if gets, _ := e.mollie.counts(); gets != 0 {
		t.Fatal("must not call Mollie for an unknown payment")
	}
	if e.smtp.Count() != 0 || e.notifier.count() != 0 || e.payments.refreshed != 0 {
		t.Fatal("side effects for an unknown payment")
	}
}

func TestWebhook_TransientFailuresReturn500SoMollieRetries(t *testing.T) {
	t.Run("DB lookup error", func(t *testing.T) {
		e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
		e.payments.lookupErr = errors.New("connection refused")
		e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
	})
	t.Run("Mollie fetch error", func(t *testing.T) {
		e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
		e.mollie.getCode = http.StatusInternalServerError
		e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
		if st := e.payments.status("tr_1"); st != paymentDomain.PaymentStatusOpen {
			t.Fatalf("status changed to %s", st)
		}
	})
	t.Run("Mollie 404 / 429 are also retryable", func(t *testing.T) {
		for _, code := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusBadGateway} {
			e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
			e.mollie.getCode = code
			e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "")
		}
	})
	t.Run("advisory lock failure", func(t *testing.T) {
		e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
		e.payments.lockErr = errors.New("pool exhausted")
		e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
	})
	t.Run("recovers on the next delivery", func(t *testing.T) {
		e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
		e.mollie.set("paid")
		e.mollie.getCode = http.StatusInternalServerError
		e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "")
		e.mollie.getCode = 0
		e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
		if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid {
			t.Fatal("paid not persisted after the retry")
		}
		if e.smtp.Count() != 1 {
			t.Fatalf("emails = %d", e.smtp.Count())
		}
	})
}

// ---------------------------------------------------------------------------
// Every Mollie status
// ---------------------------------------------------------------------------

func TestWebhook_NonTerminalStatusesOnlyPersist(t *testing.T) {
	// Stored status differs from the delivered one so the transition is real.
	cases := []struct{ stored, mollie paymentDomain.PaymentStatus }{
		{paymentDomain.PaymentStatusPending, paymentDomain.PaymentStatusOpen},
		{paymentDomain.PaymentStatusOpen, paymentDomain.PaymentStatusPending},
		{paymentDomain.PaymentStatusOpen, paymentDomain.PaymentStatusAuthorized},
	}
	for _, tc := range cases {
		t.Run(string(tc.stored)+" to "+string(tc.mollie), func(t *testing.T) {
			e := newWebhookEnv(t, orderDomain.OrderStatusPending, tc.stored)
			e.mollie.set(string(tc.mollie))
			e.expect(e.deliver("tr_1"), http.StatusOK, "processed")

			if got := e.payments.status("tr_1"); got != tc.mollie {
				t.Fatalf("stored = %s", got)
			}
			st, hist := e.orders.snapshot()
			if st != orderDomain.OrderStatusPending || len(hist) != 0 || e.orders.updates != 0 {
				t.Fatalf("order touched: %s %v", st, hist)
			}
			if e.smtp.Count() != 0 || e.notifier.count() != 0 || e.coupons.count() != 0 {
				t.Fatal("side effects on a non-terminal status")
			}
			if e.topics.get("orderCreated")+e.topics.get("orderUpdated") != 0 {
				t.Fatal("published on a non-terminal status")
			}
		})
	}
}

func TestWebhook_Paid(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")

	if got := e.payments.status("tr_1"); got != paymentDomain.PaymentStatusPaid {
		t.Fatalf("stored = %s", got)
	}
	st, hist := e.orders.snapshot()
	if st != orderDomain.OrderStatusPending || len(hist) != 0 {
		t.Fatalf("paid must leave the order PENDING for staff to confirm: %s %v", st, hist)
	}
	if e.smtp.Count() != 1 {
		t.Fatalf("confirmation emails = %d, want 1", e.smtp.Count())
	}
	if e.notifier.count() != 1 {
		t.Fatalf("pushes = %d, want 1", e.notifier.count())
	}
	for _, topic := range []string{"orderCreated", "orderUpdated", "orderUpdated:" + e.order.ID.String()} {
		if n := e.topics.get(topic); n != 1 {
			t.Errorf("%s published %d times, want 1", topic, n)
		}
	}
	if e.coupons.count() != 0 {
		t.Fatal("coupon must stay consumed after a successful payment")
	}
}

func TestWebhook_Paid_NoEmailWhenOptedOut(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.users.user.NotifyOrderUpdates = false
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.smtp.Count() != 0 {
		t.Fatalf("emails = %d", e.smtp.Count())
	}
	if e.notifier.count() != 1 {
		t.Fatal("staff push must not depend on the customer's email preference")
	}
}

func TestWebhook_Paid_NilNotifierDoesNotPanic(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	broker := pubsub.NewBroker()
	t.Cleanup(broker.Shutdown)
	router := gin.New()
	router.POST("/webhook", NewPaymentHandler(buildService(e), broker, nil).UpdatePaymentStatusHandler)
	e.router = router
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
}

func buildService(e *webhookEnv) paymentApplication.PaymentService {
	srv := httptest.NewServer(http.HandlerFunc(e.mollie.handler))
	e.t.Cleanup(srv.Close)
	client, _ := mollie.NewClient(srv.Client(), mollie.NewAPITestingConfig(false))
	_ = client.WithAuthenticationValue("test_dummydummydummydummydummydummy")
	client.BaseURL, _ = url.Parse(srv.URL + "/")
	return paymentApplication.NewPaymentService(e.payments, *client, orderApplication.NewOrderService(e.orders, e.coupons), e.users, e.products)
}

func TestWebhook_Paid_StoreReviewTestOrderStaysInvisible(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.orders.order.IsTest = true
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid {
		t.Fatal("paid must still be persisted")
	}
	if e.notifier.count() != 0 || e.topics.get("orderCreated") != 0 || e.topics.get("orderUpdated") != 0 {
		t.Fatal("test order leaked to staff")
	}
}

func TestWebhook_FailedCanceledExpired(t *testing.T) {
	for _, st := range []string{"failed", "canceled", "expired"} {
		t.Run(st, func(t *testing.T) {
			e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
			e.mollie.set(st)
			e.expect(e.deliver("tr_1"), http.StatusOK, "processed")

			if got := string(e.payments.status("tr_1")); got != st {
				t.Fatalf("stored = %s", got)
			}
			status, hist := e.orders.snapshot()
			if status != orderDomain.OrderStatusCanceled {
				t.Fatalf("order = %s, want CANCELLED", status)
			}
			if len(hist) != 1 || hist[0] != orderDomain.OrderStatusCanceled {
				t.Fatalf("status history = %v, want exactly [CANCELLED]", hist)
			}
			if e.coupons.count() != 1 {
				t.Fatalf("coupon rollbacks = %d, want 1", e.coupons.count())
			}
			if e.topics.get("orderUpdated") != 1 || e.topics.get("orderUpdated:"+e.order.ID.String()) != 1 {
				t.Fatal("cancellation not published to subscribers")
			}
			if e.topics.get("orderCreated") != 0 {
				t.Fatal("a failed payment must never announce a new order")
			}
			if e.notifier.count() != 0 {
				t.Fatal("no push for a failed payment")
			}
			if e.smtp.Count() != 0 {
				t.Fatalf("emails = %d, a failed attempt must not email the customer", e.smtp.Count())
			}
		})
	}
}

func TestWebhook_FailedWithoutCoupon(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.orders.order.CouponCode = nil
	e.mollie.set("expired")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.coupons.count() != 0 {
		t.Fatal("no coupon to release")
	}
}

// ---------------------------------------------------------------------------
// Idempotency, replays, ordering
// ---------------------------------------------------------------------------

func TestWebhook_ReplayedPaidIsIdempotent(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	for range 4 {
		e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")
	}
	if e.smtp.Count() != 1 || e.notifier.count() != 1 {
		t.Fatalf("emails=%d pushes=%d, want 1/1", e.smtp.Count(), e.notifier.count())
	}
	if n := e.topics.get("orderCreated"); n != 1 {
		t.Fatalf("orderCreated published %d times", n)
	}
	if e.payments.refreshed != 1 {
		t.Fatalf("status persisted %d times", e.payments.refreshed)
	}
}

func TestWebhook_ReplayedCancellationIsIdempotent(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("expired")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")
	e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")

	_, hist := e.orders.snapshot()
	if len(hist) != 1 {
		t.Fatalf("history rows = %d, want 1", len(hist))
	}
	if e.coupons.count() != 1 {
		t.Fatalf("coupon released %d times", e.coupons.count())
	}
	if n := e.topics.get("orderUpdated"); n != 1 {
		t.Fatalf("orderUpdated published %d times", n)
	}
}

func TestWebhook_StaleDeliveriesAfterPaidAreHarmless(t *testing.T) {
	// A delayed webhook that was sent while the payment was open/expiring is
	// delivered after the payment was paid. The handler re-fetches the current
	// state, so it must see "paid" and do nothing.
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	// stale "expired" and "pending" webhooks arrive late; Mollie still says paid.
	e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")
	e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")

	status, hist := e.orders.snapshot()
	if status != orderDomain.OrderStatusPending || len(hist) != 0 {
		t.Fatalf("order changed by a stale webhook: %s %v", status, hist)
	}
	if e.coupons.count() != 0 {
		t.Fatal("coupon released for a paid order")
	}
}

func TestWebhook_OpenThenPendingThenPaid(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	for _, st := range []string{"pending", "authorized", "paid"} {
		e.mollie.set(st)
		e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	}
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid || e.smtp.Count() != 1 || e.notifier.count() != 1 {
		t.Fatal("full progression must end paid with one email and one push")
	}
}

func TestWebhook_OrderCancelledByStaffThenExpiredWebhook(t *testing.T) {
	// Staff cancelled the unpaid order (history already has CANCELLED, coupon
	// already released); the expiry webhook arrives afterwards.
	e := newWebhookEnv(t, orderDomain.OrderStatusCanceled, paymentDomain.PaymentStatusOpen)
	e.mollie.set("expired")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	_, hist := e.orders.snapshot()
	if len(hist) != 0 {
		t.Fatalf("no new history row for a no-op transition, got %v", hist)
	}
	if e.coupons.count() != 0 {
		t.Fatal("coupon must not be released a second time")
	}
}

func TestWebhook_PaidForCancelledOrderIsRefundedNotAnnounced(t *testing.T) {
	for _, orderStatus := range []orderDomain.OrderStatus{orderDomain.OrderStatusCanceled, orderDomain.OrderStatusFailed} {
		t.Run(string(orderStatus), func(t *testing.T) {
			e := newWebhookEnv(t, orderStatus, paymentDomain.PaymentStatusOpen)
			e.mollie.set("paid")
			e.expect(e.deliver("tr_1"), http.StatusOK, "processed")

			if _, refunds := e.mollie.counts(); refunds != 1 {
				t.Fatalf("refunds = %d, want 1", refunds)
			}
			if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid {
				t.Fatal("paid status must be persisted")
			}
			if e.notifier.count() != 0 || e.topics.get("orderCreated") != 0 || e.topics.get("orderUpdated") != 0 {
				t.Fatal("a refunded order must not be announced to staff")
			}
			// Only the refund email, exactly once, even across replays.
			e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")
			if e.smtp.Count() != 1 {
				t.Fatalf("emails = %d, want 1 refund email", e.smtp.Count())
			}
			if _, refunds := e.mollie.counts(); refunds != 1 {
				t.Fatalf("refunds after replay = %d", refunds)
			}
		})
	}
}

func TestWebhook_PaidForCancelledOrder_RefundFailureIsRetried(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusCanceled, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.mollie.refundCode = http.StatusInternalServerError
	e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusOpen {
		t.Fatal("status must stay open so the retry runs the refund again")
	}
	e.mollie.refundCode = 0
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if _, refunds := e.mollie.counts(); refunds != 1 {
		t.Fatalf("refunds = %d", refunds)
	}
}

// ---------------------------------------------------------------------------
// Failures after Mollie confirmed: nothing is lost, retry completes the work
// ---------------------------------------------------------------------------

func TestWebhook_PaidBusinessLogicFailureIsRetried(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.orders.failFind = true // the order cannot be loaded
	e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusOpen {
		t.Fatal("stored status must not move to paid while the order was not processed")
	}
	if e.notifier.count() != 0 || e.topics.get("orderCreated") != 0 || e.smtp.Count() != 0 {
		t.Fatal("must not announce or email an order that failed to process")
	}
	e.orders.mu.Lock()
	e.orders.failFind = false
	e.orders.mu.Unlock()
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid || e.notifier.count() != 1 || e.smtp.Count() != 1 {
		t.Fatal("retry did not complete the work")
	}
}

func TestWebhook_ProductLookupFailureDoesNotBlockPaid(t *testing.T) {
	// Product names only matter for the customer email; the order must be committed and
	// announced regardless, and the failure is only logged.
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.products.setErr(errors.New("db down"))
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid || e.notifier.count() != 1 {
		t.Fatal("order not committed/announced")
	}
	if e.smtp.Count() != 0 {
		t.Fatal("no email can be built without product names")
	}
}

func TestWebhook_FailedPaymentBusinessLogicFailureIsRetried(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("canceled")
	e.orders.failFind = true // UpdateOrder cannot load the order
	e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusOpen {
		t.Fatal("stored status must stay open")
	}
	e.orders.mu.Lock()
	e.orders.failFind = false
	e.orders.mu.Unlock()
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if st, _ := e.orders.snapshot(); st != orderDomain.OrderStatusCanceled {
		t.Fatalf("order = %s", st)
	}
	if e.coupons.count() != 1 {
		t.Fatalf("coupon rollbacks = %d", e.coupons.count())
	}
}

func TestWebhook_FailedPaymentReloadFailureStillAcks(t *testing.T) {
	// The cancel succeeded but the reload for the pubsub event failed: the
	// work is done, so ack (a retry would be a no-op anyway) and skip publish.
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("expired")
	e.orders.failFind, e.orders.findOK = true, 1 // UpdateOrder's own load works
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusExpired {
		t.Fatal("expired not persisted")
	}
	if e.topics.get("orderUpdated") != 0 {
		t.Fatal("nothing to publish without an order")
	}
}

func TestWebhook_PersistFailureReturns500AndRetrySucceeds(t *testing.T) {
	for _, st := range []string{"pending", "paid", "expired"} {
		t.Run(st, func(t *testing.T) {
			e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
			e.mollie.set(st)
			e.payments.refreshErrs = 1
			e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "temporary failure")
			if e.payments.status("tr_1") != paymentDomain.PaymentStatusOpen {
				t.Fatal("status must not change when persisting failed")
			}
			if e.topics.get("orderCreated") != 0 {
				t.Fatal("published although the status was not persisted")
			}
			e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
			if string(e.payments.status("tr_1")) != st {
				t.Fatal("retry did not persist")
			}
			if st == "paid" && e.notifier.count() != 1 {
				t.Fatalf("pushes = %d", e.notifier.count())
			}
		})
	}
}

func TestWebhook_PaidPersistFailureDoesNotEmailTwice(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.payments.refreshErrs = 1
	e.expect(e.deliver("tr_1"), http.StatusInternalServerError, "")
	if e.smtp.Count() != 0 {
		t.Fatalf("emails = %d before the status is committed, want 0", e.smtp.Count())
	}
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")
	if e.smtp.Count() != 1 {
		t.Fatalf("emails = %d, want exactly 1", e.smtp.Count())
	}
}

func TestWebhook_ConfirmationEmailFailureStillAcksAndIsNotRetried(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	// Point the email backend at a closed port: the send fails after the commit.
	scalewaytest.UseDead(t)
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	if e.payments.status("tr_1") != paymentDomain.PaymentStatusPaid {
		t.Fatal("paid must stay committed")
	}
	if e.notifier.count() != 1 || e.topics.get("orderCreated") != 1 {
		t.Fatal("staff must still be notified when the customer email fails")
	}
	// Mollie replay: already processed, no second attempt, and a healthy backend gets nothing.
	healthy := startSMTPSink(t)
	e.expect(e.deliver("tr_1"), http.StatusOK, "already processed")
	if healthy.Count() != 0 {
		t.Fatalf("replay sent %d emails", healthy.Count())
	}
}

func TestWebhook_EmailOnlyAfterPersistAndNeverForCancelledOrders(t *testing.T) {
	t.Run("store-review test order still gets the email", func(t *testing.T) {
		e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
		e.orders.order.IsTest = true
		e.mollie.set("paid")
		e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
		if e.smtp.Count() != 1 {
			t.Fatalf("emails = %d, want 1", e.smtp.Count())
		}
	})
	t.Run("sold-out product still gets the confirmation", func(t *testing.T) {
		e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
		e.mollie.set("paid")
		e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
		if e.smtp.Count() != 1 {
			t.Fatalf("emails = %d", e.smtp.Count())
		}
	})
}

// ---------------------------------------------------------------------------
// Amount mismatch logging
// ---------------------------------------------------------------------------

func TestWebhook_AmountMismatchIsLoggedAndOrderStillProcessed(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)

	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.payments.payments["tr_1"].Amount = decimal.RequireFromString("19.90")
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")

	if e.notifier.count() != 1 || e.smtp.Count() != 1 {
		t.Fatal("a mismatch must not block the order")
	}
	found := false
	for _, entry := range logs.All() {
		if entry.Message == "payment amount mismatch" {
			found = true
			ctx := entry.ContextMap()
			if ctx["paid"] != "19.9" || ctx["expected"] != "20" {
				t.Errorf("mismatch fields = %v", ctx)
			}
		}
	}
	if !found {
		t.Fatal("amount mismatch was not logged at error level")
	}
}

func TestWebhook_MatchingAmountLogsNoMismatch(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)

	e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	e.expect(e.deliver("tr_1"), http.StatusOK, "processed")
	for _, entry := range logs.All() {
		if entry.Message == "payment amount mismatch" {
			t.Fatal("unexpected mismatch log")
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestWebhook_ConcurrentDeliveriesForTheSamePaymentRunOnce(t *testing.T) {
	for _, st := range []string{"paid", "expired"} {
		t.Run(st, func(t *testing.T) {
			e := newWebhookEnv(t, orderDomain.OrderStatusPending, paymentDomain.PaymentStatusOpen)
			e.mollie.set(st)

			const n = 16
			var wg sync.WaitGroup
			codes := make([]int, n)
			start := make(chan struct{})
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					codes[i] = e.deliver("tr_1").Code
				}()
			}
			close(start)
			wg.Wait()

			for i, c := range codes {
				if c != http.StatusOK {
					t.Fatalf("delivery %d returned %d", i, c)
				}
			}
			if st == "paid" {
				if e.smtp.Count() != 1 || e.notifier.count() != 1 || e.topics.get("orderCreated") != 1 {
					t.Fatalf("emails=%d pushes=%d created=%d, want 1/1/1", e.smtp.Count(), e.notifier.count(), e.topics.get("orderCreated"))
				}
			} else {
				_, hist := e.orders.snapshot()
				if len(hist) != 1 || e.coupons.count() != 1 {
					t.Fatalf("history=%v coupon rollbacks=%d, want 1/1", hist, e.coupons.count())
				}
			}
			if e.payments.refreshed != 1 {
				t.Fatalf("status persisted %d times", e.payments.refreshed)
			}
		})
	}
}

func TestWebhook_ConcurrentPaidForCancelledOrderRefundsOnce(t *testing.T) {
	e := newWebhookEnv(t, orderDomain.OrderStatusCanceled, paymentDomain.PaymentStatusOpen)
	e.mollie.set("paid")
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { e.deliver("tr_1") })
	}
	wg.Wait()
	if _, refunds := e.mollie.counts(); refunds != 1 {
		t.Fatalf("refunds = %d, want 1", refunds)
	}
}
