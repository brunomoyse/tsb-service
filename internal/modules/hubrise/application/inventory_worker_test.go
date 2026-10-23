package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// inventoryRow queues a hand-made inventory row due now and returns its id.
func (ie *invEnv) inventoryRow(payload any) int64 {
	ie.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(ie.t, err)
	var id int64
	require.NoError(ie.t, ie.db.GetContext(ie.t.Context(), &id, `
		INSERT INTO hubrise_outbox (kind, payload, next_attempt_at, created_at) VALUES ('inventory', $1::jsonb, $2, $2) RETURNING id`,
		string(raw), ie.clock.Now()))
	return id
}

func (ie *invEnv) rowState(id int64) domain.OutboxItem {
	ie.t.Helper()
	it, err := ie.mod.Outbox.Get(ie.t.Context(), id)
	require.NoError(ie.t, err)
	return *it
}

func inventoryPayload(productID uuid.UUID, ref string) map[string]any {
	return map[string]any{"v": domain.InventoryPayloadVersion, "product_id": productID, "sku_ref": ref}
}

func TestInventoryWorkerSettlesEveryKindOfRow(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()

	t.Run("an unreadable payload is parked", func(t *testing.T) {
		for _, p := range []any{[]int{1}, map[string]any{"v": 99, "product_id": ie.fx.SalmonSushi.ID, "sku_ref": refSalmon},
			map[string]any{"v": domain.InventoryPayloadVersion, "product_id": ie.fx.SalmonSushi.ID}} {
			id := ie.inventoryRow(p)
			ie.drainAll()
			assert.Equal(t, domain.OutboxParked, ie.rowState(id).State, "%v", p)
		}
		assert.Empty(t, ie.srv.Requests())
	})

	t.Run("a product that is not linked any more is skipped", func(t *testing.T) {
		id := ie.inventoryRow(inventoryPayload(uuid.New(), "WHATEVER"))
		ie.drainAll()
		assert.Equal(t, domain.OutboxSkipped, ie.rowState(id).State)
		assert.Contains(t, *ie.rowState(id).LastError, "no longer linked")
	})

	t.Run("a retired link, or a link to another SKU, is skipped", func(t *testing.T) {
		now := ie.clock.Now()
		tuna := ie.catalogLink(domain.EntityProduct, refTuna)
		require.NoError(t, ie.mod.CatalogLinks.SetRetired(t.Context(), tuna.ID, &now))
		retired := ie.inventoryRow(inventoryPayload(ie.fx.TunaSushi.ID, refTuna))
		changed := ie.inventoryRow(inventoryPayload(ie.fx.SalmonSushi.ID, "AN-OLD-REF"))
		ie.drainAll()
		assert.Equal(t, domain.OutboxSkipped, ie.rowState(retired).State)
		assert.Equal(t, domain.OutboxSkipped, ie.rowState(changed).State)
		assert.Contains(t, *ie.rowState(changed).LastError, "changed")
		require.NoError(t, ie.mod.CatalogLinks.SetRetired(t.Context(), tuna.ID, nil))
	})

	t.Run("a product that no longer exists is skipped", func(t *testing.T) {
		// The link would go with the product (cascade): drop the constraint to reach the worker's guard.
		ie.exec(`ALTER TABLE hubrise_catalog_links DROP CONSTRAINT hubrise_catalog_links_product_id_fkey`)
		ie.exec(`DELETE FROM hubrise_catalog_links WHERE product_id = $1`, ie.fx.MochiIce.ID)
		ghost := uuid.New()
		ie.exec(`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin) VALUES ('product', $1, 'GHOST', 'GHOST', 'manual')`, ghost)
		id := ie.inventoryRow(inventoryPayload(ghost, "GHOST"))
		ie.drainAll()
		assert.Equal(t, domain.OutboxSkipped, ie.rowState(id).State)
		assert.Contains(t, *ie.rowState(id).LastError, "no longer exists")
	})

	t.Run("HubRise refusing the SKU parks the row", func(t *testing.T) {
		ie.linkProduct(ie.fx.MochiIce.ID, "NOT-IN-THE-CATALOG")
		id := ie.inventoryRow(inventoryPayload(ie.fx.MochiIce.ID, "NOT-IN-THE-CATALOG"))
		ie.drainAll()
		assert.Equal(t, domain.OutboxParked, ie.rowState(id).State)
		assert.Contains(t, *ie.rowState(id).LastError, "sku_ref")
	})

	t.Run("a server error is retried with backoff", func(t *testing.T) {
		ie.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: "/v1/location/inventory", Status: http.StatusInternalServerError, Times: 1})
		id := ie.inventoryRow(inventoryPayload(ie.fx.SalmonSushi.ID, refSalmon))
		ie.drain()
		got := ie.rowState(id)
		assert.Equal(t, domain.OutboxPending, got.State)
		assert.True(t, got.NextAttemptAt.After(ie.clock.Now()))
		ie.clock.Advance(time.Minute)
		ie.drainAll()
		assert.Equal(t, domain.OutboxSent, ie.rowState(id).State)
	})

	t.Run("a refused token pauses the pipeline and gives the row back", func(t *testing.T) {
		ie.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: "/v1/location/inventory", Status: http.StatusUnauthorized, Times: 1})
		id := ie.inventoryRow(inventoryPayload(ie.fx.SalmonSushi.ID, refSalmon))
		ie.drain()
		got := ie.rowState(id)
		assert.Equal(t, domain.OutboxPending, got.State)
		assert.Zero(t, got.Attempts, "a connection-level refusal burns no attempt")
		conn, err := ie.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.NotNil(t, conn.AuthErrorAt)
	})
}

