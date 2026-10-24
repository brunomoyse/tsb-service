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
	productApplication "tsb-service/internal/modules/product/application"
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
	groups   [][2]*productDomain.ProductChoiceGroup
	choices  [][2]*productDomain.ProductChoice
	menu     []menuEdit
}

type menuEdit struct {
	kind productApplication.MenuKind
	id   uuid.UUID
}

func (s *spyHooks) MenuChanged(_ context.Context, kind productApplication.MenuKind, id uuid.UUID) error {
	s.menu = append(s.menu, menuEdit{kind, id})
	return s.err
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

func (s *spyHooks) ChoiceGroupGuard(_ context.Context, current, updated *productDomain.ProductChoiceGroup) error {
	s.groups = append(s.groups, [2]*productDomain.ProductChoiceGroup{current, updated})
	return s.err
}

func (s *spyHooks) ChoiceGuard(_ context.Context, current, updated *productDomain.ProductChoice) error {
	s.choices = append(s.choices, [2]*productDomain.ProductChoice{current, updated})
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
		r.MenuChanged(ctx, productApplication.MenuProduct, id)
	})
	assert.NoError(t, r.ProductGuard(ctx, &productDomain.Product{}, &productDomain.Product{}), "a standalone restaurant can edit everything")
	assert.NoError(t, r.ChoiceGroupGuard(ctx, &productDomain.ProductChoiceGroup{}, nil))
	assert.NoError(t, r.ChoiceGuard(ctx, nil, &productDomain.ProductChoice{}))
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
	r.MenuChanged(ctx, productApplication.MenuChoice, product)
	require.NoError(t, r.ProductGuard(ctx, current, updated))
	group, choice := &productDomain.ProductChoiceGroup{ID: uuid.New()}, &productDomain.ProductChoice{ID: uuid.New()}
	require.NoError(t, r.ChoiceGroupGuard(ctx, nil, group))
	require.NoError(t, r.ChoiceGuard(ctx, choice, nil))

	assert.Equal(t, []uuid.UUID{order}, spy.ready)
	assert.Equal(t, []orderDomain.OrderStatus{orderDomain.OrderStatusPreparing}, spy.statuses)
	assert.Equal(t, []uuid.UUID{history}, spy.history)
	assert.Equal(t, map[uuid.UUID]bool{product: true}, spy.avail)
	assert.Equal(t, []menuEdit{{productApplication.MenuChoice, product}}, spy.menu)
	assert.Equal(t, [][2]*productDomain.Product{{current, updated}}, spy.guarded)
	assert.Equal(t, [][2]*productDomain.ProductChoiceGroup{{nil, group}}, spy.groups)
	assert.Equal(t, [][2]*productDomain.ProductChoice{{choice, nil}}, spy.choices)
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
	r.MenuChanged(ctx, productApplication.MenuChoiceGroup, uuid.New())

	assert.Equal(t, 1, logs.FilterMessage("hubrise: menu changed hook failed").Len())
	assert.Equal(t, 1, logs.FilterMessage("hubrise: order ready hook failed").Len())
	assert.Equal(t, 1, logs.FilterMessage("hubrise: order status hook failed").Len())
	assert.Equal(t, 1, logs.FilterMessage("hubrise: availability hook failed").Len())
	assert.ErrorIs(t, r.ProductGuard(ctx, &productDomain.Product{}, &productDomain.Product{}), boom, "the guard is a veto: its error is returned")
	assert.ErrorIs(t, r.ChoiceGroupGuard(ctx, nil, &productDomain.ProductChoiceGroup{}), boom)
	assert.ErrorIs(t, r.ChoiceGuard(ctx, nil, &productDomain.ProductChoice{}), boom)
}

