package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// ErrNotRefundable is what an OrderTransitioner returns when it could not cancel an order because
// its payment cannot be refunded through Mollie. Retrying never helps: staff refund by hand.
var ErrNotRefundable = errors.New("hubrise: the payment of the order cannot be refunded")

// OrderChange is what HubRise asks of a local order. A zero field leaves the order's value alone.
type OrderChange struct {
	// Status is the local status to move to.
	Status string
	// EstimatedReadyTime is the new estimated ready time (HubRise confirmed_time).
	EstimatedReadyTime *time.Time
}

// OrderTransitioner applies a change to a local order exactly as the dashboard would: payment
// settlement on a cancel, coupon rollback, e-mail, push notifications, live activities and the
// orderUpdated topics, but without reporting the change back to HubRise. The resolver package
// implements it on top of Resolver.ApplyOrderUpdate and the HubRise origin marker, so the hubrise
// module imports no other business module.
type OrderTransitioner interface {
	Transition(ctx context.Context, orderID uuid.UUID, change OrderChange) error
}

// errOrderBusy means another process is applying a change to the same order right now.
var errOrderBusy = errors.New("hubrise: the order is being updated elsewhere")

// errStatusQueued means a status PATCH of the order is still waiting to leave: the local status is
// ahead of HubRise, so what HubRise says now is older than the local order and must wait.
var errStatusQueued = errors.New("hubrise: a status change of the order is waiting to be sent")

// orderLeaseTTL bounds how long the per order lease of an inbound change is held. A cancel with a
// Mollie refund is the slowest thing it covers.
const orderLeaseTTL = 2 * time.Minute

// StatusSyncDeps are the collaborators of a StatusSync.
type StatusSyncDeps struct {
	Connections  domain.ConnectionRepository
	Orders       domain.OrderLinkRepository
	Source       domain.OrderSource
	Leases       domain.LeaseRepository
	Transitioner OrderTransitioner
	Clock        domain.Clock
	// Holder identifies this process in the lease table; empty takes a random one.
	Holder string
}

// StatusSync applies the state of an order read from HubRise to the local order (section 7 of the
// plan). It is the one place that knows the inbound mapping, the terminal-status and refund rules,
// shared by the event processor and by the reconciler (it implements RemoteOrderHandler).
type StatusSync struct {
	d      StatusSyncDeps
	holder string
}

var _ RemoteOrderHandler = (*StatusSync)(nil)

// NewStatusSync returns the status sync.
func NewStatusSync(d StatusSyncDeps) *StatusSync {
	holder := d.Holder
	if holder == "" {
		holder = uuid.NewString()
	}
	return &StatusSync{d: d, holder: holder}
}

// ApplyRemote applies an order the reconciler read back from HubRise. It does nothing while the
// integration is paused or does not sync statuses, and when another process holds the order (the
// next run tries again).
func (s *StatusSync) ApplyRemote(ctx context.Context, orderID uuid.UUID, remote *hubrise.Order) error {
	conn, err := s.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !conn.Active() || !conn.SyncStatus {
		return nil
	}
	link, err := s.d.Orders.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.apply(ctx, conn, link, remote, nil)
	if errors.Is(err, errOrderBusy) || errors.Is(err, errStatusQueued) {
		return nil
	}
	return err
}

// apply serializes the change per order across replicas and then applies it.
func (s *StatusSync) apply(ctx context.Context, conn *domain.Connection, link *domain.OrderLink, remote *hubrise.Order, eventAt *time.Time) error {
	name := "order-status:" + link.OrderID.String()
	holder := s.holder + ":" + uuid.NewString() // one holder per call: a process can race itself
	ok, err := s.d.Leases.Acquire(ctx, name, holder, s.d.Clock.Now(), orderLeaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return errOrderBusy
	}
	defer func() {
		if err := s.d.Leases.Release(context.WithoutCancel(ctx), name, holder); err != nil {
			logging.FromContext(ctx).Error("hubrise: cannot release an order lease", zap.String("order_id", link.OrderID.String()), zap.Error(err))
		}
	}()
	return s.applyLocked(ctx, conn, link, remote, eventAt)
}

