package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

type eventFaults struct {
	domain.EventRepository
	f *faults
}

func (r eventFaults) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, types ...string) ([]domain.Event, error) {
	if err := r.f.hit("Events.Claim"); err != nil {
		return nil, err
	}
	return r.EventRepository.Claim(ctx, now, lease, limit, types...)
}

func (r eventFaults) OldestPendingReceivedAt(ctx context.Context, resource string) (*time.Time, error) {
	if err := r.f.hit("Events.OldestPendingReceivedAt"); err != nil {
		return nil, err
	}
	return r.EventRepository.OldestPendingReceivedAt(ctx, resource)
}

func (r eventFaults) CollapsePending(ctx context.Context, resource string, before, now time.Time) (int64, error) {
	if err := r.f.hit("Events.CollapsePending"); err != nil {
		return 0, err
	}
	return r.EventRepository.CollapsePending(ctx, resource, before, now)
}

func (r eventFaults) MarkDone(ctx context.Context, id string, now time.Time) error {
	if err := r.f.hit("Events.MarkDone"); err != nil {
		return err
	}
	return r.EventRepository.MarkDone(ctx, id, now)
}

func (r eventFaults) MarkIgnored(ctx context.Context, id string, now time.Time, reason string) error {
	if err := r.f.hit("Events.MarkIgnored"); err != nil {
		return err
	}
	return r.EventRepository.MarkIgnored(ctx, id, now, reason)
}

func (r eventFaults) MarkFailed(ctx context.Context, id string, now time.Time, reason string) error {
	if err := r.f.hit("Events.MarkFailed"); err != nil {
		return err
	}
	return r.EventRepository.MarkFailed(ctx, id, now, reason)
}

func (r eventFaults) Defer(ctx context.Context, id string, next time.Time) error {
	if err := r.f.hit("Events.Defer"); err != nil {
		return err
	}
	return r.EventRepository.Defer(ctx, id, next)
}

func (r eventFaults) RetryLater(ctx context.Context, id string, next time.Time, reason string) error {
	if err := r.f.hit("Events.RetryLater"); err != nil {
		return err
	}
	return r.EventRepository.RetryLater(ctx, id, next, reason)
}

type connFaults struct {
	domain.ConnectionRepository
	f *faults
}

func (c connFaults) Get(ctx context.Context) (*domain.Connection, error) {
	if err := c.f.hit("Connections.Get"); err != nil {
		return nil, err
	}
	return c.ConnectionRepository.Get(ctx)
}

func (c connFaults) SetInventoryBaseline(ctx context.Context, at time.Time) error {
	if err := c.f.hit("Connections.SetInventoryBaseline"); err != nil {
		return err
	}
	return c.ConnectionRepository.SetInventoryBaseline(ctx, at)
}

func (c connFaults) RecordAuthError(ctx context.Context, at time.Time, msg string) error {
	if err := c.f.hit("Connections.RecordAuthError"); err != nil {
		return err
	}
	return c.ConnectionRepository.RecordAuthError(ctx, at, msg)
}

type catalogLinkFaults struct {
	domain.CatalogLinkRepository
	f *faults
}

func (l catalogLinkFaults) ListByType(ctx context.Context, t domain.EntityType, retired bool) ([]domain.CatalogLink, error) {
	if err := l.f.hit("Links.ListByType." + string(t)); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.ListByType(ctx, t, retired)
}

func (l catalogLinkFaults) FindByLocal(ctx context.Context, t domain.EntityType, id uuid.UUID) (*domain.CatalogLink, error) {
	if err := l.f.hit("Links.FindByLocal"); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.FindByLocal(ctx, t, id)
}

func (l catalogLinkFaults) RecordStock(ctx context.Context, id int64, stock *decimal.Decimal, until *time.Time, at time.Time) error {
	if err := l.f.hit("Links.RecordStock"); err != nil {
		return err
	}
	return l.CatalogLinkRepository.RecordStock(ctx, id, stock, until, at)
}

func (l catalogLinkFaults) ClearSoldOut(ctx context.Context, id int64) error {
	if err := l.f.hit("Links.ClearSoldOut"); err != nil {
		return err
	}
	return l.CatalogLinkRepository.ClearSoldOut(ctx, id)
}

func (l catalogLinkFaults) ListSoldOutExpired(ctx context.Context, now time.Time) ([]domain.CatalogLink, error) {
	if err := l.f.hit("Links.ListSoldOutExpired"); err != nil {
		return nil, err
	}
	return l.CatalogLinkRepository.ListSoldOutExpired(ctx, now)
}

type invOutboxFaults struct {
	domain.OutboxRepository
	f *faults
}

