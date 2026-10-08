package resolver

// Helper functions for the order resolvers. These live in a non-generated file
// so `gqlgen generate` does not move them into the "WARNING" block at the end of
// order.go (gqlgen relocates any helper methods it finds in the resolver files
// it regenerates).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	notificationApplication "tsb-service/internal/modules/notification/application"
	orderDomain "tsb-service/internal/modules/order/domain"
)

// legacyChoiceQuantity is the line-wide selection quantity that the legacy
// single `choiceId` input stands for. That input means "this choice applies to
// every unit of the line", i.e. it is equivalent to
// selections:[{choiceId, quantity: lineQty}]. Selection quantities are
// line-wide (group min/max are scaled by the line quantity), so the legacy
// choice has to be scaled the same way to price and validate identically to
// the selections input.
func legacyChoiceQuantity(lineQty int64) int {
	return int(lineQty)
}

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

// choiceLoadError classifies a failed choice lookup in createOrder. A choice that no longer exists
// (deleted from the menu since the cart was saved) is the customer's stale basket, an invalid
// selection; any other failure is a server fault. The customer-facing error never carries the
// driver text ("sql: no rows in result set"): it is a clean "not found".
func choiceLoadError(err error, choiceID, productID uuid.UUID) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.Newf(apperr.CodeSelectionInvalid, "choice %s not found", choiceID).
			With("productId", productID.String())
	}
	return fmt.Errorf("failed to retrieve choice %s: %w", choiceID, err)
}

// maxProductRow caps the menu's product rows (myOrderedProducts, popularProducts): a row shows a handful.
const maxProductRow = 24

// productRowLimit is a product row's `first`: the default when absent or not positive, at most maxProductRow.
func productRowLimit(first *int, def int) int {
	if first == nil || *first <= 0 {
		return def
	}
	return min(*first, maxProductRow)
}

func toGQLProductOrderCount(row *orderDomain.ProductOrderCount) *model.ProductOrderCount {
	return &model.ProductOrderCount{ProductID: row.ProductID, OrderCount: row.OrderCount}
}
