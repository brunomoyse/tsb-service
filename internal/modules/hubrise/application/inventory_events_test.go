package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// addEvent stores a callback event the way the receiver does.
func (ie *invEnv) addEvent(resource, eventType string, receivedAgo time.Duration) string {
	ie.t.Helper()
	id := uuid.NewString()
	payload, err := json.Marshal(map[string]any{"id": id, "resource_type": resource, "event_type": eventType})
	require.NoError(ie.t, err)
	inserted, err := ie.mod.Events.Insert(ie.t.Context(), domain.Event{
		ID: id, ResourceType: resource, EventType: eventType, Payload: payload, ReceivedAt: ie.clock.Now().Add(-receivedAgo),
	})
	require.NoError(ie.t, err)
	require.True(ie.t, inserted)
	return id
}

func (ie *invEnv) eventState(id string) domain.EventState {
	ie.t.Helper()
	var st domain.EventState
	require.NoError(ie.t, ie.db.GetContext(ie.t.Context(), &st, `SELECT state FROM hubrise_events WHERE id = $1`, id))
	return st
}

func (ie *invEnv) inventoryReads() int {
	return len(ie.srv.RequestsTo(http.MethodGet, "/v1/location/inventory"))
}

func TestInventoryEventsCollapseIntoOneSync(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
	update1 := ie.addEvent("inventory", "update", time.Minute)
	patch := ie.addEvent("inventory", "patch", 30*time.Second)
	update2 := ie.addEvent("inventory", "update", time.Second)
	order := ie.addEvent("order", "update", time.Second)
	catalog := ie.addEvent("catalog", "update", time.Second)
	ie.srv.ClearRequests()

	n, err := ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, 1, ie.inventoryReads(), "three events, one read: the payload is only a reason to look")
	for _, id := range []string{update1, patch, update2} {
		assert.Equal(t, domain.EventDone, ie.eventState(id))
	}
	assert.Equal(t, domain.EventPending, ie.eventState(order), "the order events are the order processor's")
	assert.Equal(t, domain.EventPending, ie.eventState(catalog), "and the catalog events the drift job's")
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))

	n, err = ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestInventoryEventsWaitWhileTheIntegrationIsPaused(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	id := ie.addEvent("inventory", "update", time.Second)
	ie.exec(`UPDATE hubrise_connection SET enabled = false`)

	n, err := ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, domain.EventPending, ie.eventState(id))

	ie.exec(`DELETE FROM hubrise_connection`)
	n, err = ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestInventoryEventsAreIgnoredWhenInventorySyncIsOff(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
	id := ie.addEvent("inventory", "update", time.Second)
	ie.exec(`UPDATE hubrise_connection SET sync_inventory = false`)
	ie.srv.ClearRequests()

	n, err := ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.EventIgnored, ie.eventState(id))
	assert.Empty(t, ie.srv.Requests())
	assert.True(t, ie.available(ie.fx.SalmonSushi.ID))
}

func TestInventoryEventsWaitForAnotherReplicaThatIsSyncing(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	id := ie.addEvent("inventory", "update", time.Second)
	ok, err := ie.mod.Leases.Acquire(t.Context(), application.LeaseInventorySync, "replica-b", ie.clock.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	n, err := ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.EventPending, ie.eventState(id), "deferred, not failed")
	var attempts int
	require.NoError(t, ie.db.GetContext(t.Context(), &attempts, `SELECT attempts FROM hubrise_events WHERE id = $1`, id))
	assert.Zero(t, attempts, "waiting for a lease is not an attempt")

	// Not due yet, then due once the other replica is done.
	n, err = ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
	require.NoError(t, ie.mod.Leases.Release(t.Context(), application.LeaseInventorySync, "replica-b"))
	ie.clock.Advance(10 * time.Second)
	n, err = ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.EventDone, ie.eventState(id))
}

func TestInventoryEventsRetryAFailedSyncAndGiveUp(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	id := ie.addEvent("inventory", "update", time.Second)
	ie.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/inventory", Status: http.StatusInternalServerError})

	// Eight attempts with doubling delays (30 s, 60 s, ... capped at 30 min), then the event fails.
	for attempt := 1; attempt <= 8; attempt++ {
		n, err := ie.inv.Events.ProcessOnce(t.Context())
		require.NoError(t, err)
		require.Equal(t, 1, n, "attempt %d", attempt)
		if attempt < 8 {
			assert.Equal(t, domain.EventPending, ie.eventState(id), "attempt %d", attempt)
			n, err = ie.inv.Events.ProcessOnce(t.Context())
			require.NoError(t, err)
			assert.Zero(t, n, "backing off after attempt %d", attempt)
			ie.clock.Advance(31 * time.Minute)
		}
	}
	assert.Equal(t, domain.EventFailed, ie.eventState(id))

	// A fresh event works once HubRise is back.
	ie.srv.ClearFaults()
	ok := ie.addEvent("inventory", "update", time.Second)
	n, err := ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.EventDone, ie.eventState(ok))
}

