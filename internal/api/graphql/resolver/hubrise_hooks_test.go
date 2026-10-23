package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
)

// spyHooks records what the resolver helpers forward and can fail every hook.
type spyHooks struct {
	err      error
	ready    []uuid.UUID
	statuses []orderDomain.OrderStatus
	history  []uuid.UUID
	avail    map[uuid.UUID]bool
	guarded  [][2]*productDomain.Product
}

func (s *spyHooks) OrderReady(_ context.Context, id uuid.UUID) error {
	s.ready = append(s.ready, id)
	return s.err
}

func (s *spyHooks) OrderStatusChanged(_ context.Context, _ uuid.UUID, st orderDomain.OrderStatus, historyID uuid.UUID) error {
	s.statuses = append(s.statuses, st)
	s.history = append(s.history, historyID)
	return s.err
}

func (s *spyHooks) AvailabilityChanged(_ context.Context, id uuid.UUID, available bool) error {
	if s.avail == nil {
		s.avail = map[uuid.UUID]bool{}
	}
	s.avail[id] = available
	return s.err
}

func (s *spyHooks) ProductGuard(_ context.Context, current, updated *productDomain.Product) error {
	s.guarded = append(s.guarded, [2]*productDomain.Product{current, updated})
	return s.err
}

func TestHubriseHelpersAreNoOpsWithoutHubrise(t *testing.T) {
	r := &Resolver{} // standalone: Hubrise is nil
	ctx := t.Context()
	id := uuid.New()

	assert.NotPanics(t, func() {
		r.OrderReady(ctx, id)
		r.OrderStatusChanged(ctx, id, orderDomain.OrderStatusConfirmed, uuid.New())
		r.AvailabilityChanged(ctx, id, false)
	})
	assert.NoError(t, r.ProductGuard(ctx, &productDomain.Product{}, &productDomain.Product{}), "a standalone restaurant can edit everything")
}

func TestHubriseHelpersForwardToTheHooks(t *testing.T) {
	spy := &spyHooks{}
	r := &Resolver{Hubrise: spy}
	ctx := t.Context()
	order, product, history := uuid.New(), uuid.New(), uuid.New()
	current, updated := &productDomain.Product{ID: product}, &productDomain.Product{ID: product, IsAvailable: true}

	r.OrderReady(ctx, order)
	r.OrderStatusChanged(ctx, order, orderDomain.OrderStatusPreparing, history)
	r.AvailabilityChanged(ctx, product, true)
	require.NoError(t, r.ProductGuard(ctx, current, updated))

	assert.Equal(t, []uuid.UUID{order}, spy.ready)
	assert.Equal(t, []orderDomain.OrderStatus{orderDomain.OrderStatusPreparing}, spy.statuses)
	assert.Equal(t, []uuid.UUID{history}, spy.history)
	assert.Equal(t, map[uuid.UUID]bool{product: true}, spy.avail)
	assert.Equal(t, [][2]*productDomain.Product{{current, updated}}, spy.guarded)
}

func TestHubriseHookFailuresAreLoggedNotReturned(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	boom := errors.New("outbox down")
	spy := &spyHooks{err: boom}
	r := &Resolver{Hubrise: spy}
	ctx := t.Context()

	r.OrderReady(ctx, uuid.New())
	r.OrderStatusChanged(ctx, uuid.New(), orderDomain.OrderStatusCanceled, uuid.Nil)
	r.AvailabilityChanged(ctx, uuid.New(), false)

	assert.Equal(t, 1, logs.FilterMessage("hubrise: order ready hook failed").Len())
	assert.Equal(t, 1, logs.FilterMessage("hubrise: order status hook failed").Len())
	assert.Equal(t, 1, logs.FilterMessage("hubrise: availability hook failed").Len())
	assert.ErrorIs(t, r.ProductGuard(ctx, &productDomain.Product{}, &productDomain.Product{}), boom, "the guard is a veto: its error is returned")
}

func TestNoopHubriseHooks(t *testing.T) {
	var h HubriseHooks = NoopHubriseHooks{}
	ctx := t.Context()

	assert.NoError(t, h.OrderReady(ctx, uuid.New()))
	assert.NoError(t, h.OrderStatusChanged(ctx, uuid.New(), orderDomain.OrderStatusPending, uuid.New()))
	assert.NoError(t, h.AvailabilityChanged(ctx, uuid.New(), true))
	assert.NoError(t, h.ProductGuard(ctx, nil, nil))
}
