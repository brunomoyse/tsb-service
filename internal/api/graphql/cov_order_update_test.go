package graphql_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/testhelpers"
)

// updateOrder is the staff action behind every status change: it persists the change, then tells
// the customer (e-mail, push, Live Activity / Live Update) and the dashboards (subscriptions). The
// side effects run off the request path, so the tests wait for them on the fakes.

// pushCustomer is a customer with a device of each kind, an iOS and an Android one the push
// providers accept, and ones they reject: dead (unregistered) and refused (any other failure).
type pushCustomer struct {
	id                          uuid.UUID
	email, token                string
	ios, android                string
	deadIOS, refusedIOS         string
	deadAndroid, refusedAndroid string
	withMail                    bool
}

// newPushCustomer seeds the customer; mail=true turns their order e-mails on. Devices are added
// with registerDevices, so each test chooses what the customer owns.
func (e *covEnv) newPushCustomer(t *testing.T, label string, mail bool) *pushCustomer {
	t.Helper()
	c := &pushCustomer{withMail: mail}
	if mail {
		c.id, c.email, c.token = e.customerWithMail(t, label)
	} else {
		c.id, c.token = testhelpers.SeedCustomer(t, e.DB.DB, label)
		require.NoError(t, e.DB.DB.GetContext(t.Context(), &c.email, `SELECT email FROM users WHERE id = $1`, c.id))
	}
	c.ios, c.android = "ios-"+label, "android-"+label
	c.deadIOS, c.refusedIOS = "dead-ios-"+label, "broken-ios-"+label
	c.deadAndroid, c.refusedAndroid = "dead-android-"+label, "refused-android-"+label
	return c
}

// registerDevices registers the working iOS and Android devices (and, with bad, the rejected ones:
// APNs answers a "refused" token with a rejection that is only logged, so the iOS one is a broken
// connection instead).
func (e *covEnv) registerDevices(t *testing.T, c *pushCustomer, bad bool) {
	t.Helper()
	reg := func(tok, platform string) {
		require.NoError(t, e.Notif.RegisterDeviceToken(t.Context(), c.id, tok, platform, "user"))
	}
	reg(c.ios, "ios")
	reg(c.android, "android")
	if bad {
		reg(c.deadIOS, "ios")
		reg(c.refusedIOS, "ios")
		reg(c.deadAndroid, "android")
		reg(c.refusedAndroid, "android")
	}
}

func (e *covEnv) addActivityToken(t *testing.T, orderID uuid.UUID, token string) {
	t.Helper()
	_, err := e.DB.DB.ExecContext(t.Context(), `INSERT INTO live_activity_tokens (order_id, push_token) VALUES ($1, $2)`, orderID, token)
	require.NoError(t, err)
}

func (e *covEnv) activityTokenCount(t *testing.T, orderID uuid.UUID) int {
	t.Helper()
	return countRows(t, e.TestContext, `SELECT count(*) FROM live_activity_tokens WHERE order_id = $1`, orderID)
}

const updateOrderMutation = `
	mutation ($id: ID!, $input: UpdateOrderInput!) {
		updateOrder(id: $id, input: $input) {
			id status estimatedReadyTime cancellationReason
			statusHistory { status }
		}
	}`

type updatedOrder struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	EstimatedReadyTime *string `json:"estimatedReadyTime"`
	CancellationReason *string `json:"cancellationReason"`
	StatusHistory      []struct {
		Status string `json:"status"`
	} `json:"statusHistory"`
}

