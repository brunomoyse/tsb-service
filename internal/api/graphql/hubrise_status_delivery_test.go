package graphql_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
)

// pushTitles are the titles of the visible alerts APNs got for the order, in arrival order.
func pushTitles(env *covEnv, token, orderID string) []string {
	var titles []string
	for _, r := range alertsFor(env.APNs, token, orderID) {
		aps, _ := r.Payload["aps"].(map[string]any)
		alert, _ := aps["alert"].(map[string]any)
		title, _ := alert["title"].(string)
		titles = append(titles, title)
	}
	return titles
}

// Every step the EPOS reports for a pickup order tells the customer through the real order service,
// like the same step in the dashboard: a push alert on both platforms and the e-mail where the
// dashboard sends one.
func TestHubrisePickupStatusEventsPushLikeTheDashboard(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	h := env.withHubriseSync(t)
	c := env.newPushCustomer(t, "hubrise-pickup-push", true)
	env.registerDevices(t, c, false)
	order := env.placeOrder(t, c, "en")
	id := uuid.MustParse(order.ID)
	remote := h.pushOrder(order.ID)
	h.srv.ClearRequests()

	wantIOS := func(titles ...string) {
		t.Helper()
		require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) == len(titles) },
			20*time.Second, 20*time.Millisecond, "iOS alerts: %v", pushTitles(env, c.ios, order.ID))
		assert.Equal(t, titles, pushTitles(env, c.ios, order.ID))
		require.Eventually(t, func() bool {
			alerts, _ := messagesFor(env.FCM, c.android, order.ID)
			return len(alerts) == len(titles)
		}, 20*time.Second, 20*time.Millisecond, "Android alerts")
	}

	h.epos(remote.ID, pkghubrise.StatusAccepted)
	wantIOS("Order confirmed")
	h.epos(remote.ID, pkghubrise.StatusInPreparation)
	assert.Equal(t, "PREPARING", orderStatusInDB(t, env, id))
	wantIOS("Order confirmed", "Preparing your order")
	h.epos(remote.ID, pkghubrise.StatusAwaitingCollection)
	assert.Equal(t, "AWAITING_PICK_UP", orderStatusInDB(t, env, id))
	env.Mail.WaitSubject(t, c.email, "Your order is ready!")
	wantIOS("Order confirmed", "Preparing your order", "Your order is ready!")
	h.epos(remote.ID, pkghubrise.StatusCompleted)
	assert.Equal(t, "PICKED_UP", orderStatusInDB(t, env, id))
	wantIOS("Order confirmed", "Preparing your order", "Your order is ready!", "Picked up")

	assert.Equal(t, []string{"PENDING", "CONFIRMED", "PREPARING", "AWAITING_PICK_UP", "PICKED_UP"}, env.historyOf(t, id))
	assert.Equal(t, 1, env.Mail.CountSubject(t, c.email, "Your order is ready!"))
	assert.Zero(t, h.patches(), "no echo back to HubRise")
}

// The delivery statuses of the EPOS go through the real order service too: in_delivery is the
// on-its-way e-mail and push, "ready for collection" means nothing for a delivery (no change, no
// "come and get it" mail), and completed delivers the order without announcing it (the driver hands
// it over) while the Live Activity ends.
func TestHubriseDeliveryStatusEventsNotifyLikeTheDashboard(t *testing.T) {
	env := setupCovEnv(t, covOptions{Push: true})
	h := env.withHubriseSync(t)
	c := env.newPushCustomer(t, "hubrise-delivery", true)
	env.registerDevices(t, c, false)
	testhelpers.SeedAddress(t, env.DB.DB, "hubrise-delivery-place", 2000, "4000")
	cat := testhelpers.SeedCategory(t, env.DB.DB, 71, map[string]string{"en": "Mains"})
	big := testhelpers.SeedProduct(t, env.DB.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "BIGD", Price: "30.00", Names: map[string]string{"en": "Big"}})
	order := mustCreateOrder(t, env.TestContext, c.token, "en", map[string]any{
		"orderType": "DELIVERY", "isOnlinePayment": false, "addressPlaceId": "hubrise-delivery-place", "items": lines(big, 1),
	})
	id := uuid.MustParse(order.ID)
	env.Mail.WaitSubject(t, c.email, "Order received, awaiting confirmation")
	remote := h.pushOrder(order.ID)
	h.srv.ClearRequests()
	env.addActivityToken(t, id, "la-hubrise-delivery")

	h.epos(remote.ID, pkghubrise.StatusAccepted)
	assert.Equal(t, "CONFIRMED", orderStatusInDB(t, env, id))
	env.Mail.WaitSubject(t, c.email, "Order confirmed")

	h.epos(remote.ID, pkghubrise.StatusAwaitingCollection)
	assert.Equal(t, "CONFIRMED", orderStatusInDB(t, env, id), "ready for collection changes nothing for a delivery")
	require.Never(t, func() bool { return env.Mail.CountSubject(t, c.email, "Your order is ready!") > 0 },
		300*time.Millisecond, 20*time.Millisecond)

	h.epos(remote.ID, pkghubrise.StatusInDelivery)
	assert.Equal(t, "OUT_FOR_DELIVERY", orderStatusInDB(t, env, id))
	env.Mail.WaitSubject(t, c.email, "Your order is on its way!")
	require.Eventually(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) == 2 }, 20*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"Order confirmed", "Your order is on its way!"}, pushTitles(env, c.ios, order.ID))
	require.Eventually(t, func() bool {
		alerts, _ := messagesFor(env.FCM, c.android, order.ID)
		return len(alerts) == 2
	}, 20*time.Second, 20*time.Millisecond, "the Android alert")
	require.Eventually(t, func() bool { return len(env.APNs.PushesTo("la-hubrise-delivery")) == 2 }, 20*time.Second, 20*time.Millisecond,
		"the Live Activity moved on confirmed, then to out for delivery")
	assert.Equal(t, 1, env.activityTokenCount(t, id), "not over yet")

	h.epos(remote.ID, pkghubrise.StatusCompleted)
	assert.Equal(t, "DELIVERED", orderStatusInDB(t, env, id))
	require.Eventually(t, func() bool { return env.activityTokenCount(t, id) == 0 }, 20*time.Second, 20*time.Millisecond,
		"the Live Activity ends on the terminal status")
	la, _ := env.APNs.PushesTo("la-hubrise-delivery")[2].Payload["aps"].(map[string]any)
	assert.Equal(t, "end", la["event"])
	require.Never(t, func() bool { return len(alertsFor(env.APNs, c.ios, order.ID)) > 2 }, 300*time.Millisecond, 20*time.Millisecond,
		"a delivery handed over is not announced")
	assert.Equal(t, 1, env.Mail.CountSubject(t, c.email, "Your order is on its way!"))

	assert.Equal(t, []string{"PENDING", "CONFIRMED", "OUT_FOR_DELIVERY", "DELIVERED"}, env.historyOf(t, id))
	assert.Zero(t, h.patches(), "no echo back to HubRise")
}
