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
	customers := infrastructure.NewCustomerLinkRepository(db)
	publish := infrastructure.NewPublishStateRepository(db)
	published := infrastructure.NewPublishedItemRepository(db)
	local := infrastructure.NewLocalCatalogRepository(db)
	source := infrastructure.NewCustomerSource(db)

	calls := map[string]func() error{
		"publish get":      func() error { _, err := publish.Get(ctx); return err },
		"publish dirty":    func() error { _, err := publish.MarkDirty(ctx, now); return err },
		"publish complete": func() error { return publish.Complete(ctx, "h", 1, domain.PublishResult{At: now}, nil, nil) },
		"published list":   func() error { _, err := published.List(ctx); return err },
		"published keys":   func() error { _, err := published.ByKeys(ctx, domain.EntityProduct, []string{"k"}); return err },
		"published count":  func() error { _, err := published.Count(ctx, domain.EntityProduct); return err },
		"published all":    func() error { return published.Replace(ctx, nil) },
		"snapshot":         func() error { _, _, err := local.SnapshotWithLinks(ctx); return err },

		"conn get":        func() error { _, err := conns.Get(ctx); return err },
		"conn connect":    func() error { _, err := conns.Connect(ctx, domain.ConnectParams{}); return err },
		"conn disconnect": func() error { return conns.Disconnect(ctx) },
		"conn settings":   func() error { _, err := conns.UpdateSettings(ctx, domain.SettingsPatch{}); return err },
		"conn auth":       func() error { return conns.RecordAuthError(ctx, now, "x") },
		"conn baseline":   func() error { return conns.ResetInventoryBaseline(ctx) },

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
		"link record":   func() error { return links.RecordStock(ctx, 1, nil, nil, now) },
		"link expired":  func() error { _, err := links.ListSoldOutExpired(ctx, now); return err },
		"link delete":   func() error { return links.Delete(ctx, 1) },
		"link purge":    func() error { _, err := links.DeleteAll(ctx); return err },
		"link managed":  func() error { _, err := links.LinkedProductIDs(ctx, []uuid.UUID{id}); return err },
		"link unlinked": func() error { _, err := links.CountUnlinkedProducts(ctx); return err },

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
		"outbox extend":  func() error { _, err := outbox.Extend(ctx, 1, now, now); return err },
		"outbox retry":   func() error { _, err := outbox.RetryParked(ctx, 1, now); return err },
		"outbox retry*":  func() error { _, err := outbox.RetryAllParked(ctx, now); return err },
		"outbox scrub":   func() error { _, err := outbox.ScrubCustomerData(ctx, id); return err },
		"outbox purge":   func() error { _, err := outbox.PurgeFinished(ctx, now); return err },

		"order enqueue":   func() error { _, err := orders.EnqueueCreate(ctx, id, nil, now); return err },
		"order get":       func() error { _, err := orders.Get(ctx, id); return err },
		"order by hubr":   func() error { _, err := orders.FindByHubriseID(ctx, "h"); return err },
		"order sent":      func() error { return orders.MarkSent(ctx, id, "h", "new", now) },
		"order total":     func() error { return orders.SetRemoteTotal(ctx, id, decimal.Zero) },
		"order attention": func() error { _, err := orders.CountNeedsAttention(ctx); return err },
		"order set flag":  func() error { return orders.SetNeedsAttention(ctx, id, "X") },
		"order not accpt": func() error { return orders.MarkNotAccepted(ctx, id) },
		"order alerted":   func() error { return orders.MarkStuckAlerted(ctx, id, now) },
		"order flag":      func() error { return orders.FlagAttention(ctx, id, "X") },
		"order unflag":    func() error { return orders.ClearAttention(ctx, id, "X") },
		"order stamp":     func() error { return orders.StampRemoteRead(ctx, id, now) },
		"order recreate":  func() error { return orders.Recreate(ctx, id, nil, now) },
		"outbox max id":   func() error { _, err := outbox.MaxPendingID(ctx, id, domain.OutboxOrderStatus); return err },
		"outbox skip up":  func() error { _, err := outbox.SkipPendingUpTo(ctx, id, domain.OutboxOrderStatus, 1, "x"); return err },
		"outbox keys":     func() error { _, err := outbox.ActiveDedupeKeys(ctx, domain.OutboxInventory); return err },
		"outbox count":    func() error { _, err := outbox.CountForOrder(ctx, id, domain.OutboxOrderCreate); return err },

		"event insert":   func() error { _, err := events.Insert(ctx, domain.Event{ID: "e"}); return err },
		"event claim":    func() error { _, err := events.Claim(ctx, now, time.Minute, 1); return err },
		"event done":     func() error { return events.MarkDone(ctx, "e", now) },
		"event collapse": func() error { _, err := events.CollapsePending(ctx, "catalog", now, now); return err },
		"event oldest":   func() error { _, err := events.OldestPendingReceivedAt(ctx, "catalog"); return err },
		"event purge":    func() error { _, err := events.PurgeFinished(ctx, now); return err },

		"outbox once": func() error {
			_, _, err := outbox.EnqueueIfNoPending(ctx, domain.NewOutboxItem(domain.OutboxCustomer, nil, "cust:x", nil, now))
			return err
		},
		"outbox skip key": func() error {
			_, err := outbox.SkipOpenByDedupeKey(ctx, domain.OutboxCustomer, "cust:x", "x")
			return err
		},

		"customer get":       func() error { _, err := customers.Get(ctx, id); return err },
		"customer begin":     func() error { _, _, err := customers.Begin(ctx, id); return err },
		"customer set":       func() error { return customers.SetCustomer(ctx, id, "cu1", "h", now) },
		"customer hash":      func() error { return customers.SetHash(ctx, id, "h", now) },
		"customer forget":    func() error { return customers.Forget(ctx, id) },
		"customer anonymise": func() error { return customers.MarkAnonymised(ctx, id, now) },
		"customer profile":   func() error { _, err := source.Profile(ctx, id); return err },
		"customer deleted":   func() error { _, err := source.DeletedNotErased(ctx, 1); return err },

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