func TestNoopHubriseHooks(t *testing.T) {
	var h HubriseHooks = NoopHubriseHooks{}
	ctx := t.Context()

	assert.NoError(t, h.OrderReady(ctx, uuid.New()))
	assert.NoError(t, h.OrderStatusChanged(ctx, uuid.New(), orderDomain.OrderStatusPending, uuid.New()))
	assert.NoError(t, h.AvailabilityChanged(ctx, uuid.New(), true))
	assert.NoError(t, h.MenuChanged(ctx, productApplication.MenuProduct, uuid.New()))
	assert.NoError(t, h.ProductGuard(ctx, nil, nil))
	assert.NoError(t, h.ChoiceGroupGuard(ctx, nil, nil))
	assert.NoError(t, h.ChoiceGuard(ctx, nil, nil))
}

// HubriseHooksSet sends every hook to the part of the integration that owns it and to no other.
func TestHubriseHooksSetRoutesEachHookToItsOwner(t *testing.T) {
	order, inventory, catalog, publish := &spyHooks{}, &spyHooks{}, &spyHooks{}, &spyHooks{}
	set := HubriseHooksSet{Order: order, Inventory: inventory, Catalog: catalog, Publish: publish}
	ctx := t.Context()
	id, history := uuid.New(), uuid.New()
	product := &productDomain.Product{}

	require.NoError(t, set.OrderReady(ctx, id))
	require.NoError(t, set.OrderStatusChanged(ctx, id, orderDomain.OrderStatusConfirmed, history))
	require.NoError(t, set.AvailabilityChanged(ctx, id, false))
	require.NoError(t, set.MenuChanged(ctx, productApplication.MenuProduct, id))
	require.NoError(t, set.ProductGuard(ctx, product, product))
	require.NoError(t, set.ChoiceGroupGuard(ctx, nil, &productDomain.ProductChoiceGroup{}))
	require.NoError(t, set.ChoiceGuard(ctx, &productDomain.ProductChoice{}, nil))

	assert.Equal(t, []uuid.UUID{id}, order.ready)
	assert.Equal(t, []orderDomain.OrderStatus{orderDomain.OrderStatusConfirmed}, order.statuses)
	assert.Equal(t, []uuid.UUID{history}, order.history)
	assert.Equal(t, map[uuid.UUID]bool{id: false}, inventory.avail)
	assert.Equal(t, []menuEdit{{productApplication.MenuProduct, id}}, publish.menu)
	assert.Empty(t, order.menu)
	assert.Empty(t, inventory.menu)
	assert.Empty(t, catalog.menu)
	assert.Len(t, catalog.guarded, 1)
	assert.Len(t, catalog.groups, 1)
	assert.Len(t, catalog.choices, 1)
	for name, other := range map[string]*spyHooks{"order": order, "inventory": inventory, "catalog": catalog} {
		if name != "order" {
			assert.Empty(t, other.ready, name)
			assert.Empty(t, other.statuses, name)
		}
		if name != "inventory" {
			assert.Empty(t, other.avail, name)
		}
		if name != "catalog" {
			assert.Empty(t, other.guarded, name)
			assert.Empty(t, other.groups, name)
			assert.Empty(t, other.choices, name)
		}
	}

	t.Run("a refusal of the owner comes back", func(t *testing.T) {
		boom := errors.New("boom")
		failing := HubriseHooksSet{Order: &spyHooks{err: boom}, Inventory: &spyHooks{err: boom}, Catalog: &spyHooks{err: boom}, Publish: &spyHooks{err: boom}}
		assert.ErrorIs(t, failing.MenuChanged(ctx, productApplication.MenuProduct, id), boom)
		assert.ErrorIs(t, failing.ProductGuard(ctx, product, product), boom)
		assert.ErrorIs(t, failing.AvailabilityChanged(ctx, id, true), boom)
		assert.ErrorIs(t, failing.OrderReady(ctx, id), boom)
	})

	t.Run("a Set without a publish part ignores menu edits", func(t *testing.T) {
		assert.NoError(t, HubriseHooksSet{Order: order}.MenuChanged(ctx, productApplication.MenuProduct, id))
	})
}
