package resolver

import (
	"context"

	"github.com/google/uuid"

	orderDomain "tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
)

// HubriseHooksSet is the HubriseHooks of a deployment where HubRise is available: each hook goes to
// the part of the integration that owns it. The order hooks queue the push of an order and its
// status changes, the inventory hook queues a sold-out toggle, and the catalog hooks cast the
// ownership vetoes. The parts embed NoopHubriseHooks (each implements only its own hooks), so this
// is the one place that puts them together; the composition root installs it as Resolver.Hubrise
// and as the status observer of the order service.
type HubriseHooksSet struct {
	Order     HubriseHooks
	Inventory HubriseHooks
	Catalog   HubriseHooks
	// Publish follows the menu edits for the catalog publisher. Nil (a Set built before publishing
	// existed) is the same as NoopHubriseHooks.
	Publish HubriseHooks
}

var _ HubriseHooks = HubriseHooksSet{}

func (s HubriseHooksSet) OrderReady(ctx context.Context, orderID uuid.UUID) error {
	return s.Order.OrderReady(ctx, orderID)
}

func (s HubriseHooksSet) OrderStatusChanged(ctx context.Context, orderID uuid.UUID, newStatus orderDomain.OrderStatus, historyID uuid.UUID) error {
	return s.Order.OrderStatusChanged(ctx, orderID, newStatus, historyID)
}

func (s HubriseHooksSet) AvailabilityChanged(ctx context.Context, productID uuid.UUID, available bool) error {
	return s.Inventory.AvailabilityChanged(ctx, productID, available)
}

func (s HubriseHooksSet) ProductGuard(ctx context.Context, current, updated *productDomain.Product) error {
	return s.Catalog.ProductGuard(ctx, current, updated)
}

func (s HubriseHooksSet) ChoiceGroupGuard(ctx context.Context, current, updated *productDomain.ProductChoiceGroup) error {
	return s.Catalog.ChoiceGroupGuard(ctx, current, updated)
}

func (s HubriseHooksSet) ChoiceGuard(ctx context.Context, current, updated *productDomain.ProductChoice) error {
	return s.Catalog.ChoiceGuard(ctx, current, updated)
}

func (s HubriseHooksSet) MenuChanged(ctx context.Context, kind productApplication.MenuKind, id uuid.UUID) error {
	if s.Publish == nil {
		return nil
	}
	return s.Publish.MenuChanged(ctx, kind, id)
}
