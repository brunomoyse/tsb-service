package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	"tsb-service/pkg/timezone"
)

// Every repository method reports a store that is down as an error (and never panics): the
// integration is never in the critical path, so callers log and carry on.
func TestRepositoriesReportAStoreThatIsDown(t *testing.T) {
	db := testhelpers.ClosedDB(t)
	ctx := t.Context()
	now := time.Now()
	id := uuid.New()
	link := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: "k", HubriseRef: "k", Origin: domain.OriginImport}
	link.SetLocalID(id)

	conns := infrastructure.NewConnectionRepository(db)
	states := infrastructure.NewOAuthStateRepository(db)
	leases := infrastructure.NewLeaseRepository(db)
	links := infrastructure.NewCatalogLinkRepository(db)
	outbox := infrastructure.NewOutboxRepository(db)
	orders := infrastructure.NewOrderLinkRepository(db)
	events := infrastructure.NewEventRepository(db)
	foreign := infrastructure.NewForeignOrderRepository(db)

	calls := map[string]func() error{
		"conn get":        func() error { _, err := conns.Get(ctx); return err },
		"conn connect":    func() error { _, err := conns.Connect(ctx, domain.ConnectParams{}); return err },
		"conn disconnect": func() error { return conns.Disconnect(ctx) },
		"conn settings":   func() error { _, err := conns.UpdateSettings(ctx, domain.SettingsPatch{}); return err },
		"conn auth":       func() error { return conns.RecordAuthError(ctx, now, "x") },

		"state create":  func() error { return states.Create(ctx, "s", id, now) },
		"state consume": func() error { _, _, err := states.Consume(ctx, "s", now); return err },
		"state purge":   func() error { _, err := states.PurgeExpired(ctx, now); return err },

		"lease acquire": func() error { _, err := leases.Acquire(ctx, "n", "h", now, time.Minute); return err },
		"lease release": func() error { return leases.Release(ctx, "n", "h") },

		"link upsert":   func() error { return links.Upsert(ctx, link) },
		"link key":      func() error { _, err := links.FindByKey(ctx, domain.EntityProduct, "k"); return err },
		"link local":    func() error { _, err := links.FindByLocal(ctx, domain.EntityProduct, id); return err },
		"link list":     func() error { _, err := links.ListByType(ctx, domain.EntityProduct, true); return err },
		"link ref":      func() error { _, err := links.ListByRef(ctx, domain.EntityProduct, "k"); return err },
		"link count":    func() error { _, err := links.Count(ctx, true); return err },
		"link retired":  func() error { return links.SetRetired(ctx, 1, &now) },
		"link disabled": func() error { return links.SetCatalogDisabled(ctx, 1, true) },
		"link stock":    func() error { return links.UpdateInventory(ctx, 1, domain.InventoryState{SyncedAt: now}) },
		"link clear":    func() error { return links.ClearSoldOut(ctx, 1) },
		"link delete":   func() error { return links.Delete(ctx, 1) },
		"link purge":    func() error { _, err := links.DeleteAll(ctx); return err },

		"outbox enqueue": func() error {
			_, _, err := outbox.Enqueue(ctx, domain.NewOutboxItem(domain.OutboxInventory, nil, "k", nil, now))
			return err
		},
		"outbox claim":   func() error { _, err := outbox.Claim(ctx, now, time.Minute, 1); return err },
		"outbox sent":    func() error { _, err := outbox.MarkSent(ctx, 1, now); return err },
		"outbox parked":  func() error { _, err := outbox.MarkParked(ctx, 1, now, "x"); return err },
		"outbox skip":    func() error { _, err := outbox.MarkSkipped(ctx, 1, "x"); return err },
		"outbox release": func() error { _, err := outbox.Release(ctx, 1, now, "x", false); return err },
		"outbox get":     func() error { _, err := outbox.Get(ctx, 1); return err },
		"outbox list":    func() error { _, err := outbox.List(ctx, nil, 1); return err },
		"outbox counts":  func() error { _, err := outbox.Counts(ctx); return err },
		"outbox retry":   func() error { _, err := outbox.RetryParked(ctx, 1, now); return err },
		"outbox retry*":  func() error { _, err := outbox.RetryAllParked(ctx, now); return err },

		"order enqueue":   func() error { _, err := orders.EnqueueCreate(ctx, id, nil, now); return err },
		"order get":       func() error { _, err := orders.Get(ctx, id); return err },
		"order by hubr":   func() error { _, err := orders.FindByHubriseID(ctx, "h"); return err },
		"order sent":      func() error { return orders.MarkSent(ctx, id, "h", "new", now) },
		"order total":     func() error { return orders.SetRemoteTotal(ctx, id, decimal.Zero) },
		"order attention": func() error { _, err := orders.CountNeedsAttention(ctx); return err },

		"event insert":   func() error { _, err := events.Insert(ctx, domain.Event{ID: "e"}); return err },
		"event claim":    func() error { _, err := events.Claim(ctx, now, time.Minute, 1); return err },
		"event done":     func() error { return events.MarkDone(ctx, "e", now) },
		"event collapse": func() error { _, err := events.CollapsePending(ctx, "catalog", now, now); return err },
		"event oldest":   func() error { _, err := events.OldestPendingReceivedAt(ctx, "catalog"); return err },
		"event purge":    func() error { _, err := events.PurgeFinished(ctx, now); return err },

		"foreign mark":  func() error { return foreign.Mark(ctx, "h", now) },
		"foreign is":    func() error { _, err := foreign.IsForeign(ctx, "h"); return err },
		"foreign purge": func() error { _, err := foreign.Purge(ctx, now); return err },
	}
	for name, call := range calls {
		err := call()
		if assert.Error(t, err, name) {
			assert.NotErrorIs(t, err, domain.ErrNotFound, "%s: a store failure is not 'not found'", name)
		}
	}
}

func TestSystemClockIsBrussels(t *testing.T) {
	now := infrastructure.SystemClock{}.Now()
	assert.Equal(t, timezone.Location, now.Location())
	assert.WithinDuration(t, time.Now(), now, time.Second)
}
