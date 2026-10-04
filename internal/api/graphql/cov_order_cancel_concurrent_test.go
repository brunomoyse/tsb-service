package graphql_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentInterfaces "tsb-service/internal/modules/payment/interfaces"
)

// Cancelling a paid order refunds it. Two cancels at the same time (a double click on the dashboard,
// the dashboard and a handheld) must refund it once: the cancel runs under the advisory lock of the
// payment, the same one the Mollie webhook takes.

// cancelConcurrently runs n cancellations of the order at once, released together, and returns what
// each of them answered.
func (e *covEnv) cancelConcurrently(t *testing.T, orderID string, n int) []error {
	t.Helper()
	id := uuid.MustParse(orderID)
	ctx := e.ctxFor(e.Fixtures.AdminUser.ID.String(), true, "fr")
	cancelled := orderDomain.OrderStatusCanceled
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		errs  = make([]error, n)
	)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = e.Resolver.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: &cancelled})
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

func TestConcurrentCancelsRefundOnce(t *testing.T) {
	env := setupCovEnv(t, covOptions{})

	// Mollie accepts a second refund for this payment: its amountRemaining "may be higher than the
	// payment amount" (e.g. to reimburse a return shipment), so nothing at Mollie stops a double
	// refund. The lookup is slow, so that every caller has read the payment before the first one
	// has refunded it, unless something serialises them.
	c := env.newPushCustomer(t, "double-click", true)
	order := env.placeOnlineOrder(t, c)
	env.markPaid(t, order.ID)
	payID := order.Payment.MolliePaymentID
	amount := env.paymentCol(t, "amount", payID)
	env.Mollie.SetRefundCap(payID, decimal.RequireFromString(amount).Mul(decimal.NewFromInt(2)).String())
	env.Mollie.SetLookupDelay(60 * time.Millisecond)

	for i, err := range env.cancelConcurrently(t, order.ID, 3) {
		require.NoError(t, err, "caller %d: the second and third cancel are a no-op success, not an error", i)
	}

	assert.Equal(t, []string{"POST /v2/payments/" + payID + "/refunds"}, env.Mollie.CallsMatching("POST /v2/payments/"+payID), "exactly one refund")
	assert.Equal(t, []string{amount}, env.Mollie.RefundAmounts(t, payID))
	assert.Equal(t, amount, env.paymentCol(t, "amount_refunded", payID))
	assert.Equal(t, "CANCELLED", env.orderStatus(t, order.ID))
	assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM order_status_history WHERE order_id = $1 AND status = 'CANCELLED'`, order.ID),
		"the cancellation is recorded once")
	// One e-mail of each kind, however many callers.
	env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
	env.Mail.WaitSubject(t, c.email, "Order cancelled")
	require.Never(t, func() bool {
		return env.Mail.CountSubject(t, c.email, "Your refund has been issued") > 1 || env.Mail.CountSubject(t, c.email, "Order cancelled") > 1
	}, 200*time.Millisecond, 20*time.Millisecond)
}

func TestConcurrentCancelsOfAnOpenPaymentCancelItOnce(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "double-click-open", false)
	order := env.placeOnlineOrder(t, c)
	payID := order.Payment.MolliePaymentID
	env.Mollie.SetLookupDelay(60 * time.Millisecond)

	for i, err := range env.cancelConcurrently(t, order.ID, 3) {
		require.NoError(t, err, "caller %d", i) // Mollie would refuse a second cancel of the payment
	}

	assert.Equal(t, []string{"DELETE /v2/payments/" + payID}, env.Mollie.CallsMatching("DELETE /v2/payments/"+payID))
	assert.Equal(t, "CANCELLED", env.orderStatus(t, order.ID))
	assert.Equal(t, "canceled", env.paymentCol(t, "status", payID))
}

// A "paid" webhook that lands while staff cancel the order waits for the cancellation: it then finds
// the order cancelled and refunded, and never announces it to staff as a newly paid order.
func TestPaidWebhookDuringACancelDoesNotAnnounceTheOrder(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	deliver := env.webhook()
	announced := env.Resolver.Broker.Subscribe("orderCreated")

	c := env.newPushCustomer(t, "paid-mid-cancel", false)
	order := env.placeOnlineOrder(t, c)
	payID := order.Payment.MolliePaymentID
	amount := env.paymentCol(t, "amount", payID)
	// The customer has just paid at Mollie; our row still says open, the webhook has not been handled.
	env.Mollie.MarkPaid(payID)
	env.Mollie.SetRefundDelay(300 * time.Millisecond) // the order is still PENDING while the refund is under way

	cancelDone := make(chan []error, 1)
	go func() { cancelDone <- env.cancelConcurrently(t, order.ID, 1) }()
	// The cancellation holds the payment lock once it asks Mollie for the payment.
	require.Eventually(t, func() bool {
		return len(env.Mollie.CallsMatching("GET /v2/payments/"+payID)) > 0
	}, 5*time.Second, time.Millisecond)

	w := deliver(payID)

	require.NoError(t, (<-cancelDone)[0])
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "processed")
	select {
	case msg := <-announced:
		t.Fatalf("the order was announced as newly paid: %+v", msg)
	default:
	}
	assert.Equal(t, "CANCELLED", env.orderStatus(t, order.ID))
	assert.Equal(t, []string{amount}, env.Mollie.RefundAmounts(t, payID), "refunded once, by the cancellation")
	assert.Equal(t, amount, env.paymentCol(t, "amount_refunded", payID))
	assert.Equal(t, "paid", env.paymentCol(t, "status", payID), "the webhook still records what Mollie says")
}

func (e *covEnv) orderStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	require.NoError(t, e.DB.DB.GetContext(t.Context(), &s, `SELECT order_status FROM orders WHERE id = $1`, id))
	return s
}

// webhook returns a function that delivers a Mollie webhook for a payment id to the real handler.
func (e *covEnv) webhook() func(payID string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/webhook", paymentInterfaces.NewPaymentHandler(e.Resolver.PaymentService, e.Resolver.Broker, nil).UpdatePaymentStatusHandler)
	return func(payID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("id="+payID))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
}
