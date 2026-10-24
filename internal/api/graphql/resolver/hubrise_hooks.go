package resolver

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	orderDomain "tsb-service/internal/modules/order/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// HubriseHooks is what the resolvers (and the services wired next to them) call to let the HubRise
// integration follow the shop. It is defined here, by its consumer, so the HubRise module is never
// imported by the code that calls it. Resolver.Hubrise is nil unless HubRise is configured on the
// deployment, and every caller goes through the nil-safe helpers below: a restaurant without HubRise
// runs no HubRise code at all.
//
// The hooks are notifications on the shop's own business (an order that can be pushed, a status
// that changed, a product made unavailable). They are best effort and quick (a queue insert): the
// shop never waits for HubRise and a failure never fails the shop's own operation. ProductGuard is
// the one exception, a veto the integration is allowed to cast on an edit.
type HubriseHooks interface {
	// OrderReady says an order became actionable (a cash order was placed, an online one was paid)
	// and can be pushed.
	OrderReady(ctx context.Context, orderID uuid.UUID) error
	// OrderStatusChanged says an order moved to newStatus; historyID is the order_status_history
	// row of that transition (uuid.Nil when it could not be recorded). Its signature is the one of
	// orderApplication.StatusObserver, so the implementation can be installed as the observer of
	// the order service, the single chokepoint of status changes.
	OrderStatusChanged(ctx context.Context, orderID uuid.UUID, newStatus orderDomain.OrderStatus, historyID uuid.UUID) error
	// AvailabilityChanged says staff made a product available or sold out in the shop.
	AvailabilityChanged(ctx context.Context, productID uuid.UUID, available bool) error
	// MenuChanged says a product, a choice group or a choice was written (or its image replaced). Its
	// signature is the one of productApplication.MenuObserver, so the implementation can be
	// installed as the observer of the product service, the chokepoint of menu writes. It marks the
	// catalog publisher dirty when the dashboard owns the menu, and does nothing otherwise.
	MenuChanged(ctx context.Context, kind productApplication.MenuKind, id uuid.UUID) error
	// ProductGuard may refuse an edit of a product whose fields HubRise owns.
	ProductGuard(ctx context.Context, current, updated *productDomain.Product) error
	// ChoiceGroupGuard may refuse the creation (current is nil), the edit or the deletion (updated is
	// nil) of a choice group of a product whose options HubRise owns.
	ChoiceGroupGuard(ctx context.Context, current, updated *productDomain.ProductChoiceGroup) error
	// ChoiceGuard is ChoiceGroupGuard for a choice.
	ChoiceGuard(ctx context.Context, current, updated *productDomain.ProductChoice) error
}

// NoopHubriseHooks is the HubriseHooks that does nothing: what a deployment without HubRise behaves like.
type NoopHubriseHooks struct{}

func (NoopHubriseHooks) OrderReady(context.Context, uuid.UUID) error { return nil }

func (NoopHubriseHooks) OrderStatusChanged(context.Context, uuid.UUID, orderDomain.OrderStatus, uuid.UUID) error {
	return nil
}

func (NoopHubriseHooks) AvailabilityChanged(context.Context, uuid.UUID, bool) error { return nil }

func (NoopHubriseHooks) MenuChanged(context.Context, productApplication.MenuKind, uuid.UUID) error {
	return nil
}

func (NoopHubriseHooks) ProductGuard(context.Context, *productDomain.Product, *productDomain.Product) error {
	return nil
}

func (NoopHubriseHooks) ChoiceGroupGuard(context.Context, *productDomain.ProductChoiceGroup, *productDomain.ProductChoiceGroup) error {
	return nil
}

func (NoopHubriseHooks) ChoiceGuard(context.Context, *productDomain.ProductChoice, *productDomain.ProductChoice) error {
	return nil
}

var _ HubriseHooks = NoopHubriseHooks{}

