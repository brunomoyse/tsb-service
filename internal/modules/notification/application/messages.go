package application

import (
	"fmt"

	orderDomain "tsb-service/internal/modules/order/domain"
)

type notificationText struct {
	Title string
	Body  string
}

// GetOrderStatusNotification returns localized push notification text for a given order status.
func GetOrderStatusNotification(status orderDomain.OrderStatus, language string, orderType string) notificationText {
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
			return pickupMsg
		}
	}

	return msg
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
