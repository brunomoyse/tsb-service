package resolver

import (
	"context"

	"github.com/google/uuid"

	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	productApplication "tsb-service/internal/modules/product/application"
)

// HubrisePublishHooks is the HubriseHooks of the catalog publisher: a menu edit marks the publisher
// dirty when the dashboard owns the menu. The other hooks are the no-ops of the embedded
// NoopHubriseHooks until the composition root layers the order, inventory and catalog hooks with it.
type HubrisePublishHooks struct {
	NoopHubriseHooks
	Publisher *hubriseApplication.CatalogPublisher
}

var _ HubriseHooks = HubrisePublishHooks{}

var _ productApplication.MenuObserver = HubrisePublishHooks{}

// MenuChanged records the edit for the publisher.
func (h HubrisePublishHooks) MenuChanged(ctx context.Context, _ productApplication.MenuKind, _ uuid.UUID) error {
	return h.Publisher.MenuChanged(ctx)
}
