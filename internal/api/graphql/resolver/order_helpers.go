package resolver

// Helper functions for the order resolvers. These live in a non-generated file
// so `gqlgen generate` does not move them into the "WARNING" block at the end of
// order.go (gqlgen relocates any helper methods it finds in the resolver files
// it regenerates).

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	notificationApplication "tsb-service/internal/modules/notification/application"
	orderDomain "tsb-service/internal/modules/order/domain"
)

func normalizeOrderLanguage(l string) string {
	base := strings.ToLower(strings.TrimSpace(l))
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "fr", "en", "nl", "zh":
		return base
	default:
		return ""
	}
}

func (r *mutationResolver) repushActivitiesLanguage(orders []*orderDomain.Order, lang string) {
	for _, o := range orders {
		cs := notificationApplication.GetLiveActivityContentState(o.OrderStatus, lang, string(o.OrderType), o.CancellationReason)
		// PENDING has no localized status text — nothing meaningful to re-push.
		if sub, _ := cs["subtitle"].(string); sub == "" {
			continue
		}

		if r.APNsClient != nil {
			if laTokens, lerr := r.NotificationService.GetLiveActivityTokens(context.Background(), o.ID); lerr == nil {
				for _, lt := range laTokens {
					if pushErr := r.APNsClient.SendLiveActivity(lt.PushToken, cs, "update"); pushErr != nil {
						zap.L().Warn("failed to re-push live activity (language)",
							zap.String("order_id", o.ID.String()), zap.Error(pushErr))
					}
				}
			}
		}

		if r.FCMClient != nil {
			if deviceTokens, derr := r.NotificationService.GetDeviceTokens(context.Background(), o.UserID); derr == nil {
				deepLink := fmt.Sprintf("tsbmobile://order-completed/%s", o.ID.String())
				data := notificationApplication.GetLiveUpdateData(
					o.ID.String(), o.OrderStatus, lang, string(o.OrderType), deepLink, o.CancellationReason,
				)
				for _, dt := range deviceTokens {
					if dt.Platform != "android" {
						continue
					}
					if pushErr := r.FCMClient.SendDataMessage(dt.DeviceToken, data); pushErr != nil {
						zap.L().Warn("failed to re-push live update (language)",
							zap.String("order_id", o.ID.String()), zap.Error(pushErr))
					}
				}
			}
		}
	}
}
