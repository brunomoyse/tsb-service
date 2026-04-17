package application

import (
	"fmt"
	"time"

	orderDomain "tsb-service/internal/modules/order/domain"
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
	texts := orderNotificationTexts[language]
	if texts == nil {
		texts = orderNotificationTexts["fr"]
	}

	msg, ok := texts[status]
	if !ok {
		return notificationText{Title: "Tokyo Sushi Bar", Body: ""}
	}

	// Use pickup-specific text if available
	if orderType == "PICKUP" {
		if pickupMsg, exists := pickupOverrides[language][status]; exists {
			msg = pickupMsg
		}
	}

	// Append localized cancellation reason when meaningful.
	if status == orderDomain.OrderStatusCanceled && cancellationReason != nil && *cancellationReason != orderDomain.OrderCancellationReasonOther {
		labelLang := language
		if _, ok := cancellationReasonPushLabels[labelLang]; !ok {
			labelLang = "fr"
		}
		if reasonLabel, ok := cancellationReasonPushLabels[labelLang][*cancellationReason]; ok && reasonLabel != "" {
			msg.Body = fmt.Sprintf(cancellationReasonBodyFormat[labelLang], reasonLabel)
		}
	}

	return msg
}

// Localized labels + templates for the cancellation reason appended to push bodies.
// OTHER is intentionally omitted — we keep the generic body.
var cancellationReasonPushLabels = map[string]map[orderDomain.OrderCancellationReason]string{
	"fr": {
		orderDomain.OrderCancellationReasonOutOfStock:    "rupture de stock",
		orderDomain.OrderCancellationReasonKitchenClosed: "cuisine fermée",
		orderDomain.OrderCancellationReasonDeliveryArea:  "hors zone de livraison",
	},
	"en": {
		orderDomain.OrderCancellationReasonOutOfStock:    "out of stock",
		orderDomain.OrderCancellationReasonKitchenClosed: "kitchen closed",
		orderDomain.OrderCancellationReasonDeliveryArea:  "outside delivery area",
	},
	"nl": {
		orderDomain.OrderCancellationReasonOutOfStock:    "uitverkocht",
		orderDomain.OrderCancellationReasonKitchenClosed: "keuken gesloten",
		orderDomain.OrderCancellationReasonDeliveryArea:  "buiten bezorggebied",
	},
	"zh": {
		orderDomain.OrderCancellationReasonOutOfStock:    "缺货",
		orderDomain.OrderCancellationReasonKitchenClosed: "厨房已关闭",
		orderDomain.OrderCancellationReasonDeliveryArea:  "超出配送范围",
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
		return notificationText{Title: "Tokyo Sushi Bar", Body: ""}
	}

	texts := readyTimeUpdatedTexts[language]
	if texts == nil {
		texts = readyTimeUpdatedTexts["fr"]
	}

	return notificationText{
		Title: texts.Title,
		Body:  fmt.Sprintf(texts.Body, formatReadyTimeForNotification(*estimatedReadyTime, language)),
	}
}

