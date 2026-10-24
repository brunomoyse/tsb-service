package resolver

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

// HubriseOrderTransitioner applies a change that came from HubRise to a local order: the same
// orchestration as the dashboard's updateOrder (payment settlement on a cancel, coupon rollback,
// e-mail, push, live activities, orderUpdated topics), marked with the HubRise origin so that the
// order service does not report it back to HubRise.
type HubriseOrderTransitioner struct {
	Resolver *Resolver
}

var _ hubriseApplication.OrderTransitioner = HubriseOrderTransitioner{}

// Transition moves the order. The context is the one of a background job: it runs as the system
// (admin database pool, no user), not on behalf of a person. An order that cannot be cancelled
// because its payment cannot be refunded comes back as hubriseApplication.ErrNotRefundable.
func (t HubriseOrderTransitioner) Transition(ctx context.Context, orderID uuid.UUID, change hubriseApplication.OrderChange) error {
	ctx = utils.WithOrigin(utils.SetIsAdmin(ctx, true), utils.OriginHubrise)
	input := model.UpdateOrderInput{EstimatedReadyTime: change.EstimatedReadyTime}
	if change.Status != "" {
		status := orderDomain.OrderStatus(change.Status)
		input.Status = &status
		if status == orderDomain.OrderStatusCanceled {
			reason := orderDomain.OrderCancellationReasonOther
			input.CancellationReason = &reason
		}
	}
	_, err := t.Resolver.ApplyOrderUpdate(ctx, orderID, input)
	if appErr, ok := apperr.From(err); ok && appErr.Code == apperr.CodePaymentNotRefundable {
		return fmt.Errorf("%w: %w", hubriseApplication.ErrNotRefundable, err)
	}
	return err
}
