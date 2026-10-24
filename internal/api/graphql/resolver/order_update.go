package resolver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/model"
	addressDomain "tsb-service/internal/modules/address/domain"
	notificationApplication "tsb-service/internal/modules/notification/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/apns"
	es "tsb-service/pkg/email/scaleway"
	"tsb-service/pkg/fcm"
	"tsb-service/pkg/utils"
)

// ApplyOrderUpdate is the orchestration behind the updateOrder mutation, callable by anything that
// moves an order on behalf of staff or of another system (a HubRise status event, for instance).
// It applies the change (settling the payment first when the order is cancelled), then tells the
// customer (e-mail, push, Live Activity / Live Update) and the dashboards (orderUpdated topics).
//
// The context's origin (utils.WithOrigin) is carried down to the order service, whose status
// observer skips changes that came from the system the observer would only echo back to.
func (r *Resolver) ApplyOrderUpdate(ctx context.Context, id uuid.UUID, input model.UpdateOrderInput) (*model.Order, error) {
	oldOrder, _, err := r.OrderService.GetOrderByID(ctx, id)
	if err != nil {
		return nil, orderLookupError(err)
	}

	if err := r.refuseReopeningSettledOrder(ctx, oldOrder, input.Status); err != nil {
		return nil, err
	}

	// Cancelling settles the payment FIRST and saves CANCELLED only when that worked: a refund or
	// cancel that the payment provider refuses leaves the order untouched, so staff can retry. See cancelOrder.
	cancelling := input.Status != nil && *input.Status == orderDomain.OrderStatusCanceled &&
		oldOrder.OrderStatus != orderDomain.OrderStatusCanceled
	var settlement paymentDomain.CancelSettlement
	if cancelling {
		oldOrder, settlement, err = r.cancelOrder(ctx, id, input, oldOrder)
		if err != nil {
			return nil, err
		}
	} else if err = r.saveOrder(ctx, id, input); err != nil {
		return nil, err
	}

	// Fetch the updated order
	o, opRaw, err := r.OrderService.GetOrderByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get order: %w", err)
	}

	// Use the stored order language for all emails (captured at order creation)
	lang := o.Language

	// Skip progression-status notifications when the update lands well after
	// the customer has likely already received the order. CANCELLED / FAILED
	// stay exempt: those carry information regardless of timing (refund,
	// "your order wasn't fulfilled").
	suppressLate := isOrderUpdateTooLate(time.Now(), o.EstimatedReadyTime) &&
		o.OrderStatus != orderDomain.OrderStatusCanceled &&
		o.OrderStatus != orderDomain.OrderStatusFailed
	if suppressLate {
		zap.L().Info("suppressed late order notification",
			zap.String("order_id", o.ID.String()),
			zap.String("status", string(o.OrderStatus)),
			zap.Time("estimated_ready_time", *o.EstimatedReadyTime),
		)
	}

	// If new status is "CONFIRMED" or "PREPARING", send the confirmation email
	if oldOrder.OrderStatus == orderDomain.OrderStatusPending && (o.OrderStatus == orderDomain.OrderStatusConfirmed || o.OrderStatus == orderDomain.OrderStatusPreparing) {
		go func() {
			ctx, cancel := emailContext()
			defer cancel()
			user, err := r.UserService.GetUserByID(ctx, o.UserID.String())
			if err != nil {
				zap.L().Error("failed to retrieve user", zap.String("order_id", o.ID.String()), zap.Error(err))
				return
			}

			// 1) collect product IDs as strings
			raws := *opRaw
			ids := make([]string, len(raws))
			for i, ir := range raws {
				ids[i] = ir.ProductID.String()
			}

			// 2) fetch all products in one go
			prodDetailsSlice, err := r.ProductService.GetProductNamesForInvoice(ctx, ids)
			if err != nil {
				zap.L().Error("failed to retrieve products", zap.String("order_id", o.ID.String()), zap.Error(err))
				return
			}

			// 3) build a map for quick lookup
			prodMap := make(map[uuid.UUID]productDomain.ProductOrderDetails, len(prodDetailsSlice))
			for _, pd := range prodDetailsSlice {
				prodMap[pd.ID] = *pd
			}

			// 4) enrich raws into your final items
			items := make([]orderDomain.OrderProduct, len(raws))
			for i, ir := range raws {
				pd, ok := prodMap[ir.ProductID]
				if !ok {
					zap.L().Error("missing product details", zap.String("order_id", o.ID.String()), zap.String("product_id", ir.ProductID.String()))
					return
				}
				items[i] = orderDomain.OrderProduct{
					Product: orderDomain.Product{
						ID:           pd.ID,
						Code:         pd.Code,
						CategoryName: pd.CategoryName,
						Name:         pd.Name,
						VatCategory:  string(pd.VatCategory),
					},
					Quantity:   ir.Quantity,
					UnitPrice:  ir.UnitPrice,
					TotalPrice: ir.TotalPrice,
				}
			}

			var address *addressDomain.Address
			// Use denormalized address fields from the order (all new orders have these)
			if o.StreetName != nil {
				address = addressFromOrder(o)
			}

			if !user.NotifyOrderUpdates {
				return
			}
			// address may be nil for very old orders; email service should handle gracefully
			err = es.SendOrderConfirmedEmail(*user, lang, *o, items, address)
			if err != nil {
				zap.L().Error("failed to send order confirmed email", zap.String("order_id", o.ID.String()), zap.Error(err))
			}
		}()
	}

	// Send order ready email for actionable statuses:
	// - AWAITING_PICK_UP: pickup customers need to come get their order
	// - OUT_FOR_DELIVERY: delivery customers need to know food is on its way
	if (o.OrderStatus == orderDomain.OrderStatusAwaitingUp || o.OrderStatus == orderDomain.OrderStatusOutForDelivery) && !suppressLate {
		go func() {
			ctx, cancel := emailContext()
			defer cancel()
			user, err := r.UserService.GetUserByID(ctx, o.UserID.String())
			if err != nil {
				zap.L().Error("failed to retrieve user", zap.String("order_id", o.ID.String()), zap.Error(err))
				return
			}

			if !user.NotifyOrderUpdates {
				return
			}
			err = es.SendOrderReadyEmail(*user, lang, *o)
			if err != nil {
				zap.L().Error("failed to send order ready email", zap.String("order_id", o.ID.String()), zap.Error(err))
			}
		}()
	}

	// If estimated ready time changed on an already-confirmed order (not the initial confirmation)
	isInitialConfirmation := oldOrder.OrderStatus == orderDomain.OrderStatusPending &&
		(o.OrderStatus == orderDomain.OrderStatusConfirmed || o.OrderStatus == orderDomain.OrderStatusPreparing)
	readyTimeWasUpdated := input.EstimatedReadyTime != nil &&
		o.EstimatedReadyTime != nil &&
		!isInitialConfirmation &&
		(oldOrder.EstimatedReadyTime == nil || !oldOrder.EstimatedReadyTime.Equal(*o.EstimatedReadyTime))
	if readyTimeWasUpdated {
		go func() {
			ctx, cancel := emailContext()
			defer cancel()
			user, err := r.UserService.GetUserByID(ctx, o.UserID.String())
			if err != nil {
				zap.L().Error("failed to retrieve user", zap.String("order_id", o.ID.String()), zap.Error(err))
				return
			}

			if !user.NotifyOrderUpdates {
				return
			}
			err = es.SendReadyTimeUpdatedEmail(*user, lang, *o)
			if err != nil {
				zap.L().Error("failed to send ready time updated email", zap.String("order_id", o.ID.String()), zap.Error(err))
			}
		}()
	}

	// On the transition into CANCELLED: tell the customer about the refund (the payment was settled
	// before the status was saved, see above) and send the cancellation email. Re-saving an order
	// that was already cancelled does neither again. The refund e-mail is sent here, after the save,
	// and not when the money moved: a refund whose order save failed is only mentioned once the retry
	// saves the order, and it then states what is back with the customer (RefundNotice).
	if o.OrderStatus == orderDomain.OrderStatusCanceled && oldOrder.OrderStatus != orderDomain.OrderStatusCanceled {
		if refundNotice := settlement.RefundNotice(); refundNotice.IsPositive() {
			go func() {
				ctx, cancel := emailContext()
				defer cancel()
				user, err := r.UserService.GetUserByID(ctx, o.UserID.String())
				if err != nil {
					zap.L().Error("failed to retrieve user", zap.String("order_id", o.ID.String()), zap.Error(err))
					return
				}

				if !user.NotifyOrderUpdates {
					return
				}
				refundAmount := utils.FormatDecimal(refundNotice)
				err = es.SendRefundIssuedEmail(*user, lang, o.ID.String(), refundAmount)
				if err != nil {
					zap.L().Error("failed to send refund issued email", zap.String("order_id", o.ID.String()), zap.Error(err))
				}
			}()
		}

		go func() {
			ctx, cancel := emailContext()
			defer cancel()
			user, err := r.UserService.GetUserByID(ctx, o.UserID.String())
			if err != nil {
				zap.L().Error("failed to retrieve user", zap.String("order_id", o.ID.String()), zap.Error(err))
				return
			}

			if !user.NotifyOrderUpdates {
				return
			}
			err = es.SendOrderCanceledEmail(*user, lang, o.ID.String(), o.CancellationReason)
			if err != nil {
				zap.L().Error("failed to send order canceled email", zap.String("order_id", o.ID.String()), zap.Error(err))
			}
		}()
	}

	// Map the order to the GraphQL model
	order := ToGQLOrder(o)

	// Publish the order update to the broker
	r.Broker.Publish("orderUpdated", order)
	r.Broker.Publish(fmt.Sprintf("orderUpdated:%s", id), order)

	// Send standard alert push notifications to user's devices (non-blocking).
	// Only fired when the status actually changed.
	//
	// Exception: OUT_FOR_DELIVERY → DELIVERED is suppressed. The driver hands the
	// food directly to the customer, so a "Your order has been delivered" push
	// would be noise arriving at exactly the moment the customer already knows.
	statusChanged := oldOrder.OrderStatus != o.OrderStatus
	suppressDelivered := oldOrder.OrderStatus == orderDomain.OrderStatusOutForDelivery &&
		o.OrderStatus == orderDomain.OrderStatusDelivered
	if r.APNsClient != nil && statusChanged && !suppressDelivered && !suppressLate {
		go func() {
			deviceTokens, tokenErr := r.NotificationService.GetDeviceTokens(context.Background(), o.UserID)
			if tokenErr != nil || len(deviceTokens) == 0 {
				return
			}

			msg := notificationApplication.GetOrderStatusNotification(o.OrderStatus, o.Language, string(o.OrderType), o.CancellationReason)
			if msg.Body == "" {
				return
			}

			data := map[string]string{
				"orderId": id.String(),
				"status":  string(o.OrderStatus),
			}

			for _, dt := range deviceTokens {
				if dt.Platform == "ios" && r.APNsClient != nil {
					if pushErr := r.APNsClient.SendAlert(dt.DeviceToken, msg.Title, msg.Body, data); pushErr != nil {
						if errors.Is(pushErr, apns.ErrTokenInvalid) {
							_ = r.NotificationService.UnregisterDeviceToken(context.Background(), o.UserID, dt.DeviceToken)
						} else {
							zap.L().Error("failed to send alert push",
								zap.String("order_id", id.String()),
								zap.Error(pushErr),
							)
						}
					}
				} else if dt.Platform == "android" && r.FCMClient != nil {
					if pushErr := r.FCMClient.SendAlert(dt.DeviceToken, msg.Title, msg.Body, data); pushErr != nil {
						if errors.Is(pushErr, fcm.ErrTokenInvalid) {
							_ = r.NotificationService.UnregisterDeviceToken(context.Background(), o.UserID, dt.DeviceToken)
						} else {
							zap.L().Error("failed to send FCM push",
								zap.String("order_id", id.String()),
								zap.Error(pushErr),
							)
						}
					}
				}
			}
		}()
	}

	// Update the iOS Live Activity (Lock Screen / Dynamic Island) for this order.
	// Fires on EVERY status change, including OUT_FOR_DELIVERY → DELIVERED, whose
	// *alert* is suppressed above, because the activity must be ended on terminal
	// statuses regardless of alert suppression.
	if r.APNsClient != nil && statusChanged {
		go func() {
			laTokens, tokenErr := r.NotificationService.GetLiveActivityTokens(context.Background(), id)
			if tokenErr != nil || len(laTokens) == 0 {
				return
			}

			terminal := o.OrderStatus == orderDomain.OrderStatusDelivered ||
				o.OrderStatus == orderDomain.OrderStatusPickedUp ||
				o.OrderStatus == orderDomain.OrderStatusCanceled ||
				o.OrderStatus == orderDomain.OrderStatusFailed
			event := "update"
			if terminal {
				event = "end"
			}

			contentState := notificationApplication.GetLiveActivityContentState(
				o.OrderStatus, o.Language, string(o.OrderType), o.CancellationReason,
			)

			for _, lt := range laTokens {
				if pushErr := r.APNsClient.SendLiveActivity(lt.PushToken, contentState, event); pushErr != nil {
					zap.L().Warn("failed to send live activity push",
						zap.String("order_id", id.String()),
						zap.Error(pushErr),
					)
				}
			}

			if terminal {
				if clearErr := r.NotificationService.ClearLiveActivityTokens(context.Background(), id); clearErr != nil {
					zap.L().Warn("failed to clear live activity tokens",
						zap.String("order_id", id.String()),
						zap.Error(clearErr),
					)
				}
			}
		}()
	}

	// Drive the Android Live Update (promoted ongoing notification) via an FCM
	// data message to the user's Android devices. Like the iOS block, this fires
	// on every status change so the Live Update ends on terminal statuses.
	if r.FCMClient != nil && statusChanged {
		go func() {
			deviceTokens, tokenErr := r.NotificationService.GetDeviceTokens(context.Background(), o.UserID)
			if tokenErr != nil || len(deviceTokens) == 0 {
				return
			}

			deepLink := fmt.Sprintf("tsbmobile://order-completed/%s", id.String())
			data := notificationApplication.GetLiveUpdateData(
				id.String(), o.OrderStatus, o.Language, string(o.OrderType), deepLink, o.CancellationReason,
			)

			for _, dt := range deviceTokens {
				if dt.Platform != "android" {
					continue
				}
				if pushErr := r.FCMClient.SendDataMessage(dt.DeviceToken, data); pushErr != nil {
					if errors.Is(pushErr, fcm.ErrTokenInvalid) {
						_ = r.NotificationService.UnregisterDeviceToken(context.Background(), o.UserID, dt.DeviceToken)
					} else {
						zap.L().Warn("failed to send live update data message",
							zap.String("order_id", id.String()),
							zap.Error(pushErr),
						)
					}
				}
			}
		}()
	}

	// Send a visible alert when the estimated ready time changes.
	if (r.APNsClient != nil || r.FCMClient != nil) && readyTimeWasUpdated {
		go func() {
			deviceTokens, tokenErr := r.NotificationService.GetDeviceTokens(context.Background(), o.UserID)
			if tokenErr != nil || len(deviceTokens) == 0 {
				return
			}

			msg := notificationApplication.GetReadyTimeUpdatedNotification(o.Language, o.EstimatedReadyTime)
			if msg.Body == "" {
				return
			}

			data := map[string]string{
				"orderId": id.String(),
				"status":  string(o.OrderStatus),
			}
			if o.EstimatedReadyTime != nil {
				data["estimatedReadyTime"] = o.EstimatedReadyTime.Format(time.RFC3339)
			}

			for _, dt := range deviceTokens {
				if dt.Platform == "ios" && r.APNsClient != nil {
					if pushErr := r.APNsClient.SendAlert(dt.DeviceToken, msg.Title, msg.Body, data); pushErr != nil {
						if errors.Is(pushErr, apns.ErrTokenInvalid) {
							_ = r.NotificationService.UnregisterDeviceToken(context.Background(), o.UserID, dt.DeviceToken)
						} else {
							zap.L().Error("failed to send ready time APNs push",
								zap.String("order_id", id.String()),
								zap.Error(pushErr),
							)
						}
					}
				} else if dt.Platform == "android" && r.FCMClient != nil {
					if pushErr := r.FCMClient.SendAlert(dt.DeviceToken, msg.Title, msg.Body, data); pushErr != nil {
						if errors.Is(pushErr, fcm.ErrTokenInvalid) {
							_ = r.NotificationService.UnregisterDeviceToken(context.Background(), o.UserID, dt.DeviceToken)
						} else {
							zap.L().Error("failed to send ready time FCM push",
								zap.String("order_id", id.String()),
								zap.Error(pushErr),
							)
						}
					}
				}
			}
		}()
	}

	return order, nil
}
