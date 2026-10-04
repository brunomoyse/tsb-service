package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"tsb-service/pkg/email/scaleway/scalewaytest"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/internal/modules/payment/domain"
	userDomain "tsb-service/internal/modules/user/domain"
)

// mollieAnswering returns a Mollie client whose every call is answered by h.
func mollieAnswering(t *testing.T, h http.HandlerFunc) mollie.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := mollie.NewClient(srv.Client(), mollie.NewAPITestingConfig(false))
	require.NoError(t, err)
	require.NoError(t, c.WithAuthenticationValue("test_dummydummydummydummydummydummy"))
	c.BaseURL, _ = url.Parse(srv.URL + "/")
	return *c
}

// markFailingRepo fails MarkAsRefund once Mollie has accepted the refund.
type markFailingRepo struct {
	*memRepo
}

func (markFailingRepo) MarkAsRefund(context.Context, string, decimal.Decimal) error {
	return errors.New("db down")
}

func TestRefundRemaining_RefundResponseProblems(t *testing.T) {
	jsonReply := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/hal+json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}
	}

	t.Run("a non-created answer from Mollie is not recorded as a refund", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.svc.mollieClient = mollieAnswering(t, jsonReply(http.StatusAccepted, `{"resource":"refund","id":"re_1","amount":{"currency":"EUR","value":"20.00"}}`))

		refundedAmount, _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])
		refunded := refundedAmount.IsPositive()

		require.ErrorContains(t, err, "failed to create refund")
		assert.False(t, refunded)
		assert.True(t, f.repo.payments["tr_1"].AmountRefunded.IsZero())
	})

	t.Run("an unreadable refund amount is reported and not recorded", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.svc.mollieClient = mollieAnswering(t, jsonReply(http.StatusCreated, `{"resource":"refund","id":"re_1","amount":{"currency":"EUR","value":"abc"}}`))

		refundedAmount, _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])
		refunded := refundedAmount.IsPositive()

		require.ErrorContains(t, err, "failed to parse refund amount")
		assert.False(t, refunded)
		assert.True(t, f.repo.payments["tr_1"].AmountRefunded.IsZero())
	})

	t.Run("a refund Mollie accepted but the database could not record is an error", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.svc.repo = markFailingRepo{f.repo}

		refundedAmount, _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])
		refunded := refundedAmount.IsPositive()

		require.ErrorContains(t, err, "failed to mark payment as refunded")
		assert.False(t, refunded)
		assert.Len(t, f.mollie.find(http.MethodPost, "/v2/payments/tr_1/refunds"), 1, "Mollie was asked exactly once")
	})
}

func TestCreatePayment_UnreadableMollieAmount(t *testing.T) {
	setPaymentEnv(t)
	f := newFlow(t, orderDomain.OrderStatusPending, domain.PaymentStatusOpen)
	f.svc.mollieClient = mollieAnswering(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"resource":"payment","id":"tr_bad","status":"open","amount":{"value":"abc","currency":"EUR"}}`))
	})
	o, op := createPaymentOrder("20.00")

	p, err := f.svc.CreatePayment(t.Context(), o, op, userDomain.User{}, nil, nil)

	require.ErrorContains(t, err, "failed to map Mollie payment")
	assert.Nil(t, p)
	assert.NotContains(t, f.repo.payments, "tr_bad", "an unmappable payment must not be saved")
}

func TestHandlePaymentPaid_RefundEmailFailureDoesNotFailTheWebhook(t *testing.T) {
	// Point the mailer at a port nobody listens on, so sending fails.
	scalewaytest.UseDead(t)

	f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
	f.users.user.NotifyOrderUpdates = true

	_, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID)

	require.NoError(t, err, "the refund happened; a mail failure must not make Mollie retry the webhook")
	assert.Len(t, f.mollie.find(http.MethodPost, "/v2/payments/tr_1/refunds"), 1)
	assert.True(t, f.repo.payments["tr_1"].AmountRefunded.Equal(decimal.RequireFromString("20.00")))
}

func TestOrderPaymentLoader(t *testing.T) {
	orderID := uuid.New()
	pay := &domain.MolliePayment{MolliePaymentID: "tr_9", OrderID: orderID}
	repo := newMemRepo(pay)
	f := &paymentService{repo: repo}

	t.Run("loader on the context resolves payments per order", func(t *testing.T) {
		ctx := AttachDataLoaders(t.Context(), f)
		got, err := GetOrderPaymentLoader(ctx).Loader.Load(ctx, orderID.String())
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "tr_9", got[0].MolliePaymentID)

		none, err := GetOrderPaymentLoader(ctx).Loader.Load(ctx, uuid.NewString())
		require.NoError(t, err)
		assert.Empty(t, none)
	})

	t.Run("no loader on the context", func(t *testing.T) {
		assert.Nil(t, GetOrderPaymentLoader(t.Context()))
	})
}
