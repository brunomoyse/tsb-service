package resolver

import (
	"context"

	"github.com/google/uuid"

	hubriseApplication "tsb-service/internal/modules/hubrise/application"
)

// HubriseInventoryHooks is the HubriseHooks of the sold-out sync: a product made available or sold
// out by staff (the dashboard, or the MCP tools, which go through the same mutation) is queued for
// HubRise, and the sold-out provenance of its link is cleared. The other hooks are the no-ops of the
// embedded NoopHubriseHooks until the composition root layers the order and catalog hooks with it.
// The resolver itself is the ProductNotifier of the inventory service (PublishProductUpdated), so a
// product HubRise sells out reaches the open dashboards without any hook.
type HubriseInventoryHooks struct {
	NoopHubriseHooks
	Inventory *hubriseApplication.InventoryService
}

var _ HubriseHooks = HubriseInventoryHooks{}

var _ hubriseApplication.ProductNotifier = (*Resolver)(nil)

// AvailabilityChanged queues the new availability of the product for HubRise.
func (h HubriseInventoryHooks) AvailabilityChanged(ctx context.Context, productID uuid.UUID, available bool) error {
	return h.Inventory.AvailabilityChanged(ctx, productID, available)
}
