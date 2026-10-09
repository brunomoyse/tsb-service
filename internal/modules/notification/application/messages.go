package application

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/brand"
	"tsb-service/pkg/i18n/locale"
	"tsb-service/pkg/timezone"
)

type notificationText struct {
	Title string
	Body  string
}

// GetOrderStatusNotification returns localized push notification text for a given order status.
// When the status is CANCELLED and the cancellation reason is set to something other than
// OTHER, the reason is appended to the body so the customer knows why.
func GetOrderStatusNotification(status orderDomain.OrderStatus, language string, orderType string, cancellationReason *orderDomain.OrderCancellationReason) notificationText {
	language = locale.Normalize(language)
	texts := orderNotificationTexts[language]

	msg, ok := texts[status]
	if !ok {
		return notificationText{Title: brand.Current().Name, Body: ""}
	}

	// Use pickup-specific text if available
	if orderType == "PICKUP" {
		if pickupMsg, exists := pickupOverrides[language][status]; exists {
			msg = pickupMsg
		}
	}

	// Append localized cancellation reason when meaningful.
	if status == orderDomain.OrderStatusCanceled && cancellationReason != nil && *cancellationReason != orderDomain.OrderCancellationReasonOther {
		if reasonLabel, ok := cancellationReasonPushLabels[language][*cancellationReason]; ok && reasonLabel != "" {
			msg.Body = fmt.Sprintf(cancellationReasonBodyFormat[language], reasonLabel)
		}
	}

	return msg
}

// Localized labels + templates for the cancellation reason appended to push bodies.
// OTHER is intentionally omitted: we keep the generic body.
var cancellationReasonPushLabels = map[string]map[orderDomain.OrderCancellationReason]string{
	"fr": {
		orderDomain.OrderCancellationReasonOutOfStock:    "rupture de stock",
		orderDomain.OrderCancellationReasonKitchenClosed: "cuisine fermée",
		orderDomain.OrderCancellationReasonDeliveryArea:  "hors zone de livraison",
		orderDomain.OrderCancellationReasonDuplicate:     "commande en double",
	},
	"en": {
		orderDomain.OrderCancellationReasonOutOfStock:    "out of stock",
		orderDomain.OrderCancellationReasonKitchenClosed: "kitchen closed",
		orderDomain.OrderCancellationReasonDeliveryArea:  "outside delivery area",
		orderDomain.OrderCancellationReasonDuplicate:     "duplicate order",
	},
	"nl": {
		orderDomain.OrderCancellationReasonOutOfStock:    "uitverkocht",
		orderDomain.OrderCancellationReasonKitchenClosed: "keuken gesloten",
		orderDomain.OrderCancellationReasonDeliveryArea:  "buiten de leveringszone",
		orderDomain.OrderCancellationReasonDuplicate:     "dubbele bestelling",
	},
	"zh": {
		orderDomain.OrderCancellationReasonOutOfStock:    "缺货",
		orderDomain.OrderCancellationReasonKitchenClosed: "厨房已停止接单",
		orderDomain.OrderCancellationReasonDeliveryArea:  "超出配送范围",
		orderDomain.OrderCancellationReasonDuplicate:     "重复下单",
	},
}

var cancellationReasonBodyFormat = map[string]string{
	"fr": "Votre commande a été annulée : %s.",
	"en": "Your order has been cancelled: %s.",
	"nl": "Uw bestelling is geannuleerd: %s.",
	"zh": "您的订单已被取消：%s。",
}

// GetReadyTimeUpdatedNotification returns localized push notification text when
// the estimated ready time is updated.
func GetReadyTimeUpdatedNotification(language string, estimatedReadyTime *time.Time) notificationText {
	if estimatedReadyTime == nil {
		return notificationText{Title: brand.Current().Name, Body: ""}
	}

	language = locale.Normalize(language)
	texts := readyTimeUpdatedTexts[language]

	return notificationText{
		Title: texts.Title,
		Body:  fmt.Sprintf(texts.Body, formatReadyTimeForNotification(*estimatedReadyTime, language)),
	}
}

