package graphql_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cancelling an order settles its payment: a paid one is refunded in full, an open one is cancelled
// at Mollie so the customer cannot pay for it, and the customer is told either way.

func (e *covEnv) placeOnlineOrder(t *testing.T, c *pushCustomer) *createdOrder {
	t.Helper()
	order := mustCreateOrder(t, e.TestContext, c.token, "en", map[string]any{
		"orderType": "PICKUP", "isOnlinePayment": true, "items": lines(e.Fixtures.SalmonSushi.ID, 1),
	})
	require.NotNil(t, order.Payment)
	return order
}

// markPaid is what the Mollie webhook does: the payment of the order becomes paid.
func (e *covEnv) markPaid(t *testing.T, orderID string) {
	t.Helper()
	resp := gqlAs(t, e.TestContext, adminToken(t, e.TestContext), "fr",
		`mutation ($o: ID!) { updatePaymentStatus(orderId: $o, status: "paid") { status molliePaymentId } }`, map[string]any{"o": orderID})
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
}

func TestUpdateOrderCancellationSettlesThePayment(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	logs := captureLogs(t)

	t.Run("a paid order is refunded in full, once, and the customer is told", func(t *testing.T) {
		c := env.newPushCustomer(t, "refund", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "KITCHEN_CLOSED"})
		assert.Equal(t, "CANCELLED", got.Status)
		require.NotNil(t, got.CancellationReason)
		assert.Equal(t, "KITCHEN_CLOSED", *got.CancellationReason)

		assert.Equal(t, []string{"POST /v2/payments/" + payID + "/refunds"}, env.Mollie.callsMatching("POST /v2/payments/"+payID))
		var refunded string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &refunded, `SELECT amount_refunded::text FROM mollie_payments WHERE mollie_payment_id = $1`, payID))
		var amount string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &amount, `SELECT amount::text FROM mollie_payments WHERE mollie_payment_id = $1`, payID))
		assert.Equal(t, amount, refunded, "the whole payment is refunded")
		assert.Equal(t, []string{amount}, env.Mollie.refundAmounts(t, payID), "Mollie is asked for the whole payment amount")

		env.Mail.waitSubject(t, c.email, "Your refund has been issued")
		env.Mail.waitSubject(t, c.email, "Order canceled")

		// Saving the cancelled order again neither refunds nor writes to the customer again.
		again := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, "CANCELLED", again.Status)
		assert.Len(t, env.Mollie.callsMatching("POST /v2/payments/"+payID), 1)
		require.Never(t, func() bool {
			return env.Mail.countSubject(t, c.email, "Order canceled") > 1 || env.Mail.countSubject(t, c.email, "Your refund has been issued") > 1
		}, 400*time.Millisecond, 20*time.Millisecond)
	})

	// BUG(product decision pending): a payment that is already partially refunded (staff refunded
	// part of it in the Mollie dashboard) is refunded for its FULL amount again on cancellation,
	// instead of Amount - AmountRefunded; see also payment/application TestRefundRemaining_Failures.
	// Flip the expectation to the remaining amount ("amount - 5.00") once the owner decides.
	t.Run("a partially refunded payment is refunded for the full amount again", func(t *testing.T) {
		c := env.newPushCustomer(t, "partial", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID
		_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE mollie_payments SET amount_refunded = 5.00 WHERE mollie_payment_id = $1`, payID)
		require.NoError(t, err)
		var amount string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &amount, `SELECT amount::text FROM mollie_payments WHERE mollie_payment_id = $1`, payID))

		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, []string{amount}, env.Mollie.refundAmounts(t, payID), "currently the full amount, not amount - 5.00")
	})

	t.Run("an open payment is cancelled at Mollie, no refund is issued", func(t *testing.T) {
		c := env.newPushCustomer(t, "openpay", true)
		order := env.placeOnlineOrder(t, c)
		payID := order.Payment.MolliePaymentID

		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, []string{"DELETE /v2/payments/" + payID}, env.Mollie.callsMatching("DELETE /v2/payments/"+payID))
		assert.Empty(t, env.Mollie.callsMatching("POST /v2/payments/"+payID))
		env.Mail.waitSubject(t, c.email, "Order canceled")
		assert.Zero(t, env.Mail.countSubject(t, c.email, "Your refund has been issued"))
	})

	t.Run("an open payment that Mollie no longer lets us cancel is left alone", func(t *testing.T) {
		c := env.newPushCustomer(t, "locked", true)
		order := env.placeOnlineOrder(t, c)
		_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE mollie_payments SET is_cancelable = false WHERE mollie_payment_id = $1`, order.Payment.MolliePaymentID)
		require.NoError(t, err)

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, "CANCELLED", got.Status)
		assert.Empty(t, env.Mollie.callsMatching("DELETE /v2/payments/"+order.Payment.MolliePaymentID))
		assert.Equal(t, 1, logs.FilterMessage("open payment of a cancelled order is not cancelable at Mollie").Len())
	})

	// BUG(product decision pending): the CANCELLED status is saved BEFORE the payment is settled
	// (resolver/order.go UpdateOrder persists, then calls SettleCancelledOrderPayment). When Mollie
	// refuses, the staff member gets an error but the order is already CANCELLED with the money not
	// refunded, and because only the transition into CANCELLED settles the payment, saving the
	// order again never retries the refund. The customer paid and was neither refunded nor told.
	// Flip these assertions (status stays PAID/previous, or a re-save retries) once the owner decides.
	t.Run("a refund that Mollie refuses fails the update, the failure is not hidden", func(t *testing.T) {
		c := env.newPushCustomer(t, "norefund", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID
		env.Mollie.setFail(false, true, false)
		t.Cleanup(func() { env.Mollie.setFail(false, false, false) })

		_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "CANCELLED"})
		require.NotNil(t, oerr)
		assert.Equal(t, "Internal server error", oerr.Message)
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM mollie_payments WHERE mollie_payment_id = $1 AND amount_refunded > 0`, payID))
		assert.Len(t, env.Mollie.callsMatching("POST /v2/payments/"+payID+"/refunds"), 1, "Mollie was asked once")

		// KNOWN BUG: the order is CANCELLED although the refund failed.
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM orders WHERE id = $1 AND order_status = 'CANCELLED'`, order.ID))

		// KNOWN BUG: with Mollie healthy again, saving the order again does not retry the refund.
		env.Mollie.setFail(false, false, false)
		again := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, "CANCELLED", again.Status)
		assert.Len(t, env.Mollie.callsMatching("POST /v2/payments/"+payID+"/refunds"), 1, "no retry on re-save")
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM mollie_payments WHERE mollie_payment_id = $1 AND amount_refunded > 0`, payID), "the customer is still not refunded")
	})

	// BUG(product decision pending): same ordering problem for an open payment: the order is
	// CANCELLED and the payment is still open (payable) at Mollie after the refused cancel, and a
	// re-save does not retry the cancel.
	t.Run("an open payment that Mollie refuses to cancel fails the update", func(t *testing.T) {
		c := env.newPushCustomer(t, "nocancel", true)
		order := env.placeOnlineOrder(t, c)
		payID := order.Payment.MolliePaymentID
		env.Mollie.setFail(false, false, true)
		t.Cleanup(func() { env.Mollie.setFail(false, false, false) })

		_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "CANCELLED"})
		require.NotNil(t, oerr)
		assert.Len(t, env.Mollie.callsMatching("DELETE /v2/payments/"+payID), 1)

		// KNOWN BUG: CANCELLED is persisted although the payment could not be cancelled.
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM orders WHERE id = $1 AND order_status = 'CANCELLED'`, order.ID))

		// KNOWN BUG: no retry of the Mollie cancel when the order is saved again.
		env.Mollie.setFail(false, false, false)
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Len(t, env.Mollie.callsMatching("DELETE /v2/payments/"+payID), 1, "no retry on re-save")
	})

	t.Run("a cash order has no payment to settle", func(t *testing.T) {
		c := env.newPushCustomer(t, "cash", true)
		order := env.placeOrder(t, c, "en")
		before := len(env.Mollie.callsMatching(""))
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "OTHER"})
		env.Mail.waitSubject(t, c.email, "Order canceled")
		assert.Equal(t, before, len(env.Mollie.callsMatching("")), "Mollie is not involved")
	})
}

// updatePaymentStatus is the staff override of a payment's status.
func TestUpdatePaymentStatusMutation(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "paystatus", false)
	order := env.placeOnlineOrder(t, c)
	const m = `mutation ($o: ID!, $s: String!) { updatePaymentStatus(orderId: $o, status: $s) { status molliePaymentId orderId } }`

	resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "fr", m, map[string]any{"o": order.ID, "s": "paid"})
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	var data struct {
		UpdatePaymentStatus struct{ Status, MolliePaymentID, OrderID string }
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, "paid", data.UpdatePaymentStatus.Status)
	assert.Equal(t, order.Payment.MolliePaymentID, data.UpdatePaymentStatus.MolliePaymentID)
	assert.Equal(t, order.ID, data.UpdatePaymentStatus.OrderID)

	t.Run("an order without a payment is an error", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "fr", m, map[string]any{"o": uuid.NewString(), "s": "paid"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "Internal server error", resp.Errors[0].Message)
	})

	t.Run("a customer may not touch payments", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, c.token, "fr", m, map[string]any{"o": order.ID, "s": "paid"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
		var status string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &status, `SELECT status FROM mollie_payments WHERE order_id = $1`, order.ID))
		assert.Equal(t, "paid", status)
	})
}