func (o invOutboxFaults) Enqueue(ctx context.Context, i domain.OutboxItem) (int64, bool, error) {
	if err := o.f.hit("Outbox.Enqueue"); err != nil {
		return 0, false, err
	}
	return o.OutboxRepository.Enqueue(ctx, i)
}

func (o invOutboxFaults) ActiveDedupeKeys(ctx context.Context, k domain.OutboxKind) (map[string]bool, error) {
	if err := o.f.hit("Outbox.ActiveDedupeKeys"); err != nil {
		return nil, err
	}
	return o.OutboxRepository.ActiveDedupeKeys(ctx, k)
}

type leaseFaults struct {
	domain.LeaseRepository
	f *faults
}

func (l leaseFaults) Acquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	if err := l.f.hit("Leases.Acquire"); err != nil {
		return false, err
	}
	return l.LeaseRepository.Acquire(ctx, name, holder, now, ttl)
}

func (l leaseFaults) Release(ctx context.Context, name, holder string) error {
	if err := l.f.hit("Leases.Release"); err != nil {
		return err
	}
	return l.LeaseRepository.Release(ctx, name, holder)
}

type storeFaults struct {
	domain.AvailabilityStore
	f *faults
	// vanish makes Availability answer with no row at all.
	vanish bool
}

func (s storeFaults) Availability(ctx context.Context, t domain.EntityType, ids []uuid.UUID) (map[uuid.UUID]domain.Availability, error) {
	if err := s.f.hit("Store.Availability." + string(t)); err != nil {
		return nil, err
	}
	if s.vanish {
		return map[uuid.UUID]domain.Availability{}, nil
	}
	return s.AvailabilityStore.Availability(ctx, t, ids)
}

func (s storeFaults) TakeOver(ctx context.Context, l *domain.CatalogLink, st domain.InventoryState) (domain.InventoryChange, error) {
	if err := s.f.hit("Store.TakeOver"); err != nil {
		return domain.InventoryChange{}, err
	}
	return s.AvailabilityStore.TakeOver(ctx, l, st)
}

func (s storeFaults) Release(ctx context.Context, l *domain.CatalogLink, st domain.InventoryState) (domain.InventoryChange, error) {
	if err := s.f.hit("Store.Release"); err != nil {
		return domain.InventoryChange{}, err
	}
	return s.AvailabilityStore.Release(ctx, l, st)
}

// faultyInventory builds an inventory service on the env's repositories, each wrapped to fail on cue.
func (ie *invEnv) faultyInventory(f *faults, clients application.ClientProvider) *application.InventoryService {
	if clients == nil {
		clients = fixedClients{c: ie.srv.NewClient(ie.t)}
	}
	return application.NewInventoryService(application.InventoryDeps{
		Connections: connFaults{ie.mod.Connections, f}, Links: catalogLinkFaults{ie.mod.CatalogLinks, f},
		Outbox: invOutboxFaults{ie.mod.Outbox, f}, Leases: leaseFaults{ie.mod.Leases, f},
		Store: storeFaults{AvailabilityStore: ie.mod.Availability, f: f}, Clients: clients, Clock: ie.clock,
		Config: application.InventoryConfig{Holder: "faulty"},
	})
}