func TestInventoryEventsPauseThePipelineWhenHubRiseRefusesTheToken(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	id := ie.addEvent("inventory", "update", time.Second)
	ie.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/inventory", Status: http.StatusUnauthorized})

	n, err := ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	conn, err := ie.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, conn.AuthErrorAt, "the connection is flagged for the dashboard")
	assert.Equal(t, domain.EventPending, ie.eventState(id))

	// Paused: the event waits for the reconnection instead of burning attempts.
	n, err = ie.inv.Events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestInventoryEventsRunWakesAndStops(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
	ie.stampBaseline()
	id := ie.addEvent("inventory", "update", time.Second)

	// A long tick: only the wake-up can make the processor look in time.
	p := application.NewInventoryEvents(application.InventoryEventsDeps{
		Connections: ie.mod.Connections, Events: ie.mod.Events, Inventory: ie.inv.Service, Clock: ie.clock,
		Config: application.InventoryEventsConfig{Tick: time.Hour},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	p.Wake()
	p.Wake() // absorbed by the first
	require.Eventually(t, func() bool { return ie.eventState(id) == domain.EventDone }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the processor did not stop")
	}
}

func TestInventoryEventsDrainStopsOnAStoreError(t *testing.T) {
	ie := newInvEnv(t)
	ie.addEvent("inventory", "update", time.Second)
	f := newFaults("Events.Claim")
	p := application.NewInventoryEvents(application.InventoryEventsDeps{
		Connections: ie.mod.Connections, Events: eventFaults{EventRepository: ie.mod.Events, f: f}, Inventory: ie.inv.Service, Clock: ie.clock,
		Config: application.InventoryEventsConfig{Tick: time.Hour},
	})
	_, err := p.ProcessOnce(t.Context())
	assert.ErrorIs(t, err, errStore)

	// Run logs it and goes on to wait for the next wake-up or tick.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // the first drain fails and is logged
	cancel()
	<-done
}

func TestInventoryJobsRunBothLoopsUntilStopped(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	until := ie.clock.Now().Add(-time.Minute)
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, &until))

	jobs := application.NewInventoryJobs(application.InventoryJobsDeps{
		Inventory: ie.inv.Service, Leases: ie.mod.Leases, Clock: ie.clock,
		Config: application.InventoryJobsConfig{Holder: "x", PollInterval: time.Hour, SweepInterval: time.Hour},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { jobs.Run(ctx); close(done) }()
	// Both loops tick at once: the poll sells the salmon out, the sweeper finds it already expired
	// (or the poll runs after it); either way the loops ran.
	require.Eventually(t, func() bool { return ie.inventoryReads() >= 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the jobs did not stop")
	}
}

func TestCatalogDriftDebouncesEventsIntoOneCheck(t *testing.T) {
	ie := newInvEnv(t)
	young := ie.addEvent("catalog", "update", 5*time.Second)

	ran, err := ie.inv.Drift.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "nothing to look at")
	assert.Zero(t, ie.drift.count())

	// A burst of edits: three events, the oldest one past the debounce, the newest one not yet.
	old1 := ie.addEvent("catalog", "update", time.Minute)
	old2 := ie.addEvent("catalog", "create", 40*time.Second)
	ran, err = ie.inv.Drift.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, 1, ie.drift.count(), "one check for the burst")
	assert.Equal(t, []pkghubrise.Class{pkghubrise.ClassBackground}, ie.drift.classes, "a background call")
	assert.Equal(t, domain.EventDone, ie.eventState(old1))
	assert.Equal(t, domain.EventDone, ie.eventState(old2))
	assert.Equal(t, domain.EventPending, ie.eventState(young), "the event of the last 30 s waits for the next round")

	// Another replica looking at the same moment finds nothing left to collapse.
	other := ie.replica("replica-b")
	ran, err = other.Drift.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)

	ie.clock.Advance(time.Minute)
	ran, err = ie.inv.Drift.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, 2, ie.drift.count())
	assert.Equal(t, domain.EventDone, ie.eventState(young))
}

