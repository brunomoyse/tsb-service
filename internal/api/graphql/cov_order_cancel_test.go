package graphql_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	orderDomain "tsb-service/internal/modules/order/domain"
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

// markPaid is what the Mollie webhook does: the payment of the order becomes paid, at Mollie (the
// stub) and in our database.
func (e *covEnv) markPaid(t *testing.T, orderID string) {
	t.Helper()
	resp := gqlAs(t, e.TestContext, adminToken(t, e.TestContext), "fr",
		`mutation ($o: ID!) { updatePaymentStatus(orderId: $o, status: "paid") { status molliePaymentId } }`, map[string]any{"o": orderID})
	require.Empty(t, resp.Errors, "%+v", resp.Errors)
	var data struct {
		UpdatePaymentStatus struct{ MolliePaymentID string }
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	e.Mollie.MarkPaid(data.UpdatePaymentStatus.MolliePaymentID)
}

// paymentCol reads one column of the payment of a Mollie payment id, as text.
func (e *covEnv) paymentCol(t *testing.T, col, payID string) string {
	t.Helper()
	var v string
	require.NoError(t, e.DB.DB.GetContext(t.Context(), &v, `SELECT `+col+`::text FROM mollie_payments WHERE mollie_payment_id = $1`, payID))
	return v
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

		assert.Equal(t, []string{"POST /v2/payments/" + payID + "/refunds"}, env.Mollie.CallsMatching("POST /v2/payments/"+payID))
		var refunded string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &refunded, `SELECT amount_refunded::text FROM mollie_payments WHERE mollie_payment_id = $1`, payID))
		var amount string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &amount, `SELECT amount::text FROM mollie_payments WHERE mollie_payment_id = $1`, payID))
		assert.Equal(t, amount, refunded, "the whole payment is refunded")
		assert.Equal(t, []string{amount}, env.Mollie.RefundAmounts(t, payID), "Mollie is asked for the whole payment amount")

		env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
		env.Mail.WaitSubject(t, c.email, "Order canceled")

		// Saving the cancelled order again neither refunds nor writes to the customer again.
		again := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, "CANCELLED", again.Status)
		assert.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID), 1)
		require.Never(t, func() bool {
			return env.Mail.CountSubject(t, c.email, "Order canceled") > 1 || env.Mail.CountSubject(t, c.email, "Your refund has been issued") > 1
		}, 400*time.Millisecond, 20*time.Millisecond)
	})

	// Part of the payment may already be back with the customer (staff refunded it in the Mollie
	// dashboard): only what is left is refunded, whether or not our own row knows about it.
	t.Run("a partially refunded payment is refunded for what is left only", func(t *testing.T) {
		for name, recordedHere := range map[string]bool{"known to us": true, "known to Mollie only": false} {
			t.Run(name, func(t *testing.T) {
				c := env.newPushCustomer(t, "partial", true)
				order := env.placeOnlineOrder(t, c)
				env.markPaid(t, order.ID)
				payID := order.Payment.MolliePaymentID
				env.Mollie.SetRefunded(payID, "5.00")
				if recordedHere {
					_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE mollie_payments SET amount_refunded = 5.00 WHERE mollie_payment_id = $1`, payID)
					require.NoError(t, err)
				}
				amount := env.paymentCol(t, "amount", payID)
				remaining := env.paymentCol(t, "(amount - 5.00)", payID)

				got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})

				assert.Equal(t, "CANCELLED", got.Status)
				assert.Equal(t, []string{remaining}, env.Mollie.RefundAmounts(t, payID), "Mollie is asked for amount - 5.00, not for the full amount")
				assert.Equal(t, amount, env.paymentCol(t, "amount_refunded", payID), "the running total is recorded: the payment is refunded in full")
			})
		}
	})

	t.Run("a payment that is already refunded in full is not refunded again, our row catches up", func(t *testing.T) {
		c := env.newPushCustomer(t, "allback", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID
		amount := env.paymentCol(t, "amount", payID)
		// What a refund looks like whose bookkeeping failed on an earlier attempt: Mollie has paid
		// the money back, our row does not know.
		env.Mollie.SetRefunded(payID, amount)

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})

		assert.Equal(t, "CANCELLED", got.Status)
		assert.Empty(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID), "nothing is left to refund")
		assert.Equal(t, amount, env.paymentCol(t, "amount_refunded", payID))
	})

	t.Run("an open payment is cancelled at Mollie, no refund is issued", func(t *testing.T) {
		c := env.newPushCustomer(t, "openpay", true)
		order := env.placeOnlineOrder(t, c)
		payID := order.Payment.MolliePaymentID

		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, []string{"DELETE /v2/payments/" + payID}, env.Mollie.CallsMatching("DELETE /v2/payments/"+payID))
		assert.Empty(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID))
		env.Mail.WaitSubject(t, c.email, "Order canceled")
		assert.Zero(t, env.Mail.CountSubject(t, c.email, "Your refund has been issued"))
	})

	t.Run("an open payment that Mollie no longer lets us cancel is left alone", func(t *testing.T) {
		c := env.newPushCustomer(t, "locked", true)
		order := env.placeOnlineOrder(t, c)
		env.Mollie.LockPayment(order.Payment.MolliePaymentID) // Mollie decides: the customer is in the middle of paying

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, "CANCELLED", got.Status)
		assert.Empty(t, env.Mollie.CallsMatching("DELETE /v2/payments/"+order.Payment.MolliePaymentID))
		assert.Equal(t, 1, logs.FilterMessage("open payment of a cancelled order is not cancelable at Mollie").Len())
	})

	// Cancelling settles the payment first and saves CANCELLED only when that worked. A refund or a
	// cancel that Mollie refuses leaves the order exactly as it was, with a clear typed error, so
	// staff just retry; the money is never refunded twice.
	cancelRefused := func(t *testing.T, oerr *orderErr) {
		t.Helper()
		require.NotNil(t, oerr)
		assert.Equal(t, "PAYMENT_SETTLEMENT_FAILED", oerr.Extensions["code"])
		assert.Contains(t, oerr.Message, "the order was NOT cancelled", "the staff member is told what state the order is in")
		assert.NotContains(t, oerr.Message, "Mollie refused", "provider details stay in the logs")
	}
	orderState := func(t *testing.T, id string) string {
		t.Helper()
		var s string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &s, `SELECT order_status FROM orders WHERE id = $1`, id))
		return s
	}

	t.Run("a refund that Mollie refuses leaves the order unchanged, and the retry refunds it", func(t *testing.T) {
		c := env.newPushCustomer(t, "norefund", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID
		before := orderState(t, order.ID)
		env.Mollie.SetFail(false, true, false)
		t.Cleanup(func() { env.Mollie.SetFail(false, false, false) })

		_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "OTHER"})

		cancelRefused(t, oerr)
		assert.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID+"/refunds"), 1, "Mollie was asked once")
		assert.Equal(t, before, orderState(t, order.ID), "the order is NOT cancelled")
		assert.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM order_status_history WHERE order_id = $1 AND status = 'CANCELLED'`, order.ID))
		assert.Equal(t, "0.00", env.paymentCol(t, "amount_refunded", payID))
		require.Never(t, func() bool { return env.Mail.CountSubject(t, c.email, "Order canceled") > 0 }, 300*time.Millisecond, 20*time.Millisecond)

		// With Mollie healthy again the same call goes through and refunds once.
		env.Mollie.SetFail(false, false, false)
		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "OTHER"})
		assert.Equal(t, "CANCELLED", got.Status)
		assert.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID+"/refunds"), 2, "the refused call and the retry")
		assert.Equal(t, env.paymentCol(t, "amount", payID), env.paymentCol(t, "amount_refunded", payID))
		env.Mail.WaitSubject(t, c.email, "Your refund has been issued")
		env.Mail.WaitSubject(t, c.email, "Order canceled")
	})

	t.Run("an open payment that Mollie refuses to cancel leaves the order unchanged, and the retry cancels it", func(t *testing.T) {
		c := env.newPushCustomer(t, "nocancel", true)
		order := env.placeOnlineOrder(t, c)
		payID := order.Payment.MolliePaymentID
		before := orderState(t, order.ID)
		env.Mollie.SetFail(false, false, true)
		t.Cleanup(func() { env.Mollie.SetFail(false, false, false) })

		_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "CANCELLED"})

		cancelRefused(t, oerr)
		assert.Len(t, env.Mollie.CallsMatching("DELETE /v2/payments/"+payID), 1)
		assert.Equal(t, before, orderState(t, order.ID), "the order is NOT cancelled")
		assert.Equal(t, "open", env.paymentCol(t, "status", payID))

		env.Mollie.SetFail(false, false, false)
		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Equal(t, "CANCELLED", got.Status)
		assert.Len(t, env.Mollie.CallsMatching("DELETE /v2/payments/"+payID), 2, "the refused call and the retry")
		assert.Equal(t, "canceled", env.paymentCol(t, "status", payID), "recorded at once, not only when Mollie's webhook arrives")
	})

	t.Run("a Mollie lookup failure leaves the order unchanged too", func(t *testing.T) {
		c := env.newPushCustomer(t, "nolookup", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID
		before := orderState(t, order.ID)
		env.Mollie.SetFailLookup(true)
		t.Cleanup(func() { env.Mollie.SetFailLookup(false) })

		_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "CANCELLED"})

		cancelRefused(t, oerr)
		assert.Empty(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID), "no money moved")
		assert.Equal(t, before, orderState(t, order.ID))

		env.Mollie.SetFailLookup(false)
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		assert.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID), 1)
	})

	t.Run("a payment already cancelled at Mollie by an earlier attempt is not cancelled again", func(t *testing.T) {
		c := env.newPushCustomer(t, "twice", true)
		order := env.placeOnlineOrder(t, c)
		payID := order.Payment.MolliePaymentID
		// The earlier attempt cancelled it at Mollie, then failed before the order was saved.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, env.Mollie.Server.URL+"/v2/payments/"+payID, nil)
		require.NoError(t, err)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})

		assert.Equal(t, "CANCELLED", got.Status)
		assert.Len(t, env.Mollie.CallsMatching("DELETE /v2/payments/"+payID), 1, "only the earlier attempt's cancel; Mollie would refuse a second one")
	})

	t.Run("a payment paid at Mollie since our last webhook is refunded rather than cancelled", func(t *testing.T) {
		c := env.newPushCustomer(t, "latepay", true)
		order := env.placeOnlineOrder(t, c)
		payID := order.Payment.MolliePaymentID
		env.Mollie.MarkPaid(payID) // our row still says open: the webhook has not arrived yet

		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})

		assert.Empty(t, env.Mollie.CallsMatching("DELETE /v2/payments/"+payID))
		assert.Equal(t, []string{env.paymentCol(t, "amount", payID)}, env.Mollie.RefundAmounts(t, payID))
	})

	// The one transition rule: a CANCELLED order whose payment was given back (even partly) or can
	// no longer be paid cannot be reopened, or the kitchen would prepare food the customer got their
	// money back for. Every other transition stays free (see TestUpdateOrderOtherTransitionsStayFree).
	t.Run("a cancelled and refunded order cannot be moved back", func(t *testing.T) {
		c := env.newPushCustomer(t, "revive", true)
		order := env.placeOnlineOrder(t, c)
		env.markPaid(t, order.ID)
		payID := order.Payment.MolliePaymentID
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		require.Len(t, env.Mollie.CallsMatching("POST /v2/payments/"+payID+"/refunds"), 1)

		for _, status := range []string{"CONFIRMED", "PENDING", "PREPARING", "FAILED"} {
			_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": status})
			require.NotNil(t, oerr, status)
			assert.Equal(t, "USER_ERROR", oerr.Extensions["code"], status)
			assert.Contains(t, oerr.Message, "cannot be reopened")
		}
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM orders WHERE id = $1 AND order_status = 'CANCELLED'`, order.ID))
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM mollie_payments WHERE mollie_payment_id = $1 AND amount_refunded = amount`, payID),
			"and the payment stays refunded")

		// Saving it again as CANCELLED, or only touching its ready time, is not a reopening.
		assert.Equal(t, "CANCELLED", env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"}).Status)
		assert.Equal(t, "CANCELLED", env.mustUpdateOrder(t, order.ID, map[string]any{"estimatedReadyTime": inMinutes(30)}).Status)
	})

	t.Run("a cancelled order whose open payment was cancelled cannot be moved back either", func(t *testing.T) {
		c := env.newPushCustomer(t, "revive-open", true)
		order := env.placeOnlineOrder(t, c)
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		require.Equal(t, "canceled", env.paymentCol(t, "status", order.Payment.MolliePaymentID))

		_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "PENDING"})

		require.NotNil(t, oerr)
		assert.Equal(t, "USER_ERROR", oerr.Extensions["code"])
		assert.Equal(t, 1, countRows(t, env.TestContext, `SELECT count(*) FROM orders WHERE id = $1 AND order_status = 'CANCELLED'`, order.ID))
	})

	t.Run("a payment that expired or failed can not be paid any more: no reopening", func(t *testing.T) {
		for _, status := range []string{"expired", "failed"} {
			c := env.newPushCustomer(t, "revive-"+status, true)
			order := env.placeOnlineOrder(t, c)
			env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
			_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE mollie_payments SET status = $2 WHERE order_id = $1`, order.ID, status)
			require.NoError(t, err)

			_, oerr := env.updateOrderAs(t, order.ID, map[string]any{"status": "CONFIRMED"})

			require.NotNil(t, oerr, status)
			assert.Equal(t, "USER_ERROR", oerr.Extensions["code"], status)
		}
	})

	t.Run("a cancelled cash order has nothing to protect and may be reopened", func(t *testing.T) {
		c := env.newPushCustomer(t, "revive-cash", true)
		order := env.placeOrder(t, c, "en")
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "OTHER"})

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED"})

		assert.Equal(t, "CONFIRMED", got.Status)
	})

	t.Run("a cancelled order whose open payment could not be cancelled may be reopened", func(t *testing.T) {
		// The customer is in the middle of paying: Mollie no longer lets the payment be cancelled, so
		// it stays open (payable) and nothing was given back.
		c := env.newPushCustomer(t, "revive-locked", true)
		order := env.placeOnlineOrder(t, c)
		env.Mollie.LockPayment(order.Payment.MolliePaymentID)
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED"})
		require.Equal(t, "open", env.paymentCol(t, "status", order.Payment.MolliePaymentID))

		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "PENDING"})

		assert.Equal(t, "PENDING", got.Status)
	})

	t.Run("a cash order has no payment to settle", func(t *testing.T) {
		c := env.newPushCustomer(t, "cash", true)
		order := env.placeOrder(t, c, "en")
		before := len(env.Mollie.CallsMatching(""))
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CANCELLED", "cancellationReason": "OTHER"})
		env.Mail.WaitSubject(t, c.email, "Order canceled")
		assert.Equal(t, before, len(env.Mollie.CallsMatching("")), "Mollie is not involved")
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
		assert.Equal(t, "NOT_FOUND", resp.Errors[0].Extensions["code"])
		assert.Equal(t, "this order has no payment", resp.Errors[0].Message)
	})

	t.Run("a customer may not touch payments", func(t *testing.T) {
		resp := gqlAs(t, env.TestContext, c.token, "fr", m, map[string]any{"o": order.ID, "s": "paid"})
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "FORBIDDEN", resp.Errors[0].Extensions["code"])
		var status string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &status, `SELECT status FROM mollie_payments WHERE order_id = $1`, order.ID))
		assert.Equal(t, "paid", status)
	})

	// The column is free text and the refund / webhook logic only recognises Mollie's statuses, so a
	// typo ("payed") must not be stored.
	t.Run("only a known status is accepted", func(t *testing.T) {
		for _, bad := range []string{"payed", "PAID", "", " paid"} {
			resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "fr", m, map[string]any{"o": order.ID, "s": bad})
			require.Len(t, resp.Errors, 1, "%q", bad)
			assert.Equal(t, "USER_ERROR", resp.Errors[0].Extensions["code"], "%q", bad)
			assert.Contains(t, resp.Errors[0].Message, "unknown payment status")
			assert.Contains(t, resp.Errors[0].Message, "paid", "the message lists what is accepted")
			assert.Equal(t, "paid", env.paymentCol(t, "status", order.Payment.MolliePaymentID), "%q must not be stored", bad)
		}
		for _, ok := range []string{"open", "canceled", "pending", "authorized", "expired", "failed", "paid"} {
			resp := gqlAs(t, env.TestContext, adminToken(t, env.TestContext), "fr", m, map[string]any{"o": order.ID, "s": ok})
			require.Empty(t, resp.Errors, "%s: %+v", ok, resp.Errors)
			assert.Equal(t, ok, env.paymentCol(t, "status", order.Payment.MolliePaymentID))
		}
	})
}

// NOTE(product decision pending): apart from "a CANCELLED order whose payment was settled stays
// cancelled" there is no order state machine, on purpose: staff correct mistakes by moving orders
// back and forth (CONFIRMED back to PENDING, a delivered order back to preparing, a FAILED one
// re-confirmed), and the notification side effects are driven by the status that results. These
// transitions are accepted today; pinned here so that tightening them is a conscious decision.
func TestUpdateOrderOtherTransitionsStayFree(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	c := env.newPushCustomer(t, "free", false)

	for _, tc := range []struct{ from, to string }{
		{"CONFIRMED", "PENDING"},
		{"DELIVERED", "PREPARING"},
		{"DELIVERED", "PENDING"},
		{"AWAITING_PICK_UP", "CONFIRMED"},
		{"FAILED", "CONFIRMED"},
		{"PENDING", "DELIVERED"},
	} {
		t.Run(tc.from+" to "+tc.to, func(t *testing.T) {
			id := env.seedOrderRow(t, c.id, tc.from, "PICKUP", "en")

			got := env.mustUpdateOrder(t, id, map[string]any{"status": tc.to})

			assert.Equal(t, tc.to, got.Status)
		})
	}
}

// When the payment of an order cannot even be looked up, the order is not touched: cancelling
// without knowing whether it must be refunded would lose the customer's money, and reopening
// without knowing whether it was refunded could revive a refunded order.
func TestUpdateOrderPaymentLookupFaults(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	logs := captureLogs(t)
	c := env.newPushCustomer(t, "lookup", false)
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "fr")
	status := func(id uuid.UUID) string {
		var s string
		require.NoError(t, env.DB.DB.GetContext(t.Context(), &s, `SELECT order_status FROM orders WHERE id = $1`, id))
		return s
	}

	t.Run("cancelling with a failing payment lookup is refused and leaves the order alone", func(t *testing.T) {
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "PICKUP", "en")
		r := env.with(func(r *resolver.Resolver) {
			r.PaymentService = faultyPayments{PaymentService: env.Resolver.PaymentService, lookupErr: errBoom}
		})
		cancelled := orderDomain.OrderStatusCanceled

		_, err := r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: &cancelled})

		appErr, ok := apperr.From(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, apperr.CodePaymentSettlementFailed, appErr.Code)
		assert.Equal(t, "CONFIRMED", status(id))
		waitLog(t, logs, "cannot cancel the order: payment lookup failed")
	})

	t.Run("an order the payment service reports no payment for is cancelled as a cash order", func(t *testing.T) {
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "PICKUP", "en")
		r := env.with(func(r *resolver.Resolver) {
			r.PaymentService = faultyPayments{PaymentService: env.Resolver.PaymentService, noPayment: true}
		})
		cancelled := orderDomain.OrderStatusCanceled

		_, err := r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: &cancelled})

		require.NoError(t, err)
		assert.Equal(t, "CANCELLED", status(id))
		// ... and reopened: there is no payment that was given back.
		confirmed := orderDomain.OrderStatusConfirmed
		_, err = r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: &confirmed})
		require.NoError(t, err)
		assert.Equal(t, "CONFIRMED", status(id))
	})

	t.Run("reopening with a failing payment lookup is refused and leaves the order cancelled", func(t *testing.T) {
		id := env.seedOrderRow(t, c.id, "CANCELLED", "PICKUP", "en")
		r := env.with(func(r *resolver.Resolver) {
			r.PaymentService = faultyPayments{PaymentService: env.Resolver.PaymentService, lookupErr: errBoom}
		})
		confirmed := orderDomain.OrderStatusConfirmed

		_, err := r.Mutation().UpdateOrder(ctx, id, model.UpdateOrderInput{Status: &confirmed})

		require.ErrorIs(t, err, errBoom)
		_, typed := apperr.From(err)
		assert.False(t, typed, "an internal error, not a message that blames the staff member")
		assert.Equal(t, "CANCELLED", status(id))
	})
}
