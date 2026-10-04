package application

import (
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/brand"
	"tsb-service/pkg/i18n/locale"
)

func reason(r orderDomain.OrderCancellationReason) *orderDomain.OrderCancellationReason { return &r }

func TestGetOrderStatusNotification(t *testing.T) {
	t.Run("every status has a non-empty title and body in every language", func(t *testing.T) {
		statuses := []orderDomain.OrderStatus{
			orderDomain.OrderStatusConfirmed, orderDomain.OrderStatusPreparing, orderDomain.OrderStatusAwaitingUp,
			orderDomain.OrderStatusOutForDelivery, orderDomain.OrderStatusDelivered, orderDomain.OrderStatusPickedUp,
			orderDomain.OrderStatusCanceled, orderDomain.OrderStatusFailed,
		}
		for _, lang := range []string{"fr", "en", "nl", "zh"} {
			for _, s := range statuses {
				msg := GetOrderStatusNotification(s, lang, "DELIVERY", nil)
				require.NotEmpty(t, msg.Title, "%s/%s title", lang, s)
				require.NotEmpty(t, msg.Body, "%s/%s body", lang, s)
			}
		}
	})

	t.Run("localized wording", func(t *testing.T) {
		require.Equal(t, "Order confirmed", GetOrderStatusNotification(orderDomain.OrderStatusConfirmed, "en", "DELIVERY", nil).Title)
		require.Equal(t, "Bestelling bevestigd", GetOrderStatusNotification(orderDomain.OrderStatusConfirmed, "nl", "DELIVERY", nil).Title)
		require.Equal(t, "订单已确认", GetOrderStatusNotification(orderDomain.OrderStatusConfirmed, "zh", "DELIVERY", nil).Title)
		require.Equal(t, "Commande confirmée", GetOrderStatusNotification(orderDomain.OrderStatusConfirmed, "fr", "DELIVERY", nil).Title)
	})

	t.Run("unknown language falls back to French", func(t *testing.T) {
		msg := GetOrderStatusNotification(orderDomain.OrderStatusPreparing, "de", "DELIVERY", nil)
		require.Equal(t, "En préparation", msg.Title)
	})

	t.Run("language is normalised: case, region tags, unsupported values", func(t *testing.T) {
		for in, want := range pushLanguageVariants {
			for _, orderType := range []string{"DELIVERY", "PICKUP"} {
				for _, s := range []orderDomain.OrderStatus{orderDomain.OrderStatusConfirmed, orderDomain.OrderStatusAwaitingUp, orderDomain.OrderStatusCanceled} {
					require.Equal(t,
						GetOrderStatusNotification(s, want, orderType, nil),
						GetOrderStatusNotification(s, in, orderType, nil),
						"%q must read as %q (%s/%s)", in, want, s, orderType)
				}
			}
		}
	})

	t.Run("pickup wording also applies when the language falls back to French", func(t *testing.T) {
		msg := GetOrderStatusNotification(orderDomain.OrderStatusAwaitingUp, "de", "PICKUP", nil)
		require.Equal(t, "Venez la retirer au comptoir.", msg.Body)
		msg = GetOrderStatusNotification(orderDomain.OrderStatusAwaitingUp, "", "PICKUP", nil)
		require.Equal(t, "Venez la retirer au comptoir.", msg.Body)
	})

	t.Run("cancellation reason follows the normalised language", func(t *testing.T) {
		r := orderDomain.OrderCancellationReasonOutOfStock
		require.Equal(t, "Your order has been cancelled: out of stock.", GetOrderStatusNotification(orderDomain.OrderStatusCanceled, "EN-us", "DELIVERY", &r).Body)
		require.Equal(t, "Votre commande a été annulée : rupture de stock.", GetOrderStatusNotification(orderDomain.OrderStatusCanceled, "", "DELIVERY", &r).Body)
	})

	t.Run("status without a message yields the brand name and an empty body", func(t *testing.T) {
		msg := GetOrderStatusNotification(orderDomain.OrderStatusPending, "en", "DELIVERY", nil)
		require.Equal(t, brand.Current().Name, msg.Title)
		require.Empty(t, msg.Body)
	})

	t.Run("pickup orders get the counter wording when ready", func(t *testing.T) {
		pickup := GetOrderStatusNotification(orderDomain.OrderStatusAwaitingUp, "en", "PICKUP", nil)
		delivery := GetOrderStatusNotification(orderDomain.OrderStatusAwaitingUp, "en", "DELIVERY", nil)
		require.Equal(t, "Come pick it up at the counter.", pickup.Body)
		require.Equal(t, "It's ready and waiting.", delivery.Body)
		require.Equal(t, pickup.Title, delivery.Title)
	})

	t.Run("pickup without an override keeps the generic text", func(t *testing.T) {
		msg := GetOrderStatusNotification(orderDomain.OrderStatusPreparing, "fr", "PICKUP", nil)
		require.Equal(t, "Votre commande est en cours de préparation.", msg.Body)
	})

	t.Run("cancellation reason is appended in the customer's language", func(t *testing.T) {
		cases := []struct {
			lang   string
			reason orderDomain.OrderCancellationReason
			want   string
		}{
			{"fr", orderDomain.OrderCancellationReasonOutOfStock, "Votre commande a été annulée : rupture de stock."},
			{"en", orderDomain.OrderCancellationReasonKitchenClosed, "Your order has been cancelled: kitchen closed."},
			{"nl", orderDomain.OrderCancellationReasonDeliveryArea, "Uw bestelling is geannuleerd: buiten de leveringszone."},
			{"zh", orderDomain.OrderCancellationReasonOutOfStock, "您的订单已被取消：缺货。"},
		}
		for _, c := range cases {
			msg := GetOrderStatusNotification(orderDomain.OrderStatusCanceled, c.lang, "DELIVERY", reason(c.reason))
			require.Equal(t, c.want, msg.Body, c.lang)
		}
	})

	t.Run("cancellation reason in an unsupported language uses French labels", func(t *testing.T) {
		// Texts fall back to fr as well, so the whole message is consistent.
		msg := GetOrderStatusNotification(orderDomain.OrderStatusCanceled, "de", "DELIVERY", reason(orderDomain.OrderCancellationReasonOutOfStock))
		require.Equal(t, "Votre commande a été annulée : rupture de stock.", msg.Body)
	})

	t.Run("reason OTHER, nil or unknown keeps the generic body", func(t *testing.T) {
		generic := "Your order has been cancelled."
		require.Equal(t, generic, GetOrderStatusNotification(orderDomain.OrderStatusCanceled, "en", "DELIVERY", reason(orderDomain.OrderCancellationReasonOther)).Body)
		require.Equal(t, generic, GetOrderStatusNotification(orderDomain.OrderStatusCanceled, "en", "DELIVERY", nil).Body)
		require.Equal(t, generic, GetOrderStatusNotification(orderDomain.OrderStatusCanceled, "en", "DELIVERY", reason("SOMETHING_NEW")).Body)
	})

	t.Run("reason is ignored for non-cancelled statuses", func(t *testing.T) {
		msg := GetOrderStatusNotification(orderDomain.OrderStatusConfirmed, "en", "DELIVERY", reason(orderDomain.OrderCancellationReasonOutOfStock))
		require.Equal(t, "Your order has been confirmed by the restaurant.", msg.Body)
	})
}