var orderNotificationTexts = map[string]map[orderDomain.OrderStatus]notificationText{
	"fr": {
		orderDomain.OrderStatusConfirmed:      {Title: "Commande confirmée", Body: "Votre commande a été confirmée par le restaurant."},
		orderDomain.OrderStatusPreparing:      {Title: "En préparation", Body: "Votre commande est en cours de préparation."},
		orderDomain.OrderStatusAwaitingUp:     {Title: "Prête !", Body: "Votre commande est prête."},
		orderDomain.OrderStatusOutForDelivery: {Title: "En livraison", Body: "Votre commande est en route !"},
		orderDomain.OrderStatusDelivered:      {Title: "Livrée", Body: "Votre commande a été livrée. Bon appétit !"},
		orderDomain.OrderStatusPickedUp:       {Title: "Retirée", Body: "Votre commande a été retirée. Bon appétit !"},
		orderDomain.OrderStatusCanceled:       {Title: "Commande annulée", Body: "Votre commande a été annulée."},
		orderDomain.OrderStatusFailed:         {Title: "Commande échouée", Body: "Un problème est survenu avec votre commande."},
	},
	"en": {
		orderDomain.OrderStatusConfirmed:      {Title: "Order confirmed", Body: "Your order has been confirmed by the restaurant."},
		orderDomain.OrderStatusPreparing:      {Title: "Preparing your order", Body: "The kitchen is preparing your order."},
		orderDomain.OrderStatusAwaitingUp:     {Title: "Ready!", Body: "Your order is ready."},
		orderDomain.OrderStatusOutForDelivery: {Title: "Out for delivery", Body: "Your order is on its way!"},
		orderDomain.OrderStatusDelivered:      {Title: "Delivered", Body: "Your order has been delivered. Enjoy!"},
		orderDomain.OrderStatusPickedUp:       {Title: "Picked up", Body: "Your order has been picked up. Enjoy!"},
		orderDomain.OrderStatusCanceled:       {Title: "Order cancelled", Body: "Your order has been cancelled."},
		orderDomain.OrderStatusFailed:         {Title: "Order failed", Body: "There was a problem with your order."},
	},
	"zh": {
		orderDomain.OrderStatusConfirmed:      {Title: "订单已确认", Body: "您的订单已被餐厅确认。"},
		orderDomain.OrderStatusPreparing:      {Title: "正在准备", Body: "您的订单正在准备中。"},
		orderDomain.OrderStatusAwaitingUp:     {Title: "准备好了！", Body: "您的订单已准备好。"},
		orderDomain.OrderStatusOutForDelivery: {Title: "配送中", Body: "您的订单正在配送途中！"},
		orderDomain.OrderStatusDelivered:      {Title: "已送达", Body: "您的订单已送达，请享用！"},
		orderDomain.OrderStatusPickedUp:       {Title: "已取走", Body: "您的订单已取走，请享用！"},
		orderDomain.OrderStatusCanceled:       {Title: "订单已取消", Body: "您的订单已被取消。"},
		orderDomain.OrderStatusFailed:         {Title: "订单失败", Body: "您的订单出现了问题。"},
	},
	"nl": {
		orderDomain.OrderStatusConfirmed:      {Title: "Bestelling bevestigd", Body: "Uw bestelling is bevestigd door het restaurant."},
		orderDomain.OrderStatusPreparing:      {Title: "In voorbereiding", Body: "Uw bestelling wordt bereid."},
		orderDomain.OrderStatusAwaitingUp:     {Title: "Klaar!", Body: "Uw bestelling is klaar."},
		orderDomain.OrderStatusOutForDelivery: {Title: "Onderweg", Body: "Uw bestelling is onderweg!"},
		orderDomain.OrderStatusDelivered:      {Title: "Bezorgd", Body: "Uw bestelling is bezorgd. Eet smakelijk!"},
		orderDomain.OrderStatusPickedUp:       {Title: "Opgehaald", Body: "Uw bestelling is opgehaald. Eet smakelijk!"},
		orderDomain.OrderStatusCanceled:       {Title: "Bestelling geannuleerd", Body: "Uw bestelling is geannuleerd."},
		orderDomain.OrderStatusFailed:         {Title: "Bestelling mislukt", Body: "Er is een probleem met uw bestelling."},
	},
}

// GetNewOrderNotification returns localized push notification text for admin devices when a new order is created.
func GetNewOrderNotification(language, orderType, total string) notificationText {
	texts := newOrderTexts[language]
	if texts == nil {
		texts = newOrderTexts["fr"]
	}

	var typeKey string
	switch language {
	case "en":
		if orderType == "PICKUP" {
			typeKey = "pickup"
		} else {
			typeKey = "delivery"
		}
	case "zh":
		if orderType == "PICKUP" {
			typeKey = "自取"
		} else {
			typeKey = "外送"
		}
	case "nl":
		if orderType == "PICKUP" {
			typeKey = "afhaling"
		} else {
			typeKey = "levering"
		}
	default: // fr
		if orderType == "PICKUP" {
			typeKey = "retrait"
		} else {
			typeKey = "livraison"
		}
	}

	return notificationText{
		Title: texts.Title,
		Body:  fmt.Sprintf(texts.Body, typeKey, total),
	}
}

var newOrderTexts = map[string]*notificationText{
	"fr": {Title: "Nouvelle commande", Body: "Nouvelle commande %s de %s€"},
	"en": {Title: "New order", Body: "New %s order for %s€"},
	"zh": {Title: "新订单", Body: "新%s订单 %s€"},
	"nl": {Title: "Nieuwe bestelling", Body: "Nieuwe %s bestelling van %s€"},
}

var readyTimeUpdatedTexts = map[string]*notificationText{
	"fr": {Title: "Heure de retrait mise à jour", Body: "Nouvelle heure estimée : %s."},
	"en": {Title: "Ready time updated", Body: "New estimated ready time: %s."},
	"zh": {Title: "预计完成时间已更新", Body: "新的预计完成时间：%s。"},
	"nl": {Title: "Afhaaltijd bijgewerkt", Body: "Nieuwe geschatte afhaaltijd: %s."},
}

func formatReadyTimeForNotification(t time.Time, language string) string {
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

var pickupOverrides = map[string]map[orderDomain.OrderStatus]notificationText{
	"fr": {
		orderDomain.OrderStatusAwaitingUp: {Title: "Prête à retirer !", Body: "Votre commande est prête à être retirée."},
	},
	"en": {
		orderDomain.OrderStatusAwaitingUp: {Title: "Ready for pickup!", Body: "Your order is ready to be picked up."},
	},
	"zh": {
		orderDomain.OrderStatusAwaitingUp: {Title: "可以取餐了！", Body: "您的订单已准备好，可以来取餐了。"},
	},
	"nl": {
		orderDomain.OrderStatusAwaitingUp: {Title: "Klaar om op te halen!", Body: "Uw bestelling is klaar om opgehaald te worden."},
	},
}
