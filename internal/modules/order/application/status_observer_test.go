package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"tsb-service/internal/modules/order/domain"
	"tsb-service/pkg/utils"
)

// recordingObserver remembers every status change it is told about; err and panics make it misbehave.
type recordingObserver struct {
	mu    sync.Mutex
	calls []observed
	err   error
	boom  bool
}

type observed struct {
	orderID   uuid.UUID
	status    domain.OrderStatus
	historyID uuid.UUID
	origin    utils.Origin
}

func (o *recordingObserver) OrderStatusChanged(ctx context.Context, orderID uuid.UUID, status domain.OrderStatus, historyID uuid.UUID) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, observed{orderID, status, historyID, utils.GetOrigin(ctx)})
	if o.boom {
		panic("observer bug")
	}
	return o.err
}

func (o *recordingObserver) seen() []observed {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observed(nil), o.calls...)
}

func captureServiceLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)
	return logs
}

func TestStatusObserver(t *testing.T) {
	confirmed, canceled := domain.OrderStatusConfirmed, domain.OrderStatusCanceled

	t.Run("it is told once per transition, with the order, the new status and the history row", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil))
		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil))

		require.Len(t, obs.seen(), 2)
		first := obs.seen()[0]
		assert.Equal(t, repo.order.ID, first.orderID)
		assert.Equal(t, domain.OrderStatusConfirmed, first.status)
		assert.NotEqual(t, uuid.Nil, first.historyID)
		assert.Equal(t, domain.OrderStatusCanceled, obs.seen()[1].status)
		assert.NotEqual(t, first.historyID, obs.seen()[1].historyID, "each transition has its own history row")
		assert.Equal(t, repo.lastHistoryID, obs.seen()[1].historyID, "the id is the one the repository returned")
	})

	t.Run("it runs after the order was saved", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		var persisted domain.OrderStatus
		obs := &probeObserver{probe: func() { persisted = repo.updatedOrder.OrderStatus }}
		svc := NewOrderService(repo, nil)
		svc.SetStatusObserver(obs)

		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil))

		assert.Equal(t, domain.OrderStatusCanceled, persisted)
	})

	t.Run("an update that leaves the status alone is not reported", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusConfirmed, nil)
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)
		eta := time.Now().Add(20 * time.Minute)

		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil), "same status")
		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, nil, &eta, nil), "ready time only")
		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, nil, nil, nil), "nothing")

		assert.Empty(t, obs.seen())
	})

	t.Run("a change that came from HubRise is not echoed back", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		require.NoError(t, svc.UpdateOrder(utils.WithOrigin(t.Context(), utils.OriginHubrise), repo.order.ID, &confirmed, nil, nil))

		assert.Empty(t, obs.seen())
		assert.Equal(t, domain.OrderStatusConfirmed, repo.updatedOrder.OrderStatus, "the order itself still moved")
		assert.Equal(t, []domain.OrderStatus{domain.OrderStatusConfirmed}, repo.history, "and was recorded")
	})

	t.Run("another origin is still reported, with the context it was called with", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		require.NoError(t, svc.UpdateOrder(utils.WithOrigin(t.Context(), utils.Origin("pos")), repo.order.ID, &confirmed, nil, nil))

		require.Len(t, obs.seen(), 1)
		assert.Equal(t, utils.Origin("pos"), obs.seen()[0].origin)
	})

	t.Run("the stale test-order sweep is never reported", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.staleTestOrders = []domain.CancelledOrderRef{{ID: uuid.New(), UserID: uuid.New()}}
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		n, err := svc.CancelStaleTestOrders(t.Context(), time.Minute)

		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Empty(t, obs.seen())
	})

	t.Run("an order created is not a status change", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		_, _, err := svc.CreateOrder(t.Context(), repo.order, nil)

		require.NoError(t, err)
		assert.Empty(t, obs.seen())
	})

	t.Run("a failing update reports nothing", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.updateErr = errors.New("db down")
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		require.Error(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil))

		assert.Empty(t, obs.seen())
	})

	t.Run("an observer that fails never fails the update, the failure is logged", func(t *testing.T) {
		logs := captureServiceLogs(t)
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		svc.SetStatusObserver(&recordingObserver{err: errors.New("outbox down")})

		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil))

		assert.Equal(t, 1, logs.FilterMessage("order status observer failed").Len())
		assert.Equal(t, domain.OrderStatusConfirmed, repo.updatedOrder.OrderStatus)
	})

	t.Run("an observer that panics never fails the update either", func(t *testing.T) {
		logs := captureServiceLogs(t)
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		svc.SetStatusObserver(&recordingObserver{boom: true})

		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil))

		assert.Equal(t, 1, logs.FilterMessage("order status observer panicked").Len())
	})

	t.Run("a history row that could not be written is reported with a nil id", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		repo.historyErr = errors.New("db down")
		svc := NewOrderService(repo, nil)
		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)

		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil))

		require.Len(t, obs.seen(), 1)
		assert.Equal(t, uuid.Nil, obs.seen()[0].historyID)
		assert.Equal(t, domain.OrderStatusConfirmed, obs.seen()[0].status)
	})

	t.Run("there is none by default, and it can be removed", func(t *testing.T) {
		repo := newHistRepo(domain.OrderStatusPending, nil)
		svc := NewOrderService(repo, nil)
		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &confirmed, nil, nil), "no observer: nothing to call")

		obs := &recordingObserver{}
		svc.SetStatusObserver(obs)
		svc.SetStatusObserver(nil)
		require.NoError(t, svc.UpdateOrder(t.Context(), repo.order.ID, &canceled, nil, nil))
		assert.Empty(t, obs.seen())
	})
}

// probeObserver runs a function when it is told about a change.
type probeObserver struct{ probe func() }

func (p *probeObserver) OrderStatusChanged(context.Context, uuid.UUID, domain.OrderStatus, uuid.UUID) error {
	p.probe()
	return nil
}
