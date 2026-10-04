package application

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/modules/payment/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
)

// fakeMollie records the Mollie API calls the service makes.
type fakeMollie struct {
	mu    sync.Mutex
	calls []string
	// What GET /v2/payments/tr_1 answers: the status, whether it can still be cancelled and, when
	// set, the amountRefunded / amountRemaining Mollie reports.
	status     string
	cancelable bool
	refunded   string
	remaining  string
}

func (f *fakeMollie) handler(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/hal+json")
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		status, cancelable, refunded, remaining := f.status, f.cancelable, f.refunded, f.remaining
		f.mu.Unlock()
		extra := ""
		if refunded != "" {
			extra += `,"amountRefunded":{"currency":"EUR","value":"` + refunded + `"}`
		}
		if remaining != "" {
			extra += `,"amountRemaining":{"currency":"EUR","value":"` + remaining + `"}`
		}
		_, _ = fmt.Fprintf(w, `{"resource":"payment","id":"tr_1","status":%q,"isCancelable":%t,"amount":{"currency":"EUR","value":"20.00"}%s}`, status, cancelable, extra)
	case http.MethodPost: // create refund
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"resource":"refund","id":"re_1","amount":{"currency":"EUR","value":"20.00"},"status":"pending"}`))
	case http.MethodDelete: // cancel payment
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resource":"payment","id":"tr_1","status":"canceled"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeMollie) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

// fakePaymentRepo holds a single payment and applies MarkAsRefund to it.
type fakePaymentRepo struct {
	domain.PaymentRepository
	payment *domain.MolliePayment
}

func (r *fakePaymentRepo) FindByOrderID(_ context.Context, _ uuid.UUID) (*domain.MolliePayment, error) {
	cp := *r.payment
	return &cp, nil
}

func (r *fakePaymentRepo) FindByExternalID(_ context.Context, _ string) (*domain.MolliePayment, error) {
	cp := *r.payment
	return &cp, nil
}

func (r *fakePaymentRepo) MarkAsRefund(_ context.Context, _ string, amount decimal.Decimal) error {
	r.payment.AmountRefunded = amount
	return nil
}

func (r *fakePaymentRepo) RefreshStatus(_ context.Context, _ string, u *domain.PaymentStatusUpdate) (*uuid.UUID, error) {
	r.payment.Status = u.Status
	r.payment.CanceledAt = u.CanceledAt
	return &r.payment.OrderID, nil
}

type fakeOrders struct {
	orderApplication.OrderService
	order *orderDomain.Order
}

func (f *fakeOrders) GetOrderByID(_ context.Context, _ uuid.UUID) (*orderDomain.Order, *[]orderDomain.OrderProductRaw, error) {
	return f.order, &[]orderDomain.OrderProductRaw{}, nil
}

type fakeUsers struct {
	userApplication.UserService
}

func (fakeUsers) GetUserByID(_ context.Context, id string) (*userDomain.User, error) {
	return &userDomain.User{ID: uuid.MustParse(id), NotifyOrderUpdates: false}, nil
}

func newTestService(t *testing.T, payment *domain.MolliePayment, order *orderDomain.Order) (*paymentService, *fakeMollie, *fakePaymentRepo) {
	t.Helper()
	fm := &fakeMollie{status: string(payment.Status), cancelable: payment.IsCancelable}
	srv := httptest.NewServer(http.HandlerFunc(fm.handler))
	t.Cleanup(srv.Close)

	client, err := mollie.NewClient(srv.Client(), mollie.NewAPITestingConfig(false))
	if err != nil {
		t.Fatalf("mollie client: %v", err)
	}
	if err := client.WithAuthenticationValue("test_dummydummydummydummydummydummy"); err != nil {
		t.Fatalf("mollie auth: %v", err)
	}
	client.BaseURL, _ = url.Parse(srv.URL + "/")

	repo := &fakePaymentRepo{payment: payment}
	svc := &paymentService{
		repo:         repo,
		mollieClient: *client,
		orderService: &fakeOrders{order: order},
		userService:  fakeUsers{},
	}
	return svc, fm, repo
}

func dummyOrder(status orderDomain.OrderStatus) *orderDomain.Order {
	return &orderDomain.Order{
		ID:          uuid.New(),
		UserID:      uuid.New(),
		OrderStatus: status,
		TotalPrice:  decimal.RequireFromString("20.00"),
	}
}

func dummyPayment(status domain.PaymentStatus) *domain.MolliePayment {
	return &domain.MolliePayment{
		MolliePaymentID: "tr_1",
		Status:          status,
		Amount:          decimal.RequireFromString("20.00"),
		IsCancelable:    true,
	}
}

func TestHandlePaymentPaid_CancelledOrderIsRefundedOnce(t *testing.T) {
	order := dummyOrder(orderDomain.OrderStatusCanceled)
	// Stored status is still open: the webhook persists paid only afterwards.
	svc, fm, _ := newTestService(t, dummyPayment(domain.PaymentStatusOpen), order)

	got, err := svc.HandlePaymentPaid(t.Context(), order.ID)
	if err != nil {
		t.Fatalf("HandlePaymentPaid: %v", err)
	}
	if got.OrderStatus != orderDomain.OrderStatusCanceled {
		t.Fatalf("status = %s, want CANCELLED", got.OrderStatus)
	}
	if n := fm.count("POST /v2/payments/tr_1/refunds"); n != 1 {
		t.Fatalf("refund calls = %d, want 1", n)
	}

	// A Mollie retry of the same webhook must not refund a second time.
	if _, err := svc.HandlePaymentPaid(t.Context(), order.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := fm.count("POST /v2/payments/tr_1/refunds"); n != 1 {
		t.Fatalf("refund calls after retry = %d, want 1", n)
	}
}

func TestSettleCancelledOrderPayment(t *testing.T) {
	t.Run("open payment is cancelled at Mollie", func(t *testing.T) {
		svc, fm, _ := newTestService(t, dummyPayment(domain.PaymentStatusOpen), dummyOrder(orderDomain.OrderStatusCanceled))
		refunded, err := svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(domain.PaymentStatusOpen))
		if err != nil || refunded {
			t.Fatalf("refunded=%v err=%v, want false nil", refunded, err)
		}
		if n := fm.count("DELETE /v2/payments/tr_1"); n != 1 {
			t.Fatalf("cancel calls = %d, want 1", n)
		}
	})

	t.Run("paid payment is refunded, already refunded is skipped", func(t *testing.T) {
		svc, fm, repo := newTestService(t, dummyPayment(domain.PaymentStatusPaid), dummyOrder(orderDomain.OrderStatusCanceled))
		refunded, err := svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(domain.PaymentStatusPaid))
		if err != nil || !refunded {
			t.Fatalf("refunded=%v err=%v, want true nil", refunded, err)
		}
		again, err := svc.SettleCancelledOrderPayment(t.Context(), repo.payment)
		if err != nil || again {
			t.Fatalf("second refund: refunded=%v err=%v, want false nil", again, err)
		}
		if n := fm.count("POST /v2/payments/tr_1/refunds"); n != 1 {
			t.Fatalf("refund calls = %d, want 1", n)
		}
	})

	t.Run("failed payment needs nothing", func(t *testing.T) {
		svc, fm, _ := newTestService(t, dummyPayment(domain.PaymentStatusFailed), dummyOrder(orderDomain.OrderStatusCanceled))
		if _, err := svc.SettleCancelledOrderPayment(t.Context(), dummyPayment(domain.PaymentStatusFailed)); err != nil {
			t.Fatal(err)
		}
		if len(fm.calls) != 0 {
			t.Fatalf("unexpected Mollie calls: %v", fm.calls)
		}
	})
}