func TestGetReadyTimeUpdatedNotification(t *testing.T) {
	t.Run("nil time yields brand name and empty body", func(t *testing.T) {
		msg := GetReadyTimeUpdatedNotification("en", nil)
		require.Equal(t, brand.Current().Name, msg.Title)
		require.Empty(t, msg.Body)
	})

	// 2026-07-01 is summer time in Brussels (UTC+2).
	summer := time.Date(2026, 7, 1, 17, 5, 0, 0, time.UTC) // 19:05 local
	t.Run("24h clock in the restaurant timezone for non-English", func(t *testing.T) {
		require.Equal(t, "Nouvelle heure estimée : 19:05.", GetReadyTimeUpdatedNotification("fr", &summer).Body)
		require.Equal(t, "Nieuwe geschatte tijd: 19:05.", GetReadyTimeUpdatedNotification("nl", &summer).Body)
		require.Equal(t, "新的预计时间：19:05。", GetReadyTimeUpdatedNotification("zh", &summer).Body)
		require.Equal(t, "Heure estimée mise à jour", GetReadyTimeUpdatedNotification("fr", &summer).Title)
	})

	t.Run("unknown language falls back to French", func(t *testing.T) {
		require.Equal(t, "Nouvelle heure estimée : 19:05.", GetReadyTimeUpdatedNotification("de", &summer).Body)
	})

	t.Run("language is normalised: case, region tags, unsupported values", func(t *testing.T) {
		for in, want := range pushLanguageVariants {
			require.Equal(t, GetReadyTimeUpdatedNotification(want, &summer), GetReadyTimeUpdatedNotification(in, &summer), "%q must read as %q", in, want)
		}
		require.Equal(t, "New estimated time: 7:05 PM.", GetReadyTimeUpdatedNotification("EN-GB", &summer).Body, "12h clock follows the normalised language")
	})

	t.Run("English uses a 12h clock", func(t *testing.T) {
		cases := []struct {
			utc  time.Time
			want string
		}{
			{time.Date(2026, 7, 1, 17, 5, 0, 0, time.UTC), "7:05 PM"},   // 19:05 local
			{time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC), "12:00 PM"},  // noon local
			{time.Date(2026, 7, 1, 22, 30, 0, 0, time.UTC), "12:30 AM"}, // 00:30 local
			{time.Date(2026, 7, 1, 7, 45, 0, 0, time.UTC), "9:45 AM"},   // 09:45 local
			{time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC), "12:00 PM"}, // winter UTC+1
		}
		for _, c := range cases {
			msg := GetReadyTimeUpdatedNotification("en", &c.utc)
			require.Equal(t, "New estimated time: "+c.want+".", msg.Body)
		}
		require.Equal(t, "Estimated time updated", GetReadyTimeUpdatedNotification("en", &summer).Title)
	})
}