func TestInventoryReportsStoreFailuresAndKeepsTheRest(t *testing.T) {
	ctx := t.Context()

	t.Run("the connection cannot be read", func(t *testing.T) {
		ie := newInvEnv(t)
		svc := ie.faultyInventory(newFaults("Connections.Get"), nil)
		_, err := svc.Sync(ctx, false)
		assert.ErrorIs(t, err, errStore)
		_, err = svc.Baseline(ctx)
		assert.ErrorIs(t, err, errStore)
		_, err = svc.ReleaseExpired(ctx)
		assert.ErrorIs(t, err, errStore)
		assert.ErrorIs(t, svc.AvailabilityChanged(ctx, uuid.New(), true), errStore)
	})

	t.Run("the lease cannot be taken", func(t *testing.T) {
		ie := newInvEnv(t)
		svc := ie.faultyInventory(newFaults("Leases.Acquire"), nil)
		_, err := svc.Sync(ctx, false)
		assert.ErrorIs(t, err, errStore)
		_, err = svc.Baseline(ctx)
		assert.ErrorIs(t, err, errStore)
		_, err = svc.ReleaseExpired(ctx)
		assert.ErrorIs(t, err, errStore)
	})

	t.Run("a lease that cannot be released is only logged", func(t *testing.T) {
		ie := newInvEnv(t)
		ie.stampBaseline()
		svc := ie.faultyInventory(newFaults("Leases.Release"), nil)
		_, err := svc.Sync(ctx, false)
		assert.NoError(t, err)
	})

	t.Run("no client can be built", func(t *testing.T) {
		ie := newInvEnv(t)
		svc := ie.faultyInventory(newFaults(), brokenClients{})
		_, err := svc.Sync(ctx, false)
		assert.ErrorIs(t, err, domain.ErrNotConnected)
	})

	t.Run("HubRise refuses the token and the pause cannot be recorded", func(t *testing.T) {
		ie := newInvEnv(t)
		ie.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/inventory", Status: http.StatusForbidden})
		svc := ie.faultyInventory(newFaults("Connections.RecordAuthError"), nil)
		_, err := svc.Sync(ctx, false)
		require.Error(t, err)
		assert.ErrorIs(t, err, errStore)
		assert.ErrorIs(t, err, pkghubrise.KindForbidden)
	})

	t.Run("the baseline fails on each of its steps, and nothing is restored meanwhile", func(t *testing.T) {
		for _, name := range []string{"Links.ListByType.product", "Store.Availability.product", "Outbox.Enqueue", "Connections.SetInventoryBaseline"} {
			ie := newInvEnv(t)
			ie.linkAll()
			ie.setAvailable(ie.fx.SalmonSushi.ID, false)
			svc := ie.faultyInventory(newFaults(name), nil)
			_, err := svc.Baseline(ctx)
			assert.ErrorIs(t, err, errStore, name)
			conn, err := ie.mod.Connections.Get(ctx)
			require.NoError(t, err)
			assert.Nil(t, conn.InventoryBaselineAt, name)
		}
	})

	t.Run("the apply fails on each of its steps and the others still run", func(t *testing.T) {
		for name, wantApplied := range map[string]bool{
			"Outbox.ActiveDedupeKeys":          false,
			"Links.ListByType.product":         false,
			"Store.Availability.product":       false,
			"Links.ListByType.choice":          true,
			"Store.Availability.choice":        true,
			"Store.TakeOver":                   false,
			"Links.RecordStock":                true,
			"Store.Release":                    true,
			"Connections.SetInventoryBaseline": true,
		} {
			ie := newInvEnv(t)
			ie.linkAll()
			choice := ie.newChoice(ie.fx.SalmonSushi.ID, refSalmon+"|SAUCE|"+refSauce, refSauce)
			ie.stampBaseline()
			if name == "Store.Release" {
				_, err := ie.mod.Availability.TakeOver(ctx, ie.catalogLink(domain.EntityProduct, refMochi), domain.InventoryState{SyncedAt: ie.clock.Now()})
				require.NoError(t, err)
			}
			if name == "Connections.SetInventoryBaseline" {
				ie.exec(`UPDATE hubrise_connection SET inventory_baseline_at = NULL`)
			}
			ie.setInventory(pkghubrise.SoldOutSku(refTuna, nil), stockEntry(refTea, "5"), optionEntry(refSauce, "0"))
			svc := ie.faultyInventory(newFaults(name), nil)
			_, err := svc.Sync(ctx, false)
			assert.ErrorIs(t, err, errStore, name)
			assert.Equal(t, wantApplied, !ie.available(ie.fx.TunaSushi.ID), "%s: the product sell out", name)
			_ = choice
		}
	})

	t.Run("a link whose row vanished meanwhile is skipped", func(t *testing.T) {
		ie := newInvEnv(t)
		ie.linkAll()
		ie.stampBaseline()
		ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
		svc := application.NewInventoryService(application.InventoryDeps{
			Connections: ie.mod.Connections, Links: ie.mod.CatalogLinks, Outbox: ie.mod.Outbox, Leases: ie.mod.Leases,
			Store:   storeFaults{AvailabilityStore: ie.mod.Availability, f: newFaults(), vanish: true},
			Clients: fixedClients{c: ie.srv.NewClient(t)}, Clock: ie.clock,
		})
		res, err := svc.Sync(ctx, false)
		require.NoError(t, err)
		assert.Equal(t, application.InventoryResult{}, res)
		assert.True(t, ie.available(ie.fx.SalmonSushi.ID))
	})

	t.Run("the hook fails to find, queue or clear", func(t *testing.T) {
		for _, name := range []string{"Links.FindByLocal", "Outbox.Enqueue", "Links.ClearSoldOut"} {
			ie := newInvEnv(t)
			ie.linkAll()
			ie.stampBaseline()
			_, err := ie.mod.Availability.TakeOver(ctx, ie.catalogLink(domain.EntityProduct, refSalmon), domain.InventoryState{SyncedAt: ie.clock.Now()})
			require.NoError(t, err)
			svc := ie.faultyInventory(newFaults(name), nil)
			assert.ErrorIs(t, svc.AvailabilityChanged(ctx, ie.fx.SalmonSushi.ID, true), errStore, name)
		}
	})

	t.Run("the expiry sweeper fails to list or to release, and releases the rest", func(t *testing.T) {
		ie := newInvEnv(t)
		ie.linkAll()
		ie.stampBaseline()
		past := ie.clock.Now().Add(-time.Minute)
		for _, id := range []string{refSalmon, refTuna} {
			_, err := ie.mod.Availability.TakeOver(ctx, ie.catalogLink(domain.EntityProduct, id), domain.InventoryState{SoldOutUntil: &past, SyncedAt: past})
			require.NoError(t, err)
		}
		_, err := ie.faultyInventory(newFaults("Links.ListSoldOutExpired"), nil).ReleaseExpired(ctx)
		assert.ErrorIs(t, err, errStore)
		n, err := ie.faultyInventory(newFaults("Store.Release"), nil).ReleaseExpired(ctx)
		assert.ErrorIs(t, err, errStore)
		assert.Zero(t, n)
		n, err = ie.inv.Service.ReleaseExpired(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})
}

// A stock HubRise sends that is not a number is logged and skipped, the other entries still apply.
func TestInventoryIgnoresAnUnreadableStock(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	hr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"sku_ref": refSalmon, "stock": "plenty"},
			{"sku_ref": refTuna, "stock": "0"},
			{"sku_ref": "NO-STOCK-FIGURE"}, {"stock": "1"},
		})
	}))
	defer hr.Close()
	client, err := pkghubrise.New(hr.URL, "token")
	require.NoError(t, err)

	svc := application.NewInventoryService(application.InventoryDeps{
		Connections: ie.mod.Connections, Links: ie.mod.CatalogLinks, Outbox: ie.mod.Outbox, Leases: ie.mod.Leases,
		Store: ie.mod.Availability, Clients: fixedClients{c: client}, Clock: ie.clock,
	})
	res, err := svc.Sync(t.Context(), false)
	require.NoError(t, err)
	assert.Equal(t, 1, res.SoldOut)
	assert.True(t, ie.available(ie.fx.SalmonSushi.ID))
	assert.False(t, ie.available(ie.fx.TunaSushi.ID))
}