func TestCatalogDriftDoesNothingWhilePaused(t *testing.T) {
	ie := newInvEnv(t)
	id := ie.addEvent("catalog", "update", time.Minute)
	ie.exec(`UPDATE hubrise_connection SET enabled = false`)

	ran, err := ie.inv.Drift.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)
	ran, err = ie.inv.Drift.CheckScheduled(t.Context())
	require.NoError(t, err)
	assert.True(t, ran, "the lease was free, but the check itself is skipped")
	assert.Zero(t, ie.drift.count())
	assert.Equal(t, domain.EventPending, ie.eventState(id), "the events are kept for the resumption")

	ie.exec(`DELETE FROM hubrise_connection`)
	ran, err = ie.inv.Drift.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)
}

func TestCatalogDriftScheduledCheckIsSharedBetweenReplicas(t *testing.T) {
	ie := newInvEnv(t)

	ran, err := ie.inv.Drift.CheckScheduled(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, 1, ie.drift.count())
	assert.Equal(t, []pkghubrise.Class{pkghubrise.ClassBackground}, ie.drift.classes)

	ran, err = ie.replica("replica-b").Drift.CheckScheduled(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "six hours between two checks, whoever runs them")

	ie.clock.Advance(6 * time.Hour)
	ran, err = ie.replica("replica-b").Drift.CheckScheduled(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, 2, ie.drift.count())
}

func TestCatalogDriftReportsAFailedCheck(t *testing.T) {
	ie := newInvEnv(t)
	ie.drift.err = errStore
	ie.addEvent("catalog", "update", time.Minute)

	ran, err := ie.inv.Drift.CheckEvents(t.Context())
	assert.ErrorIs(t, err, errStore)
	assert.True(t, ran)
	_, err = ie.inv.Drift.CheckScheduled(t.Context())
	assert.ErrorIs(t, err, errStore)
}