// pushLanguageVariants maps raw language values to the language push texts must use.
var pushLanguageVariants = map[string]string{
	"EN": "en", " fr ": "fr", "NL": "nl", "Zh": "zh",
	"fr-BE": "fr", "en-US": "en", "en_GB": "en", "nl-BE": "nl", "zh-CN": "zh", "zh-Hans": "zh",
	"de": "fr", "de-DE": "fr", "": "fr", "  ": "fr", "xx": "fr", "english": "fr", "%%": "fr",
}

func TestGetNewOrderNotification(t *testing.T) {
	require.Equal(t, notificationText{Title: "Nouvelle commande", Body: "En attente de confirmation"}, GetNewOrderNotification("fr", "DELIVERY", "12.50"))
	require.Equal(t, notificationText{Title: "New order", Body: "Awaiting confirmation"}, GetNewOrderNotification("en", "PICKUP", "1.00"))
	require.Equal(t, notificationText{Title: "Nieuwe bestelling", Body: "Wacht op bevestiging"}, GetNewOrderNotification("nl", "", ""))
	require.Equal(t, notificationText{Title: "新订单", Body: "等待确认"}, GetNewOrderNotification("zh", "", ""))
	t.Run("unknown language falls back to French and never leaks the amount", func(t *testing.T) {
		msg := GetNewOrderNotification("de", "DELIVERY", "99.99")
		require.Equal(t, "Nouvelle commande", msg.Title)
		require.NotContains(t, msg.Body, "99.99")
	})
	t.Run("language is normalised: case, region tags, unsupported values", func(t *testing.T) {
		for in, want := range pushLanguageVariants {
			require.Equal(t, GetNewOrderNotification(want, "", ""), GetNewOrderNotification(in, "", ""), "%q must read as %q", in, want)
		}
	})
	t.Run("returned value is a copy", func(t *testing.T) {
		m := GetNewOrderNotification("en", "", "")
		m.Title = "mutated"
		require.Equal(t, "New order", GetNewOrderNotification("en", "", "").Title)
	})
}

func TestLiveActivityProgress(t *testing.T) {
	cases := []struct {
		status    orderDomain.OrderStatus
		orderType string
		want      float64
	}{
		{orderDomain.OrderStatusPending, "DELIVERY", 0},
		{orderDomain.OrderStatusConfirmed, "DELIVERY", 0.25},
		{orderDomain.OrderStatusPreparing, "DELIVERY", 0.5},
		{orderDomain.OrderStatusOutForDelivery, "DELIVERY", 0.75},
		{orderDomain.OrderStatusDelivered, "DELIVERY", 1},
		{orderDomain.OrderStatusAwaitingUp, "PICKUP", 0.75},
		{orderDomain.OrderStatusPickedUp, "PICKUP", 1},
		// A status absent from the flow (ready-for-pickup on a delivery order,
		// or delivery statuses on a pickup order) maps to 0.
		{orderDomain.OrderStatusAwaitingUp, "DELIVERY", 0},
		{orderDomain.OrderStatusOutForDelivery, "PICKUP", 0},
		{orderDomain.OrderStatusCanceled, "DELIVERY", 0},
	}
	for _, c := range cases {
		require.InDelta(t, c.want, liveActivityProgress(c.status, c.orderType), 1e-9, "%s/%s", c.orderType, c.status)
	}
}

func TestGetLiveActivityContentState(t *testing.T) {
	cs := GetLiveActivityContentState(orderDomain.OrderStatusPreparing, "en", "DELIVERY", nil)
	require.Equal(t, map[string]any{
		"title":    "Preparing your order",
		"subtitle": "The kitchen is preparing your order.",
		"progress": 0.5,
	}, cs)

	cs = GetLiveActivityContentState(orderDomain.OrderStatusCanceled, "fr", "PICKUP", reason(orderDomain.OrderCancellationReasonKitchenClosed))
	require.Equal(t, "Commande annulée", cs["title"])
	require.Equal(t, "Votre commande a été annulée : cuisine fermée.", cs["subtitle"])
}

