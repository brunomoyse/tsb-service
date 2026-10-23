package resolver

import (
	"context"

	"github.com/google/uuid"

	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	orderDomain "tsb-service/internal/modules/order/domain"
)

// HubriseOrderHooks is the HubriseHooks of the order push: it queues an order that became actionable
// and every status change for HubRise. The availability hook and the product guard are the no-ops of
// the embedded NoopHubriseHooks until the composition root layers the catalog and inventory hooks on
// top. Install it as Resolver.Hubrise, and as the status observer of the order service (it has the
// observer's method), only when HubRise is available on the deployment.
type HubriseOrderHooks struct {
	NoopHubriseHooks
	Push *hubriseApplication.OrderPusher
}

var _ HubriseHooks = HubriseOrderHooks{}

// OrderReady queues the push of the order.
func (h HubriseOrderHooks) OrderReady(ctx context.Context, orderID uuid.UUID) error {
	return h.Push.OrderReady(ctx, orderID)
}

// OrderStatusChanged queues the status change. Its signature is the order service's StatusObserver.
func (h HubriseOrderHooks) OrderStatusChanged(ctx context.Context, orderID uuid.UUID, newStatus orderDomain.OrderStatus, historyID uuid.UUID) error {
	return h.Push.OrderStatusChanged(ctx, orderID, string(newStatus), historyID)
}