// orderNotificationTexts holds the push title + body per status & locale.
// Titles mirror the customer-facing email subjects (pkg/email/scaleway) so push,
// Live Activity and email read consistently; bodies stay short and distinct from
// the title. Statuses without a dedicated status email (PREPARING / DELIVERED /
// PICKED_UP / FAILED) keep their own concise wording.
var orderNotificationTexts = map[string]map[orderDomain.OrderStatus]notificationText{
	"fr": {
		orderDomain.OrderStatusConfirmed:      {Title: "Commande confirmée", Body: "Votre commande a été confirmée par le restaurant."},
		orderDomain.OrderStatusPreparing:      {Title: "En préparation", Body: "Votre commande est en cours de préparation."},
		orderDomain.OrderStatusAwaitingUp:     {Title: "Votre commande est prête !", Body: "Elle est prête et vous attend."},
		orderDomain.OrderStatusOutForDelivery: {Title: "Votre commande est en route !", Body: "Elle arrive bientôt."},
		orderDomain.OrderStatusDelivered:      {Title: "Livrée", Body: "Votre commande a été livrée. Bon appétit !"},
		orderDomain.OrderStatusPickedUp:       {Title: "Retirée", Body: "Votre commande a été retirée. Bon appétit !"},
		orderDomain.OrderStatusCanceled:       {Title: "Commande annulée", Body: "Votre commande a été annulée."},
		orderDomain.OrderStatusFailed:         {Title: "Commande échouée", Body: "Un problème est survenu avec votre commande."},
	},
	"en": {
		orderDomain.OrderStatusConfirmed:      {Title: "Order confirmed", Body: "Your order has been confirmed by the restaurant."},
		orderDomain.OrderStatusPreparing:      {Title: "Preparing your order", Body: "The kitchen is preparing your order."},
		orderDomain.OrderStatusAwaitingUp:     {Title: "Your order is ready!", Body: "It's ready and waiting."},
		orderDomain.OrderStatusOutForDelivery: {Title: "Your order is on its way!", Body: "It'll be with you soon."},
		orderDomain.OrderStatusDelivered:      {Title: "Delivered", Body: "Your order has been delivered. Enjoy!"},
		orderDomain.OrderStatusPickedUp:       {Title: "Picked up", Body: "Your order has been picked up. Enjoy!"},
		orderDomain.OrderStatusCanceled:       {Title: "Order cancelled", Body: "Your order has been cancelled."},
		orderDomain.OrderStatusFailed:         {Title: "Order failed", Body: "There was a problem with your order."},
	},
	"zh": {
		orderDomain.OrderStatusConfirmed:      {Title: "订单已确认", Body: "您的订单已被餐厅确认。"},
		orderDomain.OrderStatusPreparing:      {Title: "正在准备", Body: "您的订单正在准备中。"},
		orderDomain.OrderStatusAwaitingUp:     {Title: "您的订单已准备好！", Body: "请尽快取餐。"},
		orderDomain.OrderStatusOutForDelivery: {Title: "您的订单正在配送中！", Body: "很快就到。"},
		orderDomain.OrderStatusDelivered:      {Title: "已送达", Body: "您的订单已送达，祝您用餐愉快！"},
		orderDomain.OrderStatusPickedUp:       {Title: "已取餐", Body: "您已取餐，祝您用餐愉快！"},
		orderDomain.OrderStatusCanceled:       {Title: "订单已取消", Body: "您的订单已被取消。"},
		orderDomain.OrderStatusFailed:         {Title: "订单失败", Body: "您的订单出现了问题。"},
	},
	"nl": {
		orderDomain.OrderStatusConfirmed:      {Title: "Bestelling bevestigd", Body: "Uw bestelling is bevestigd door het restaurant."},
		orderDomain.OrderStatusPreparing:      {Title: "In bereiding", Body: "Uw bestelling wordt bereid."},
		orderDomain.OrderStatusAwaitingUp:     {Title: "Uw bestelling is klaar!", Body: "Ze staat voor u klaar."},
		orderDomain.OrderStatusOutForDelivery: {Title: "Uw bestelling is onderweg!", Body: "Ze is zo bij u."},
		orderDomain.OrderStatusDelivered:      {Title: "Geleverd", Body: "Uw bestelling is geleverd. Smakelijk!"},
		orderDomain.OrderStatusPickedUp:       {Title: "Afgehaald", Body: "Uw bestelling is afgehaald. Smakelijk!"},
		orderDomain.OrderStatusCanceled:       {Title: "Bestelling geannuleerd", Body: "Uw bestelling is geannuleerd."},
		orderDomain.OrderStatusFailed:         {Title: "Bestelling mislukt", Body: "Er is een probleem met uw bestelling."},
	},
}

// GetNewOrderNotification returns localized push notification text for admin/POS
// devices when a new order is created. Deliberately omits the amount (and type):
// staff just need to know an order landed and is awaiting confirmation.
// orderType/total are kept in the signature for callers but are intentionally
// unused.
func GetNewOrderNotification(language, _ /*orderType*/, _ /*total*/ string) notificationText {
	return *newOrderTexts[locale.Normalize(language)]
}

var newOrderTexts = map[string]*notificationText{
	"fr": {Title: "Nouvelle commande", Body: "En attente de confirmation"},
	"en": {Title: "New order", Body: "Awaiting confirmation"},
	"zh": {Title: "新订单", Body: "等待确认"},
	"nl": {Title: "Nieuwe bestelling", Body: "Wacht op bevestiging"},
}

var readyTimeUpdatedTexts = map[string]*notificationText{
	"fr": {Title: "Heure estimée mise à jour", Body: "Nouvelle heure estimée : %s."},
	"en": {Title: "Estimated time updated", Body: "New estimated time: %s."},
	"zh": {Title: "预计时间已更新", Body: "新的预计时间：%s。"},
	"nl": {Title: "Geschatte tijd bijgewerkt", Body: "Nieuwe geschatte tijd: %s."},
}