// applyLocked records what HubRise says and moves the local order accordingly. It gives way to a
// status PATCH of ours that is still queued (errStatusQueued): the remote state is older than the
// local one, and the processor or the reconciler looks again once the PATCH has gone out. The
// remote status is recorded first: it is a fact about HubRise whatever happens to the local order, and it keeps the
// outbound side from sending an older status back over it.
func (s *StatusSync) applyLocked(ctx context.Context, conn *domain.Connection, link *domain.OrderLink, remote *hubrise.Order, eventAt *time.Time) error {
	orderID := link.OrderID
	log := logging.FromContext(ctx).With(zap.String("order_id", orderID.String()))
	status := remote.Status.Canonical()
	queued, err := s.d.Orders.HasQueuedStatus(ctx, orderID)
	if err != nil {
		return err
	}
	if queued {
		return errStatusQueued
	}
	if err := s.d.Orders.RecordRemoteStatus(ctx, orderID, string(status), nil); err != nil {
		return err
	}
	if status != hubrise.StatusNew && link.NeedsAttention != nil && *link.NeedsAttention == domain.AttentionNotAccepted {
		if err := s.d.Orders.ClearAttention(ctx, orderID, domain.AttentionNotAccepted); err != nil {
			return err
		}
	}
	state, err := s.d.Source.State(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil // the order is gone, the link goes with it
	}
	if err != nil {
		return err
	}

	var change OrderChange
	switch status {
	case hubrise.StatusDeliveryFailed:
		log.Error("hubrise: the delivery failed, staff must decide", zap.String("hubrise_order_id", remote.ID))
		if err := s.d.Orders.SetNeedsAttention(ctx, orderID, domain.AttentionDeliveryFailed); err != nil {
			return err
		}
	case hubrise.StatusCancelled, hubrise.StatusRejected:
		if state.Status != domain.LocalCancelled {
			change.Status, err = s.cancelDecision(ctx, conn, orderID, state)
			if err != nil {
				return err
			}
		}
	default:
		if want := LocalStatusFor(status, state.Type); want != "" && want != state.Status {
			change.Status = want
		}
	}
	if change.Status != "" && domain.IsTerminalLocal(state.Status) {
		// The shop already finished the order: a late or contrary HubRise status never reopens it.
		log.Warn("hubrise: a remote status would move a finished order, kept as it is",
			zap.String("local", state.Status), zap.String("remote", string(status)))
		if err := s.d.Orders.FlagAttention(ctx, orderID, domain.AttentionStatusConflict); err != nil {
			return err
		}
		change.Status = ""
	}
	if t := readyTimeChange(link, remote, state, status, change.Status); t != nil {
		change.EstimatedReadyTime = t
	}

	if change != (OrderChange{}) {
		if err := s.d.Transitioner.Transition(ctx, orderID, change); err != nil {
			if change.Status == domain.LocalCancelled && errors.Is(err, ErrNotRefundable) {
				s.flagCancelFailed(ctx, orderID, err) // retrying never helps: staff refund by hand
			}
			return err
		}
		log.Info("hubrise: remote change applied", zap.String("status", change.Status), zap.String("remote", string(status)))
	}
	if ct := remote.ConfirmedTime; ct != nil && !ct.IsZero() && !sameSecond(link.LastRemoteConfirmedTime, *ct) {
		if err := s.d.Orders.RecordRemoteConfirmedTime(ctx, orderID, *ct); err != nil {
			return err
		}
	}
	if eventAt != nil {
		return s.d.Orders.RecordRemoteStatus(ctx, orderID, string(status), eventAt)
	}
	return nil
}

// cancelDecision decides what a remote cancel does to a local order that is not cancelled yet. It
// returns the status to move to, or "" when the order must be left alone: a paid online order whose
// connection does not refund by itself is flagged for staff instead, because an EPOS mis-tap must
// never move money.
func (s *StatusSync) cancelDecision(ctx context.Context, conn *domain.Connection, orderID uuid.UUID, state *domain.OrderState) (string, error) {
	if domain.IsTerminalLocal(state.Status) || !state.OnlinePaid || conn.AutoRefundOnCancel {
		return domain.LocalCancelled, nil // a terminal order is refused by the caller
	}
	logging.FromContext(ctx).Error("hubrise: an online paid order was cancelled at HubRise, staff must cancel it to refund the customer",
		zap.String("order_id", orderID.String()))
	return "", s.d.Orders.SetNeedsAttention(ctx, orderID, domain.AttentionRemoteCancelledPaid)
}

// sameSecond reports whether a is set and equals b to the second (HubRise times have that precision).
func sameSecond(a *time.Time, b time.Time) bool {
	return a != nil && a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}

// readyTimeChange returns the new estimated ready time when HubRise changed the confirmed time of an
// order that stays alive. A confirmed time already seen (the link remembers it) is not applied
// again: the ready time may have been edited locally since, and local edits are never sent back.
func readyTimeChange(link *domain.OrderLink, remote *hubrise.Order, state *domain.OrderState, status hubrise.OrderStatus, newStatus string) *time.Time {
	if remote.ConfirmedTime == nil || remote.ConfirmedTime.IsZero() || status.Final() ||
		domain.IsTerminalLocal(state.Status) || newStatus == domain.LocalCancelled {
		return nil
	}
	if sameSecond(link.LastRemoteConfirmedTime, *remote.ConfirmedTime) || sameSecond(state.EstimatedReadyTime, *remote.ConfirmedTime) {
		return nil
	}
	t := *remote.ConfirmedTime
	return &t
}

// flagCancelFailed flags an order whose remote cancel could not be applied.
func (s *StatusSync) flagCancelFailed(ctx context.Context, orderID uuid.UUID, err error) {
	logging.FromContext(ctx).Error("hubrise: cannot cancel the order cancelled at HubRise",
		zap.String("order_id", orderID.String()), zap.Error(err))
	if ferr := s.d.Orders.SetNeedsAttention(ctx, orderID, domain.AttentionRemoteCancelFailed); ferr != nil {
		logging.FromContext(ctx).Error("hubrise: cannot flag the order", zap.String("order_id", orderID.String()), zap.Error(ferr))
	}
}