// OrderReady tells HubRise an order can be pushed. It does nothing when HubRise is not configured,
// and a failure is logged, never returned: the order is already placed.
func (r *Resolver) OrderReady(ctx context.Context, orderID uuid.UUID) {
	if r.Hubrise == nil {
		return
	}
	if err := r.Hubrise.OrderReady(ctx, orderID); err != nil {
		logging.FromContext(ctx).Error("hubrise: order ready hook failed", zap.String("order_id", orderID.String()), zap.Error(err))
	}
}

// OrderStatusChanged tells HubRise an order changed status, with the same rules as OrderReady.
func (r *Resolver) OrderStatusChanged(ctx context.Context, orderID uuid.UUID, newStatus orderDomain.OrderStatus, historyID uuid.UUID) {
	if r.Hubrise == nil {
		return
	}
	if err := r.Hubrise.OrderStatusChanged(ctx, orderID, newStatus, historyID); err != nil {
		logging.FromContext(ctx).Error("hubrise: order status hook failed",
			zap.String("order_id", orderID.String()), zap.String("status", string(newStatus)), zap.Error(err))
	}
}

// AvailabilityChanged tells HubRise a product was made available or sold out, with the same rules
// as OrderReady.
func (r *Resolver) AvailabilityChanged(ctx context.Context, productID uuid.UUID, available bool) {
	if r.Hubrise == nil {
		return
	}
	if err := r.Hubrise.AvailabilityChanged(ctx, productID, available); err != nil {
		logging.FromContext(ctx).Error("hubrise: availability hook failed",
			zap.String("product_id", productID.String()), zap.Bool("available", available), zap.Error(err))
	}
}

// MenuChanged tells HubRise a menu item was written, with the same rules as OrderReady. Writes made
// through the product service already reach the hook through its observer; this is for the changes
// that bypass it (a product image replaced by the resolver).
func (r *Resolver) MenuChanged(ctx context.Context, kind productApplication.MenuKind, id uuid.UUID) {
	if r.Hubrise == nil {
		return
	}
	if err := r.Hubrise.MenuChanged(ctx, kind, id); err != nil {
		logging.FromContext(ctx).Error("hubrise: menu changed hook failed",
			zap.String("kind", string(kind)), zap.String("id", id.String()), zap.Error(err))
	}
}

// ProductGuard returns the integration's veto on a product edit, or nil: always nil when HubRise is
// not configured, so a standalone restaurant can edit everything.
func (r *Resolver) ProductGuard(ctx context.Context, current, updated *productDomain.Product) error {
	if r.Hubrise == nil {
		return nil
	}
	return r.Hubrise.ProductGuard(ctx, current, updated)
}

// PublishProductUpdated publishes the current state of a product on the productUpdated topic, so
// open dashboards and menus follow a change the shop did not make through a mutation (an inventory
// sync that sold a product out). A failure is logged: the change itself is already stored.
func (r *Resolver) PublishProductUpdated(ctx context.Context, productID uuid.UUID) {
	prod, err := r.ProductService.GetProduct(ctx, productID)
	if err != nil {
		logging.FromContext(ctx).Warn("hubrise: cannot publish a product update",
			zap.String("product_id", productID.String()), zap.Error(err))
		return
	}
	r.Broker.Publish("productUpdated", ToGQLProduct(prod, utils.GetLang(ctx)))
}

// ChoiceGroupGuard returns the integration's veto on a choice group change, or nil: always nil when
// HubRise is not configured.
func (r *Resolver) ChoiceGroupGuard(ctx context.Context, current, updated *productDomain.ProductChoiceGroup) error {
	if r.Hubrise == nil {
		return nil
	}
	return r.Hubrise.ChoiceGroupGuard(ctx, current, updated)
}

// ChoiceGuard returns the integration's veto on a choice change, with the same rules.
func (r *Resolver) ChoiceGuard(ctx context.Context, current, updated *productDomain.ProductChoice) error {
	if r.Hubrise == nil {
		return nil
	}
	return r.Hubrise.ChoiceGuard(ctx, current, updated)
}
