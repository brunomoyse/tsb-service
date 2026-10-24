package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/logging"
)

// OrderPusher turns shop events into outbox rows: an order that became actionable (OrderReady) and an
// order whose status changed (OrderStatusChanged). Both are quick (a few queries, never a network
// call) and are safe to call when the integration is off: they return before touching anything.
//
// OrderStatusChanged has the signature of the order service's StatusObserver (the order status type
// is a string type, so the wiring adapts it in one line), and OrderReady that of the resolver hook.
type OrderPusher struct {
	orders domain.OrderLinkRepository
	outbox domain.OutboxRepository
	source domain.OrderSource
	clock  domain.Clock
	conns  domain.ConnectionRepository
	wake   func()
}

// PusherDeps are the collaborators of an OrderPusher.
type PusherDeps struct {
	Connections domain.ConnectionRepository
	Orders      domain.OrderLinkRepository
	Outbox      domain.OutboxRepository
	Source      domain.OrderSource
	Clock       domain.Clock
}

// NewOrderPusher returns the pusher.
func NewOrderPusher(d PusherDeps) *OrderPusher {
	return &OrderPusher{
		orders: d.Orders, outbox: d.Outbox, source: d.Source, clock: d.Clock,
		conns: d.Connections, wake: func() {},
	}
}

// SetWake installs the function called after a row was queued, to wake the outbox worker early. It
// is an optimisation only: the worker also polls.
func (p *OrderPusher) SetWake(wake func()) {
	if wake == nil {
		wake = func() {}
	}
	p.wake = wake
}

// connection returns the connection row, or nil when the installation never connected. It reads the
// database on every call, on purpose: a cached row would let the hooks queue rows for a while after
// the integration was switched off (on this replica or, worse, on another one), and the order hooks
// are rare enough for one primary key read to cost nothing.
func (p *OrderPusher) connection(ctx context.Context) (*domain.Connection, error) {
	conn, err := p.conns.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return conn, err
}

// OrderReady queues the push of an order that became actionable: a cash order that was placed or an
// online order whose payment was confirmed. It is idempotent: a second call for the same order (a
// redelivered Mollie webhook) queues nothing. Nothing happens unless the integration is connected,
// enabled and pushing orders.
func (p *OrderPusher) OrderReady(ctx context.Context, orderID uuid.UUID) error {
	conn, err := p.connection(ctx)
	if err != nil {
		return fmt.Errorf("hubrise: read connection: %w", err)
	}
	if conn == nil || !conn.Active() || !conn.PushOrders {
		return nil
	}
	_, err = p.enqueueCreate(ctx, orderID)
	return err
}

// enqueueCreate snapshots the order and queues its order_create, once. It reports whether a row was
// queued. Test orders and orders that already ended are never pushed.
func (p *OrderPusher) enqueueCreate(ctx context.Context, orderID uuid.UUID) (bool, error) {
	if _, err := p.orders.Get(ctx, orderID); err == nil {
		return false, nil // already known: nothing to snapshot
	} else if !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	snap, err := p.source.Snapshot(ctx, orderID)
	if err != nil {
		return false, fmt.Errorf("hubrise: snapshot order: %w", err)
	}
	if snap.IsTest || domain.IsTerminalLocal(snap.Status) {
		return false, nil
	}
	payload, err := BuildCreatePayload(snap).Marshal()
	if err != nil {
		return false, fmt.Errorf("hubrise: encode order payload: %w", err)
	}
	enqueued, err := p.orders.EnqueueCreate(ctx, orderID, payload, p.clock.Now())
	if err != nil {
		return false, err
	}
	if enqueued {
		logging.FromContext(ctx).Info("hubrise: order queued for push", zap.String("order_id", orderID.String()))
		p.wake()
	}
	return enqueued, nil
}

// OrderStatusChanged queues the status PATCH of a pushed order. Each transition gets its own row,
// frozen at the target status, so a sequence CONFIRMED, PREPARING, CONFIRMED is sent as three
// PATCHes in order. historyID is the id of the status history row (uuid.Nil when it could not be
// written), which makes the dedupe key of the row unique per transition.
func (p *OrderPusher) OrderStatusChanged(ctx context.Context, orderID uuid.UUID, newStatus string, historyID uuid.UUID) error {
	conn, err := p.connection(ctx)
	if err != nil {
		return fmt.Errorf("hubrise: read connection: %w", err)
	}
	if conn == nil || !conn.Active() || !conn.SyncStatus {
		return nil
	}
	if RemoteStatusFor(newStatus) == "" {
		return nil // PENDING and FAILED are never pushed
	}
	link, err := p.orders.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil // never pushed (a test order, or HubRise was off when it was placed)
	}
	if err != nil {
		return err
	}
	if link.PushState != domain.PushPending && link.PushState != domain.PushSent {
		return nil // parked or skipped: HubRise never got the order
	}
	return p.enqueueStatus(ctx, orderID, newStatus, fmt.Sprintf("order:%s:status:%s", orderID, keyOf(historyID)))
}

// keyOf makes the unique part of a status row's dedupe key.
func keyOf(historyID uuid.UUID) string {
	if historyID == uuid.Nil {
		return "x" + uuid.NewString() // the history row is missing: still unique
	}
	return historyID.String()
}

// enqueueStatus inserts an order_status row for the local status.
func (p *OrderPusher) enqueueStatus(ctx context.Context, orderID uuid.UUID, status, dedupeKey string) error {
	payload, err := json.Marshal(domain.StatusPayload{Status: status})
	if err != nil {
		return fmt.Errorf("hubrise: encode status payload: %w", err)
	}
	item := domain.NewOutboxItem(domain.OutboxOrderStatus, &orderID, dedupeKey, payload, p.clock.Now())
	item.CreatedAt = item.NextAttemptAt
	if _, _, err := p.outbox.Enqueue(ctx, item); err != nil {
		return err
	}
	p.wake()
	return nil
}
