package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	orderApp "tsb-service/internal/modules/order/application"
)

// DefaultOrderLoader implements OrderPusher's OrderLoader interface
// by delegating to the order + product services. It's a minimal
// bridge — the shape of the HubRise request evolves as we enrich
// order metadata.
type DefaultOrderLoader struct {
	orderService orderApp.OrderService
}

// NewDefaultOrderLoader returns a loader that builds HubRise request
// payloads from the internal order model.
func NewDefaultOrderLoader(orderService orderApp.OrderService) *DefaultOrderLoader {
	return &DefaultOrderLoader{orderService: orderService}
}

// LoadForHubrisePush fetches the order and maps it to a HubRise
// create-order request. It emits a single synthetic line item
// covering the total — enriching with real line items (requires
// joining order_product + product_choices + translations) is a
// follow-up task.
func (l *DefaultOrderLoader) LoadForHubrisePush(
	ctx context.Context,
	orderID uuid.UUID,
) (*domain.HubriseCreateOrderRequest, error) {
	order, _, err := l.orderService.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, fmt.Errorf("order %s not found", orderID)
	}

	serviceType := "collection"
	if string(order.OrderType) == "DELIVERY" {
		serviceType = "delivery"
	}

	items := []domain.HubriseOrderItem{
		{
			ProductName: "Order " + order.ID.String()[:8],
			Price:       order.TotalPrice.String() + " EUR",
			Quantity:    "1",
		},
	}

	return domain.MapOrderToHubrise(order, items, serviceType), nil
}