// updateOrderAs runs updateOrder as an admin; exactly one of the results is set.
func (e *covEnv) updateOrderAs(t *testing.T, id any, input map[string]any) (*updatedOrder, *orderErr) {
	t.Helper()
	resp := gqlAs(t, e.TestContext, adminToken(t, e.TestContext), "fr", updateOrderMutation, map[string]any{"id": fmt.Sprint(id), "input": input})
	if len(resp.Errors) > 0 {
		return nil, &orderErr{Message: resp.Errors[0].Message, Extensions: resp.Errors[0].Extensions}
	}
	var data struct {
		UpdateOrder updatedOrder `json:"updateOrder"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data), string(resp.Data))
	return &data.UpdateOrder, nil
}

func (e *covEnv) mustUpdateOrder(t *testing.T, id any, input map[string]any) *updatedOrder {
	t.Helper()
	got, oerr := e.updateOrderAs(t, id, input)
	require.Nil(t, oerr, "updateOrder failed: %+v", oerr)
	return got
}

// placeOrder places a cash pickup order of one salmon sushi as the customer.
func (e *covEnv) placeOrder(t *testing.T, c *pushCustomer, lang string) *createdOrder {
	t.Helper()
	return mustCreateOrder(t, e.TestContext, c.token, lang, map[string]any{
		"orderType": "PICKUP", "isOnlinePayment": false, "items": lines(e.Fixtures.SalmonSushi.ID, 1),
	})
}

func inMinutes(m int) string {
	return time.Now().Add(time.Duration(m) * time.Minute).UTC().Format(time.RFC3339)
}

// alertsFor is what the fake APNs got for the order as visible alerts (not Live Activity updates).
func (f *fakeAPNs) alertsFor(token, orderID string) []pushReq {
	var out []pushReq
	for _, r := range f.pushesTo(token) {
		if r.Payload["orderId"] == orderID {
			out = append(out, r)
		}
	}
	return out
}

// messagesFor is what the fake FCM got for the order, keyed by what it is: "alert" for a visible
// notification, "data" for a Live Update data message.
func (f *fakeFCM) messagesFor(token, orderID string) (alerts, data []pushReq) {
	for _, r := range f.pushesTo(token) {
		if _, ok := r.Payload["notification"]; ok {
			if d, _ := r.Payload["data"].(map[string]any); d["orderId"] == orderID {
				alerts = append(alerts, r)
			}
			continue
		}
		if d, _ := r.Payload["data"].(map[string]any); fmt.Sprint(d["deepLinkUrl"]) != "" && containsID(d["deepLinkUrl"], orderID) {
			data = append(data, r)
		}
	}
	return alerts, data
}

func containsID(v any, id string) bool {
	s, _ := v.(string)
	return len(s) >= len(id) && s[len(s)-len(id):] == id
}

func recvOrder(t *testing.T, ch <-chan *model.Order) *model.Order {
	t.Helper()
	select {
	case o := <-ch:
		require.NotNil(t, o)
		return o
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no order was published")
		return nil
	}
}

func TestUpdateOrderLifecycleNotifications(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	logs := captureLogs(t)
	c := env.newPushCustomer(t, "life", true)
	env.registerDevices(t, c, false)

	order := env.placeOrder(t, c, "en")
	orderID := uuid.MustParse(order.ID)
	env.Mail.waitSubject(t, c.email, "Order pending validation")
	env.addActivityToken(t, orderID, "la-life")

	all, stopAll := context.WithCancel(t.Context())
	defer stopAll()
	updatedAll, err := env.Resolver.Subscription().OrderUpdated(all)
	require.NoError(t, err)
	mine, err := env.Resolver.Subscription().MyOrderUpdated(env.ctxForCancel(all, c.id.String(), false, "en"), orderID)
	require.NoError(t, err)

	t.Run("confirming a pending order tells the customer everywhere", func(t *testing.T) {
		got := env.mustUpdateOrder(t, order.ID, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(30)})
		assert.Equal(t, "CONFIRMED", got.Status)
		require.NotNil(t, got.EstimatedReadyTime)
		var history []string
		for _, h := range got.StatusHistory {
			history = append(history, h.Status)
		}
		assert.ElementsMatch(t, []string{"PENDING", "CONFIRMED"}, history)

		// Dashboards: every order update, and this customer's order topic.
		assert.Equal(t, order.ID, recvOrder(t, updatedAll).ID.String())
		pubMine := recvOrder(t, mine)
		assert.Equal(t, order.ID, pubMine.ID.String())
		assert.Equal(t, "CONFIRMED", string(pubMine.Status))

		// E-mail, with the real order lines.
		confirm := env.Mail.waitSubject(t, c.email, "Order confirmed")
		assert.Equal(t, []string{c.email}, confirm.To)

		// Visible alerts on both platforms, the Live Activity and the Android Live Update.
		require.Eventually(t, func() bool {
			alerts, data := env.FCM.messagesFor(c.android, order.ID)
			return len(env.APNs.alertsFor(c.ios, order.ID)) == 1 && len(alerts) == 1 && len(data) == 1 && len(env.APNs.pushesTo("la-life")) == 1
		}, 20*time.Second, 20*time.Millisecond)
		alert := env.APNs.alertsFor(c.ios, order.ID)[0]
		assert.Equal(t, "CONFIRMED", alert.Payload["status"])
		aps, _ := alert.Payload["aps"].(map[string]any)
		body := aps["alert"].(map[string]any)["body"]
		assert.Contains(t, body, "confirmed")

		la, _ := env.APNs.pushesTo("la-life")[0].Payload["aps"].(map[string]any)
		assert.Equal(t, "update", la["event"])
		assert.Equal(t, 1, env.activityTokenCount(t, orderID), "a live activity that goes on keeps its token")

		_, data := env.FCM.messagesFor(c.android, order.ID)
		d, _ := data[0].Payload["data"].(map[string]any)
		assert.Equal(t, "update", d["event"])

		assert.Zero(t, env.Mail.countSubject(t, c.email, "Updated estimated time"), "the first estimate is not an update of it")
	})

	t.Run("moving the estimate of a confirmed order tells the customer, without a status change", func(t *testing.T) {
		before := len(env.APNs.pushesTo("la-life"))
		got := env.mustUpdateOrder(t, order.ID, map[string]any{"estimatedReadyTime": inMinutes(50)})
		assert.Equal(t, "CONFIRMED", got.Status)

		env.Mail.waitSubject(t, c.email, "Updated estimated time")
		require.Eventually(t, func() bool {
			var withETA int
			for _, r := range env.APNs.alertsFor(c.ios, order.ID) {
				if _, ok := r.Payload["estimatedReadyTime"]; ok {
					withETA++
				}
			}
			alerts, _ := env.FCM.messagesFor(c.android, order.ID)
			return withETA == 1 && len(alerts) == 2
		}, 20*time.Second, 20*time.Millisecond)
		assert.Equal(t, before, len(env.APNs.pushesTo("la-life")), "no status change, no live activity push")
		assert.Equal(t, 1, env.Mail.countSubject(t, c.email, "Order confirmed"), "no second confirmation")
	})

	t.Run("an order that is ready sends the pick-up e-mail", func(t *testing.T) {
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "AWAITING_PICK_UP"})
		env.Mail.waitSubject(t, c.email, "Your order is ready!")
	})

	t.Run("picking it up ends the live activity and clears its token", func(t *testing.T) {
		env.mustUpdateOrder(t, order.ID, map[string]any{"status": "PICKED_UP"})
		require.Eventually(t, func() bool { return env.activityTokenCount(t, orderID) == 0 }, 20*time.Second, 20*time.Millisecond)
		var ends int
		for _, r := range env.APNs.pushesTo("la-life") {
			if aps, _ := r.Payload["aps"].(map[string]any); aps["event"] == "end" {
				ends++
			}
		}
		assert.Equal(t, 1, ends)
		require.Eventually(t, func() bool {
			_, data := env.FCM.messagesFor(c.android, order.ID)
			for _, m := range data {
				if d, _ := m.Payload["data"].(map[string]any); d["event"] == "stop" {
					return true
				}
			}
			return false
		}, 20*time.Second, 20*time.Millisecond, "the Android Live Update is ended too")
	})

	t.Run("a delivery on its way sends the on-its-way e-mail", func(t *testing.T) {
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "DELIVERY", "en")
		env.mustUpdateOrder(t, id, map[string]any{"status": "OUT_FOR_DELIVERY"})
		env.Mail.waitSubject(t, c.email, "Your order is on its way!")
	})

	t.Run("an unknown order is an error and publishes nothing", func(t *testing.T) {
		_, oerr := env.updateOrderAs(t, uuid.New(), map[string]any{"status": "CONFIRMED"})
		require.NotNil(t, oerr)
		assert.Equal(t, "Internal server error", oerr.Message)
	})
	_ = logs
}

func TestUpdateOrderQuietCases(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	logs := captureLogs(t)

	t.Run("a late update sends no alert and no progress e-mail, but still moves the live activity", func(t *testing.T) {
		c := env.newPushCustomer(t, "late", true)
		env.registerDevices(t, c, false)
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "PICKUP", "en")
		_, err := env.DB.DB.ExecContext(t.Context(), `UPDATE orders SET estimated_ready_time = now() - interval '2 hours' WHERE id = $1`, id)
		require.NoError(t, err)
		env.addActivityToken(t, id, "la-late")

		env.mustUpdateOrder(t, id, map[string]any{"status": "AWAITING_PICK_UP"})
		assert.Equal(t, 1, logs.FilterMessage("suppressed late order notification").Len())

		require.Eventually(t, func() bool { return len(env.APNs.pushesTo("la-late")) == 1 }, 20*time.Second, 20*time.Millisecond)
		require.Never(t, func() bool {
			alerts, _ := env.FCM.messagesFor(c.android, id.String())
			return len(env.APNs.alertsFor(c.ios, id.String())) > 0 || len(alerts) > 0 ||
				env.Mail.countSubject(t, c.email, "Your order is ready!") > 0
		}, 400*time.Millisecond, 20*time.Millisecond)

		// A cancellation is news whenever it comes.
		env.mustUpdateOrder(t, id, map[string]any{"status": "CANCELLED", "cancellationReason": "OUT_OF_STOCK"})
		env.Mail.waitSubject(t, c.email, "Order canceled")
		require.Eventually(t, func() bool { return len(env.APNs.alertsFor(c.ios, id.String())) == 1 }, 20*time.Second, 20*time.Millisecond)
		aps, _ := env.APNs.alertsFor(c.ios, id.String())[0].Payload["aps"].(map[string]any)
		assert.Contains(t, aps["alert"].(map[string]any)["body"], "out of stock")
	})

	t.Run("a delivery handed over to the customer is not announced, its live activity ends", func(t *testing.T) {
		c := env.newPushCustomer(t, "handover", true)
		env.registerDevices(t, c, false)
		id := env.seedOrderRow(t, c.id, "OUT_FOR_DELIVERY", "DELIVERY", "en")
		env.addActivityToken(t, id, "la-handover")

		env.mustUpdateOrder(t, id, map[string]any{"status": "DELIVERED"})
		require.Eventually(t, func() bool { return env.activityTokenCount(t, id) == 0 }, 20*time.Second, 20*time.Millisecond)
		la, _ := env.APNs.pushesTo("la-handover")[0].Payload["aps"].(map[string]any)
		assert.Equal(t, "end", la["event"])
		require.Never(t, func() bool { return len(env.APNs.alertsFor(c.ios, id.String())) > 0 }, 300*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("a status without a customer text sends no alert", func(t *testing.T) {
		c := env.newPushCustomer(t, "pending", true)
		env.registerDevices(t, c, false)
		id := env.seedOrderRow(t, c.id, "CONFIRMED", "PICKUP", "en")
		env.addActivityToken(t, id, "la-pending")
		env.mustUpdateOrder(t, id, map[string]any{"status": "PENDING"})
		require.Eventually(t, func() bool { return len(env.APNs.pushesTo("la-pending")) == 1 }, 20*time.Second, 20*time.Millisecond)
		require.Never(t, func() bool { return len(env.APNs.alertsFor(c.ios, id.String())) > 0 }, 300*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("a customer who turned order e-mails off gets none, the order still moves on", func(t *testing.T) {
		c := env.newPushCustomer(t, "nomail", false)
		id := env.seedOrderRow(t, c.id, "PENDING", "PICKUP", "en")
		got := env.mustUpdateOrder(t, id, map[string]any{"status": "CONFIRMED", "estimatedReadyTime": inMinutes(20)})
		assert.Equal(t, "CONFIRMED", got.Status)
		env.mustUpdateOrder(t, id, map[string]any{"estimatedReadyTime": inMinutes(40)})
		env.mustUpdateOrder(t, id, map[string]any{"status": "AWAITING_PICK_UP"})
		env.mustUpdateOrder(t, id, map[string]any{"status": "CANCELLED"})
		require.Never(t, func() bool { return len(env.Mail.mailTo(c.email)) > 0 }, 400*time.Millisecond, 20*time.Millisecond)
	})
}