func TestCatalogDriftRunStopsAndChecksEvents(t *testing.T) {
	ie := newInvEnv(t)
	id := ie.addEvent("catalog", "update", time.Minute)
	drift := application.NewCatalogDrift(application.CatalogDriftDeps{
		Connections: ie.mod.Connections, Events: ie.mod.Events, Leases: ie.mod.Leases, Checker: ie.drift, Clock: ie.clock,
		Config: application.CatalogDriftConfig{Holder: "x", Tick: 10 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { drift.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return ie.eventState(id) == domain.EventDone }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the drift job did not stop")
	}
	assert.GreaterOrEqual(t, ie.drift.count(), 1)
}

func TestCatalogDriftLogsAStoreErrorAndKeepsGoing(t *testing.T) {
	ie := newInvEnv(t)
	f := newFaults("Events.OldestPendingReceivedAt")
	drift := application.NewCatalogDrift(application.CatalogDriftDeps{
		Connections: ie.mod.Connections, Events: eventFaults{EventRepository: ie.mod.Events, f: f}, Leases: ie.mod.Leases,
		Checker: ie.drift, Clock: ie.clock, Config: application.CatalogDriftConfig{Holder: "x", Tick: 5 * time.Millisecond},
	})
	_, err := drift.CheckEvents(t.Context())
	assert.ErrorIs(t, err, errStore)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { drift.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond) // several ticks, each failing and logged
	cancel()
	<-done
}

func TestModuleBuildsTheInventoryWithAndWithoutADriftChecker(t *testing.T) {
	ie := newInvEnv(t)
	assert.NotNil(t, ie.inv.Drift)
	without := ie.mod.NewInventory(ie.push, hubrise.InventoryOptions{})
	assert.Nil(t, without.Drift)
	assert.NotNil(t, without.Service)

	// Attach makes a stored callback event wake the inventory processor.
	in := ie.mod.NewSync(ie.push, hubrise.SyncOptions{Transitioner: &fakeTransitioner{t: t, e: ie.env}})
	ie.inv.Attach(in)

	// Run starts everything and stops with the context.
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for _, inv := range []*hubrise.Inventory{ie.inv, without} {
		wg.Add(1)
		go func() { defer wg.Done(); inv.Run(ctx) }()
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestInventoryEventsDrainsLargeBacklogsInBatches(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	var ids []string
	for range 5 {
		ids = append(ids, ie.addEvent("inventory", "update", time.Second))
	}
	p := application.NewInventoryEvents(application.InventoryEventsDeps{
		Connections: ie.mod.Connections, Events: ie.mod.Events, Inventory: ie.inv.Service, Clock: ie.clock,
		Config: application.InventoryEventsConfig{Tick: time.Hour, BatchSize: 2},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		for _, id := range ids {
			if ie.eventState(id) != domain.EventDone {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond, "three claims of 2, 2 and 1 events")
	cancel()
	<-done
	assert.Equal(t, 3, ie.inventoryReads(), "one read per batch")

	// A stopped context ends a drain before it claims anything.
	late := ie.addEvent("inventory", "update", time.Second)
	stopped, stop := context.WithCancel(t.Context())
	stop()
	p.Run(stopped)
	assert.Equal(t, domain.EventPending, ie.eventState(late))
}

func TestInventoryEventsReportAConnectionThatCannotBeRead(t *testing.T) {
	ie := newInvEnv(t)
	p := application.NewInventoryEvents(application.InventoryEventsDeps{
		Connections: connFaults{ie.mod.Connections, newFaults("Connections.Get")}, Events: ie.mod.Events,
		Inventory: ie.inv.Service, Clock: ie.clock,
	})
	_, err := p.ProcessOnce(t.Context())
	assert.ErrorIs(t, err, errStore)
}

func TestCatalogDriftReportsStoreFailures(t *testing.T) {
	ie := newInvEnv(t)
	ie.addEvent("catalog", "update", time.Minute)
	for name, build := range map[string]func() *application.CatalogDrift{
		"connection": func() *application.CatalogDrift {
			return application.NewCatalogDrift(application.CatalogDriftDeps{
				Connections: connFaults{ie.mod.Connections, newFaults("Connections.Get")}, Events: ie.mod.Events, Leases: ie.mod.Leases,
				Checker: ie.drift, Clock: ie.clock,
			})
		},
		"collapse": func() *application.CatalogDrift {
			return application.NewCatalogDrift(application.CatalogDriftDeps{
				Connections: ie.mod.Connections, Events: eventFaults{ie.mod.Events, newFaults("Events.CollapsePending")}, Leases: ie.mod.Leases,
				Checker: ie.drift, Clock: ie.clock,
			})
		},
	} {
		ran, err := build().CheckEvents(t.Context())
		assert.ErrorIs(t, err, errStore, name)
		assert.False(t, ran, name)
	}
	assert.Zero(t, ie.drift.count())
}

func TestInventoryConfigurationDefaultsAndOverrides(t *testing.T) {
	ie := newInvEnv(t)
	ie.addEvent("catalog", "update", 2*time.Minute)

	// Every knob set: a 10 minute debounce keeps a 2 minute old event waiting; the defaults would not.
	custom := application.NewCatalogDrift(application.CatalogDriftDeps{
		Connections: ie.mod.Connections, Events: ie.mod.Events, Leases: ie.mod.Leases, Checker: ie.drift, Clock: ie.clock,
		Config: application.CatalogDriftConfig{Holder: "h", Interval: time.Hour, Tick: time.Second, Debounce: 10 * time.Minute},
	})
	ran, err := custom.CheckEvents(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)

	jobs := application.NewInventoryJobs(application.InventoryJobsDeps{
		Inventory: ie.inv.Service, Leases: ie.mod.Leases, Clock: ie.clock,
		Config: application.InventoryJobsConfig{Holder: "h", PollInterval: time.Hour, SweepInterval: time.Hour},
	})
	ran, err = jobs.SweepOnce(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)

	defaults := application.NewInventoryJobs(application.InventoryJobsDeps{Inventory: ie.inv.Service, Leases: ie.mod.Leases, Clock: ie.clock})
	ran, err = defaults.PollOnce(t.Context())
	require.NoError(t, err)
	assert.True(t, ran, "a random holder and the default cadence")

	events := application.NewInventoryEvents(application.InventoryEventsDeps{
		Connections: ie.mod.Connections, Events: ie.mod.Events, Inventory: ie.inv.Service, Clock: ie.clock,
		Config: application.InventoryEventsConfig{BatchSize: 3, Lease: time.Minute, Tick: time.Second, BusyDelay: time.Second,
			BackoffBase: time.Second, BackoffMax: time.Minute, MaxAttempts: 2},
	})
	n, err := events.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)

	svc := application.NewInventoryService(application.InventoryDeps{
		Connections: ie.mod.Connections, Links: ie.mod.CatalogLinks, Outbox: ie.mod.Outbox, Leases: ie.mod.Leases,
		Store: ie.mod.Availability, Clients: fixedClients{c: ie.srv.NewClient(t)}, Clock: ie.clock,
		Config: application.InventoryConfig{Holder: "h", CallTimeout: time.Minute, SyncLease: time.Minute},
	})
	ie.stampBaseline()
	_, err = svc.Sync(t.Context(), false)
	require.NoError(t, err)
}