func formatReadyTimeForNotification(t time.Time, language string) string {
	language = locale.Normalize(language)
	local := timezone.In(t)
	hour := local.Hour()
	minute := local.Minute()

	if language == "en" {
		period := "AM"
		displayHour := hour
		if hour >= 12 {
			period = "PM"
			if hour > 12 {
				displayHour = hour - 12
			}
		}
		if displayHour == 0 {
			displayHour = 12
		}

		return fmt.Sprintf("%d:%02d %s", displayHour, minute, period)
	}

	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// pickupOverrides keep the same email-aligned title ("…is ready!") but give a
// pickup-specific body inviting the customer to collect it at the counter.
var pickupOverrides = map[string]map[orderDomain.OrderStatus]notificationText{
	"fr": {
		orderDomain.OrderStatusAwaitingUp: {Title: "Votre commande est prête !", Body: "Venez la retirer au comptoir."},
	},
	"en": {
		orderDomain.OrderStatusAwaitingUp: {Title: "Your order is ready!", Body: "Come pick it up at the counter."},
	},
	"zh": {
		orderDomain.OrderStatusAwaitingUp: {Title: "您的订单已准备好！", Body: "请到柜台取餐。"},
	},
	"nl": {
		orderDomain.OrderStatusAwaitingUp: {Title: "Uw bestelling is klaar!", Body: "U kunt ze aan de balie afhalen."},
	},
}

// liveActivityDeliverySteps / liveActivityPickupSteps mirror the iOS app's
// stepsFor() so the pushed Live Activity progress matches the in-app timeline.
var liveActivityDeliverySteps = []orderDomain.OrderStatus{
	orderDomain.OrderStatusPending,
	orderDomain.OrderStatusConfirmed,
	orderDomain.OrderStatusPreparing,
	orderDomain.OrderStatusOutForDelivery,
	orderDomain.OrderStatusDelivered,
}

var liveActivityPickupSteps = []orderDomain.OrderStatus{
	orderDomain.OrderStatusPending,
	orderDomain.OrderStatusConfirmed,
	orderDomain.OrderStatusPreparing,
	orderDomain.OrderStatusAwaitingUp,
	orderDomain.OrderStatusPickedUp,
}

// liveActivityProgress returns the 0..1 step position for a status (determinate
// progress; matches the app; no timer).
func liveActivityProgress(status orderDomain.OrderStatus, orderType string) float64 {
	steps := liveActivityDeliverySteps
	if orderType == "PICKUP" {
		steps = liveActivityPickupSteps
	}
	for i, s := range steps {
		if s == status {
			if len(steps) <= 1 {
				return 0
			}
			return float64(i) / float64(len(steps)-1)
		}
	}
	return 0
}

// GetLiveActivityContentState builds the ActivityKit content-state for an order
// status (title + subtitle + progress). It reuses the localized status texts so
// the Live Activity wording matches the alert push.
func GetLiveActivityContentState(status orderDomain.OrderStatus, language, orderType string, cancellationReason *orderDomain.OrderCancellationReason) map[string]any {
	msg := GetOrderStatusNotification(status, language, orderType, cancellationReason)
	return map[string]any{
		"title":    msg.Title,
		"subtitle": msg.Body,
		"progress": liveActivityProgress(status, orderType),
	}
}

// IsTerminalOrderStatus reports whether a status ends the order's lifecycle, so
// the Live Activity / Live Update should be ended rather than updated.
func IsTerminalOrderStatus(status orderDomain.OrderStatus) bool {
	switch status {
	case orderDomain.OrderStatusDelivered,
		orderDomain.OrderStatusPickedUp,
		orderDomain.OrderStatusCanceled,
		orderDomain.OrderStatusFailed:
		return true
	default:
		return false
	}
}

// LiveUpdateNotificationID derives a stable positive 31-bit notification id from
// an order id. The Android app computes the SAME id (FNV-1a 32-bit, masked) so
// backend data messages target the Live Update the app created.
func LiveUpdateNotificationID(orderID string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(orderID))
	return int32(h.Sum32() & 0x7fffffff)
}

// GetLiveUpdateData builds the FCM data-message payload (all string values) that
// drives an Android Live Update for an order status change. event is "update" or
// "stop"; progress is expressed as 0..100 to match the app's progress bar max.
func GetLiveUpdateData(orderID string, status orderDomain.OrderStatus, language, orderType, deepLink string, cancellationReason *orderDomain.OrderCancellationReason) map[string]string {
	cs := GetLiveActivityContentState(status, language, orderType, cancellationReason)
	event := "update"
	if IsTerminalOrderStatus(status) {
		event = "stop"
	}
	progress, _ := cs["progress"].(float64)
	title, _ := cs["title"].(string)
	text, _ := cs["subtitle"].(string)

	return map[string]string{
		"event":          event,
		"notificationId": strconv.Itoa(int(LiveUpdateNotificationID(orderID))),
		"title":          title,
		"text":           text,
		"progressMax":    "100",
		"progressValue":  strconv.Itoa(int(progress * 100)),
		"deepLinkUrl":    deepLink,
	}
}