func TestInventoryWorkerFailsOnAStoreErrorAndRetries(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	id := ie.inventoryRow(inventoryPayload(ie.fx.SalmonSushi.ID, refSalmon))
	f := newFaults("Links.FindByLocal")
	w := application.NewOutboxWorker(application.WorkerDeps{
		Connections: ie.mod.Connections, Outbox: ie.mod.Outbox, Orders: ie.mod.Orders, Links: catalogLinkFaults{ie.mod.CatalogLinks, f},
		Availability: storeFaults{AvailabilityStore: ie.mod.Availability, f: f}, Source: ie.mod.Source,
		Clients: fixedClients{c: ie.srv.NewClient(t)}, Clock: ie.clock,
	})
	n, err := w.DrainOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.OutboxPending, ie.rowState(id).State)

	f.set("Links.FindByLocal", nil)
	f.set("Store.Availability.product", errStore)
	ie.clock.Advance(time.Hour)
	_, err = w.DrainOnce(t.Context())
	require.NoError(t, err)
	got := ie.rowState(id)
	assert.Equal(t, domain.OutboxPending, got.State)
	assert.Contains(t, *got.LastError, "store down")
}

func TestInventoryRowsAreOnlyClaimedByAWorkerThatCanSendThem(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	id := ie.inventoryRow(inventoryPayload(ie.fx.SalmonSushi.ID, refSalmon))
	w := application.NewOutboxWorker(application.WorkerDeps{
		Connections: ie.mod.Connections, Outbox: ie.mod.Outbox, Orders: ie.mod.Orders, Links: ie.mod.CatalogLinks,
		Source: ie.mod.Source, Clients: fixedClients{c: ie.srv.NewClient(t)}, Clock: ie.clock,
	})
	n, err := w.DrainOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, domain.OutboxPending, ie.rowState(id).State, "left for a worker with the availability store")
}

// The first-enable baseline end to end: the rows it queues are sent by the same worker as everything
// else, and the product states HubRise ends up with are those of the shop.
func TestInventoryFirstEnableBaselineEndToEnd(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.setAvailable(ie.fx.SalmonSushi.ID, false)
	ie.setAvailable(ie.fx.MochiIce.ID, false)
	// HubRise says tea is sold out, which the shop did not know.
	ie.setInventory(pkghubrise.SoldOutSku(refTea, nil))

	res := ie.sync()
	assert.Equal(t, 2, res.BaselinePushed)
	assert.Equal(t, 1, res.SoldOut)
	ie.drainAll()
	assert.ElementsMatch(t, []pkghubrise.InventoryEntry{
		pkghubrise.SoldOutSku(refTea, nil), pkghubrise.SoldOutSku(refSalmon, nil), pkghubrise.SoldOutSku(refMochi, nil),
	}, ie.srv.Inventory())
	assert.False(t, ie.available(ie.fx.GreenTea.ID))
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
	assert.True(t, ie.available(ie.fx.TunaSushi.ID))

	// Now that HubRise holds every sell out, the next read only records the two the shop pushed
	// (it does not own them), and the one after that has nothing to tell.
	ie.clock.Advance(31 * time.Minute)
	ie.srv.ClearRequests()
	res = ie.sync()
	assert.Equal(t, application.InventoryResult{Recorded: 2}, res)
	assert.False(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut)
	assert.Equal(t, application.InventoryResult{}, ie.sync())
	assert.Zero(t, ie.inventoryWrites())
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
}

// flipOutbox switches inventory sync off right after a claim: the window in which the owner can turn
// the sync off under rows that are already leased.
type flipOutbox struct {
	domain.OutboxRepository
	off func()
}

func (f flipOutbox) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int, kinds ...domain.OutboxKind) ([]domain.OutboxItem, error) {
	items, err := f.OutboxRepository.Claim(ctx, now, lease, limit, kinds...)
	f.off()
	return items, err
}

func TestInventoryWorkerSkipsARowOnceInventorySyncIsOff(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	w := application.NewOutboxWorker(application.WorkerDeps{
		Connections: ie.mod.Connections, Orders: ie.mod.Orders, Links: ie.mod.CatalogLinks,
		Outbox:       flipOutbox{ie.mod.Outbox, func() { ie.exec(`UPDATE hubrise_connection SET sync_inventory = false`) }},
		Availability: ie.mod.Availability, Source: ie.mod.Source, Clock: ie.clock, Clients: ie.opts.Clients,
	})
	id := ie.inventoryRow(inventoryPayload(ie.fx.SalmonSushi.ID, refSalmon))
	before := len(ie.srv.Requests())

	n, err := w.DrainOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	got := ie.rowState(id)
	assert.Equal(t, domain.OutboxSkipped, got.State)
	assert.Contains(t, *got.LastError, "inventory sync is off")
	assert.Len(t, ie.srv.Requests(), before, "nothing was sent")
}
