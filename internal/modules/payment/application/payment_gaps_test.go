package application

import (
	"context"
	"errors"
	"fmt"
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
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

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
	// The payment itself (GET) is paid and refundable; the refund request is answered with code and body.
	jsonReply := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/hal+json")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"resource":"payment","id":"tr_1","status":"paid","amount":{"currency":"EUR","value":"20.00"},"amountRemaining":{"currency":"EUR","value":"20.00"}}`))
				return
			}
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

	// Mollie refusing the refund (422) cannot be cured by retrying; an outage or a rate limit can.
	t.Run("a refund Mollie refuses with 422 is not refundable, a 5xx or 429 is just an error to retry", func(t *testing.T) {
		for code, notRefundable := range map[int]bool{http.StatusUnprocessableEntity: true, http.StatusInternalServerError: false, http.StatusTooManyRequests: false} {
			f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
			f.svc.mollieClient = mollieAnswering(t, jsonReply(code, fmt.Sprintf(`{"status":%d,"title":"x","detail":"The refund period has passed"}`, code)))

			refundedAmount, _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

			require.Error(t, err, "%d", code)
			assert.Equal(t, notRefundable, errors.Is(err, domain.ErrPaymentNotRefundable), "%d: %v", code, err)
			assert.True(t, refundedAmount.IsZero())
			assert.True(t, f.repo.payments["tr_1"].AmountRefunded.IsZero(), "%d: nothing recorded", code)
		}
	})
}

// Mollie only reports amountRemaining when refunds are available for the payment. A paid payment
// without it (voucher, gift card, expired refund window) can not be refunded: that is its own error,
// not "something went wrong, retry".
func TestRefundRemaining_NotRefundable(t *testing.T) {
	t.Run("a paid payment without amountRemaining is not refundable, and Mollie is not asked to refund it", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.mollie.noRemaining = true

		refundedAmount, _, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		require.ErrorIs(t, err, domain.ErrPaymentNotRefundable)
		assert.True(t, refundedAmount.IsZero())
		assert.Empty(t, f.mollie.refundAmounts(t))
		assert.True(t, f.repo.payments["tr_1"].AmountRefunded.IsZero())
	})

	t.Run("a payment that has been refunded in full is not 'not refundable' although Mollie reports no amountRemaining", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusConfirmed, domain.PaymentStatusPaid)
		f.mollie.noRemaining = true
		f.mollie.refunded = "20.00"

		refundedAmount, total, err := f.svc.refundRemaining(t.Context(), f.repo.payments["tr_1"])

		require.NoError(t, err)
		assert.True(t, refundedAmount.IsZero())
		assert.True(t, total.Equal(decimal.RequireFromString("20.00")))
	})

	t.Run("settling the cancelled order of such a payment reports it", func(t *testing.T) {
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusPaid)
		f.mollie.noRemaining = true

		_, err := f.svc.SettleCancelledOrderPayment(t.Context(), f.repo.payments["tr_1"])

		require.ErrorIs(t, err, domain.ErrPaymentNotRefundable)
	})

	t.Run("the webhook of a payment paid for a cancelled order acknowledges it instead of having Mollie retry, and says so loudly", func(t *testing.T) {
		core, logs := observer.New(zap.ErrorLevel)
		t.Cleanup(zap.ReplaceGlobals(zap.New(core)))
		sink := startSMTPSink(t)
		f := newFlow(t, orderDomain.OrderStatusCanceled, domain.PaymentStatusOpen)
		f.users.user.NotifyOrderUpdates = true
		f.mollie.status = "paid"
		f.mollie.noRemaining = true

		got, err := f.svc.HandlePaymentPaid(t.Context(), f.order.ID)

		require.NoError(t, err, "a 500 would only make Mollie retry something that cannot work")
		assert.Equal(t, orderDomain.OrderStatusCanceled, got.OrderStatus)
		assert.Empty(t, f.mollie.refundAmounts(t))
		assert.Zero(t, sink.Count(), "the customer is not told about a refund that did not happen")
		require.Equal(t, 1, logs.FilterMessageSnippet("refund the customer manually").Len(), "logged at error level")
		assert.Equal(t, f.order.ID.String(), logs.All()[0].ContextMap()["order_id"])
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