// An event processor that cannot record the outcome logs it and carries on.
func TestInventoryEventsLogStoreFailuresWhileSettling(t *testing.T) {
	for name, setup := range map[string]struct {
		fault string
		sync  bool
		mod   func(ie *invEnv)
	}{
		"mark done":       {"Events.MarkDone", true, nil},
		"mark ignored":    {"Events.MarkIgnored", true, func(ie *invEnv) { ie.exec(`UPDATE hubrise_connection SET sync_inventory = false`) }},
		"defer when busy": {"Events.Defer", true, func(ie *invEnv) { ie.holdSyncLease() }},
		"retry later":     {"Events.RetryLater", true, func(ie *invEnv) { ie.srv.AddFault(hubrisetest.Fault{Path: "/v1/location/inventory", Status: 500}) }},
		"mark failed":     {"Events.MarkFailed", true, func(ie *invEnv) { ie.srv.AddFault(hubrisetest.Fault{Path: "/v1/location/inventory", Status: 500}) }},
	} {
		t.Run(name, func(t *testing.T) {
			ie := newInvEnv(t)
			ie.stampBaseline()
			id := ie.addEvent("inventory", "update", time.Second)
			if name == "mark failed" {
				ie.exec(`UPDATE hubrise_events SET attempts = 7 WHERE id = $1`, id) // the claim makes it the 8th
			}
			if setup.mod != nil {
				setup.mod(ie)
			}
			p := application.NewInventoryEvents(application.InventoryEventsDeps{
				Connections: ie.mod.Connections, Events: eventFaults{EventRepository: ie.mod.Events, f: newFaults(setup.fault)},
				Inventory: ie.inv.Service, Clock: ie.clock,
			})
			n, err := p.ProcessOnce(t.Context())
			require.NoError(t, err)
			assert.Equal(t, 1, n)
			assert.Equal(t, domain.EventPending, ie.eventState(id), "the outcome could not be recorded: the lease brings it back")
		})
	}
}

func (ie *invEnv) holdSyncLease() {
	ie.t.Helper()
	ok, err := ie.mod.Leases.Acquire(ie.t.Context(), application.LeaseInventorySync, "someone-else", ie.clock.Now(), time.Hour)
	require.NoError(ie.t, err)
	require.True(ie.t, ok)
}