func TestIsTerminalOrderStatus(t *testing.T) {
	for _, s := range []orderDomain.OrderStatus{
		orderDomain.OrderStatusDelivered, orderDomain.OrderStatusPickedUp,
		orderDomain.OrderStatusCanceled, orderDomain.OrderStatusFailed,
	} {
		require.True(t, IsTerminalOrderStatus(s), s)
	}
	for _, s := range []orderDomain.OrderStatus{
		orderDomain.OrderStatusPending, orderDomain.OrderStatusConfirmed, orderDomain.OrderStatusPreparing,
		orderDomain.OrderStatusAwaitingUp, orderDomain.OrderStatusOutForDelivery,
	} {
		require.False(t, IsTerminalOrderStatus(s), s)
	}
}

func TestLiveUpdateNotificationID(t *testing.T) {
	id := LiveUpdateNotificationID("order-1")
	require.Equal(t, id, LiveUpdateNotificationID("order-1"), "must be stable")
	require.GreaterOrEqual(t, id, int32(0), "must be a positive 31-bit value")
	require.NotEqual(t, id, LiveUpdateNotificationID("order-2"))
	// FNV-1a 32-bit of the empty string is the offset basis 2166136261, masked to 31 bits.
	require.Equal(t, int32(2166136261&0x7fffffff), LiveUpdateNotificationID(""))
}

func TestGetLiveUpdateData(t *testing.T) {
	t.Run("in-flight status is an update", func(t *testing.T) {
		d := GetLiveUpdateData("order-1", orderDomain.OrderStatusPreparing, "en", "DELIVERY", "app://orders/1", nil)
		require.Equal(t, map[string]string{
			"event":          "update",
			"notificationId": strconv.Itoa(int(LiveUpdateNotificationID("order-1"))),
			"title":          "Preparing your order",
			"text":           "The kitchen is preparing your order.",
			"progressMax":    "100",
			"progressValue":  "50",
			"deepLinkUrl":    "app://orders/1",
		}, d)
	})

	t.Run("terminal status stops the live update at 100%", func(t *testing.T) {
		d := GetLiveUpdateData("order-1", orderDomain.OrderStatusPickedUp, "fr", "PICKUP", "", nil)
		require.Equal(t, "stop", d["event"])
		require.Equal(t, "100", d["progressValue"])
		require.Equal(t, "Retirée", d["title"])
	})

	t.Run("cancellation reason flows into the text", func(t *testing.T) {
		d := GetLiveUpdateData("o", orderDomain.OrderStatusCanceled, "en", "DELIVERY", "", reason(orderDomain.OrderCancellationReasonOutOfStock))
		require.Equal(t, "stop", d["event"])
		require.Equal(t, "Your order has been cancelled: out of stock.", d["text"])
		require.Equal(t, "0", d["progressValue"])
	})
}

// keys of a translation table, sorted.
func langsOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The notification texts are looked up with locale.Normalize(language) and dereferenced without a
// fallback (GetNewOrderNotification), so a table that lacks a supported language would panic or
// send an empty push. Adding a language to locale must fail here until every table has it.
func TestEveryTranslationTableCoversEverySupportedLanguage(t *testing.T) {
	want := locale.Supported()
	require.Equal(t, want, langsOf(cancellationReasonPushLabels), "cancellationReasonPushLabels")
	require.Equal(t, want, langsOf(cancellationReasonBodyFormat), "cancellationReasonBodyFormat")
	require.Equal(t, want, langsOf(orderNotificationTexts), "orderNotificationTexts")
	require.Equal(t, want, langsOf(newOrderTexts), "newOrderTexts")
	require.Equal(t, want, langsOf(readyTimeUpdatedTexts), "readyTimeUpdatedTexts")
	require.Equal(t, want, langsOf(pickupOverrides), "pickupOverrides")

	for _, l := range want {
		require.NotNil(t, newOrderTexts[l], l)
		require.NotEmpty(t, newOrderTexts[l].Title, l)
		require.NotEmpty(t, readyTimeUpdatedTexts[l].Title, l)
		require.Contains(t, readyTimeUpdatedTexts[l].Body, "%s", l)
		require.Contains(t, cancellationReasonBodyFormat[l], "%s", l)
		require.NotEmpty(t, cancellationReasonPushLabels[l], l)
	}
}
