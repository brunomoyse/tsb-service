package application_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
)

// SKU and option refs of the mock's seed catalog, linked to the fixture products.
const (
	refSalmon   = "MAKI-SAUMON-6" // SalmonSushi
	refTuna     = "CALIFORNIA-8"  // TunaSushi
	refTea      = "COCA-33"       // GreenTea
	refMochi    = "MISO-1"        // MochiIce
	refSauce    = "SAUCE-SOJA"    // an option of the SAUCE list
	refOtherOpt = "SAUCE-SUCREE"  // another option of the same list
)

// notifier records the products announced on productUpdated.
type notifier struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (n *notifier) PublishProductUpdated(_ context.Context, id uuid.UUID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ids = append(n.ids, id)
}

func (n *notifier) all() []uuid.UUID {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]uuid.UUID(nil), n.ids...)
}

func (n *notifier) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ids = nil
}

// driftSpy stands in for the catalog service: it counts the drift checks and remembers the class of
// the rate limiter the calls were made with.
type driftSpy struct {
	mu      sync.Mutex
	calls   int
	classes []pkghubrise.Class
	err     error
}

func (d *driftSpy) CheckDrift(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.classes = append(d.classes, pkghubrise.ClassFrom(ctx))
	return d.err
}

func (d *driftSpy) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// invEnv is an env with the inventory sync built next to the push, the way a replica runs it.
type invEnv struct {
	*env
	inv   *hubrise.Inventory
	notes *notifier
	drift *driftSpy
}

func newInvEnv(t *testing.T, options ...envOption) *invEnv {
	t.Helper()
	e := newEnv(t, append([]envOption{withSharedClock()}, options...)...)
	ie := &invEnv{env: e, notes: &notifier{}, drift: &driftSpy{}}
	ie.inv = ie.replica("replica-a")
	return ie
}

// replica builds another set of inventory workers on the same database and mock.
func (ie *invEnv) replica(holder string) *hubrise.Inventory {
	return ie.mod.NewInventory(ie.push, hubrise.InventoryOptions{Holder: holder, Notifier: ie.notes, Drift: ie.drift})
}

// linkAll links the four fixture products to SKU refs of the mock's catalog.
func (ie *invEnv) linkAll() {
	ie.linkProduct(ie.fx.SalmonSushi.ID, refSalmon)
	ie.linkProduct(ie.fx.TunaSushi.ID, refTuna)
	ie.linkProduct(ie.fx.GreenTea.ID, refTea)
	ie.linkProduct(ie.fx.MochiIce.ID, refMochi)
}

// stampBaseline marks the first-enable baseline as done, as it is for an installation that has been
// syncing for a while.
func (ie *invEnv) stampBaseline() {
	ie.t.Helper()
	require.NoError(ie.t, ie.mod.Connections.SetInventoryBaseline(ie.t.Context(), ie.clock.Now()))
}

func (ie *invEnv) sync() application.InventoryResult {
	ie.t.Helper()
	res, err := ie.inv.Service.Sync(ie.t.Context(), false)
	require.NoError(ie.t, err)
	return res
}

func (ie *invEnv) available(productID uuid.UUID) bool {
	ie.t.Helper()
	var v bool
	require.NoError(ie.t, ie.db.GetContext(ie.t.Context(), &v, `SELECT is_available FROM products WHERE id = $1`, productID))
	return v
}

func (ie *invEnv) choiceAvailable(choiceID uuid.UUID) bool {
	ie.t.Helper()
	var v bool
	require.NoError(ie.t, ie.db.GetContext(ie.t.Context(), &v, `SELECT is_available FROM product_choices WHERE id = $1`, choiceID))
	return v
}

func (ie *invEnv) setAvailable(productID uuid.UUID, v bool) {
	ie.t.Helper()
	ie.exec(`UPDATE products SET is_available = $2 WHERE id = $1`, productID, v)
}

func (ie *invEnv) catalogLink(t domain.EntityType, key string) *domain.CatalogLink {
	ie.t.Helper()
	l, err := ie.mod.CatalogLinks.FindByKey(ie.t.Context(), t, key)
	require.NoError(ie.t, err)
	return l
}

// newChoice adds a choice to a product and links it to an option ref.
func (ie *invEnv) newChoice(productID uuid.UUID, key, optionRef string) uuid.UUID {
	ie.t.Helper()
	groupID, choiceID := uuid.New(), uuid.New()
	ie.exec(`INSERT INTO product_choice_groups (id, product_id) VALUES ($1, $2)`, groupID, productID)
	ie.exec(`INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier) VALUES ($1, $2, $3, 0.50)`, choiceID, productID, groupID)
	ie.linkChoice(choiceID, key, optionRef)
	return choiceID
}

func stockEntry(ref, stock string) pkghubrise.InventoryEntry {
	return pkghubrise.InventoryEntry{SkuRef: ref, Stock: &stock}
}

func optionEntry(ref, stock string) pkghubrise.InventoryEntry {
	return pkghubrise.InventoryEntry{OptionRef: ref, Stock: &stock}
}

func (ie *invEnv) setInventory(entries ...pkghubrise.InventoryEntry) {
	ie.t.Helper()
	require.NoError(ie.t, ie.srv.SetInventory(entries))
}

// inventoryWrites counts the calls that wrote the inventory at HubRise.
func (ie *invEnv) inventoryWrites() int {
	return len(ie.srv.RequestsTo(http.MethodPatch, "/v1/location/inventory")) +
		len(ie.srv.RequestsTo(http.MethodPut, "/v1/location/inventory"))
}

func (ie *invEnv) outboxCount(kind domain.OutboxKind) int {
	return ie.count(`SELECT count(*) FROM hubrise_outbox WHERE kind = $1`, kind)
}

func TestInventorySoldOutProductsBecomeUnavailableAndComeBack(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	until := ie.clock.Now().Add(2 * time.Hour).Truncate(time.Second)

	// HubRise sells out two SKUs, one of them until a given time.
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil), pkghubrise.SoldOutSku(refTuna, &until), stockEntry(refTea, "12"))
	ie.srv.ClearRequests()
	res := ie.sync()

	assert.Equal(t, 2, res.SoldOut)
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
	assert.False(t, ie.available(ie.fx.TunaSushi.ID))
	assert.True(t, ie.available(ie.fx.GreenTea.ID), "a positive stock is not a sell out")
	assert.True(t, ie.available(ie.fx.MochiIce.ID), "no entry: unlimited")
	assert.ElementsMatch(t, []uuid.UUID{ie.fx.SalmonSushi.ID, ie.fx.TunaSushi.ID}, ie.notes.all(), "productUpdated for what changed")

	salmon := ie.catalogLink(domain.EntityProduct, refSalmon)
	assert.True(t, salmon.InventorySoldOut)
	assert.Nil(t, salmon.SoldOutUntil)
	require.NotNil(t, salmon.Stock)
	assert.True(t, salmon.Stock.IsZero())
	tuna := ie.catalogLink(domain.EntityProduct, refTuna)
	assert.True(t, tuna.InventorySoldOut)
	require.NotNil(t, tuna.SoldOutUntil)
	assert.WithinDuration(t, until, *tuna.SoldOutUntil, time.Second)
	tea := ie.catalogLink(domain.EntityProduct, refTea)
	assert.False(t, tea.InventorySoldOut)
	require.NotNil(t, tea.Stock)
	assert.True(t, tea.Stock.Equal(decimal.NewFromInt(12)), "the stock is stored for display")

	t.Run("no echo: nothing leaves for HubRise and nothing is queued", func(t *testing.T) {
		assert.Zero(t, ie.outboxCount(domain.OutboxInventory))
		assert.Zero(t, ie.inventoryWrites())
	})

	t.Run("a second sync with the same inventory changes and announces nothing", func(t *testing.T) {
		ie.notes.reset()
		res := ie.sync()
		assert.Equal(t, application.InventoryResult{}, res)
		assert.Empty(t, ie.notes.all())
	})

	t.Run("an entry that disappears gives the product back, a positive stock too", func(t *testing.T) {
		ie.setInventory(stockEntry(refTuna, "3"), stockEntry(refTea, "12"))
		res := ie.sync()
		assert.Equal(t, 2, res.Released)
		assert.True(t, ie.available(ie.fx.SalmonSushi.ID), "absent entry: unlimited again")
		assert.True(t, ie.available(ie.fx.TunaSushi.ID), "positive stock: available again")
		salmon := ie.catalogLink(domain.EntityProduct, refSalmon)
		assert.False(t, salmon.InventorySoldOut)
		assert.Nil(t, salmon.Stock)
		tuna := ie.catalogLink(domain.EntityProduct, refTuna)
		assert.False(t, tuna.InventorySoldOut)
		assert.Nil(t, tuna.SoldOutUntil)
		require.NotNil(t, tuna.Stock)
		assert.True(t, tuna.Stock.Equal(decimal.NewFromInt(3)))
		assert.Zero(t, ie.outboxCount(domain.OutboxInventory), "still no echo")
	})
}

func TestInventoryUnknownAndUnlinkedRefsAreIgnored(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkProduct(ie.fx.SalmonSushi.ID, refSalmon)
	ie.stampBaseline()
	// Tuna has no link: HubRise selling it out changes nothing here.
	ie.setInventory(pkghubrise.SoldOutSku(refTuna, nil), optionEntry(refSauce, "0"))
	res := ie.sync()
	assert.Equal(t, application.InventoryResult{}, res)
	assert.True(t, ie.available(ie.fx.TunaSushi.ID))
	assert.Empty(t, ie.notes.all())
}

func TestInventoryManualUnavailabilitySurvivesSyncAndPoll(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	// The shop sold salmon out by hand before HubRise was connected. Tuna is HubRise's business.
	ie.setAvailable(ie.fx.SalmonSushi.ID, false)

	// Enabling the sync: HubRise knows nothing yet, the baseline pushes the manual sell out.
	res := ie.sync()
	assert.Equal(t, 1, res.BaselinePushed)
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID), "a first sync does not restore what the shop sold out")
	conn, err := ie.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, conn.InventoryBaselineAt, "the baseline is stamped")

	// A full sync and the 30 minute poll with the inventory still empty (the PATCH has not left yet),
	// then with a positive stock, then with the SKU sold out at HubRise too: never restored, never
	// owned.
	for _, entries := range [][]pkghubrise.InventoryEntry{
		nil,
		{stockEntry(refSalmon, "8")},
		{pkghubrise.SoldOutSku(refSalmon, nil)},
		nil,
	} {
		ie.setInventory(entries...)
		ie.sync()
		_, err := ie.inv.Jobs.PollOnce(t.Context())
		require.NoError(t, err)
		ie.clock.Advance(31 * time.Minute)
		assert.False(t, ie.available(ie.fx.SalmonSushi.ID), "entries %v", entries)
		assert.False(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut, "never owned")
	}
	// The queued PATCH that tells HubRise is still a single row.
	assert.Equal(t, 1, ie.count(`SELECT count(*) FROM hubrise_outbox WHERE kind = 'inventory' AND state = 'pending'`))
}

func TestInventoryManualUnavailabilityWithSoldOutEntryIsRecordedNotOwned(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	ie.setAvailable(ie.fx.SalmonSushi.ID, false)
	until := ie.clock.Now().Add(time.Hour).Truncate(time.Second)

	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, &until))
	res := ie.sync()
	assert.Equal(t, 1, res.Recorded)
	assert.Zero(t, res.SoldOut)
	salmon := ie.catalogLink(domain.EntityProduct, refSalmon)
	assert.False(t, salmon.InventorySoldOut, "the shop's own reason is not taken over")
	require.NotNil(t, salmon.Stock)
	assert.True(t, salmon.Stock.IsZero(), "but the state is recorded")
	require.NotNil(t, salmon.SoldOutUntil)

	// HubRise gets stock back: the shop's unavailability stays, and the expiry sweeper ignores it.
	ie.setInventory(stockEntry(refSalmon, "4"))
	ie.sync()
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
	ie.clock.Advance(3 * time.Hour)
	n, err := ie.inv.Service.ReleaseExpired(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
}

func TestInventoryAbsentEntryNeverRestoresBeforeTheBaselineIsStamped(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	// Inventory took salmon away earlier (an event arrived before the first sync finished).
	link := ie.catalogLink(domain.EntityProduct, refSalmon)
	_, err := ie.mod.Availability.TakeOver(t.Context(), link, domain.InventoryState{SyncedAt: ie.clock.Now()})
	require.NoError(t, err)
	require.False(t, ie.available(ie.fx.SalmonSushi.ID))

	// The baseline cannot be stamped (the store refuses the write): the sync reports it and restores
	// nothing, because an absent entry proves nothing yet.
	ie.exec(`CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`)
	ie.exec(`CREATE TRIGGER t BEFORE UPDATE ON hubrise_connection FOR EACH ROW EXECUTE FUNCTION f()`)
	_, err = ie.inv.Service.Sync(t.Context(), false)
	require.Error(t, err)
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID), "no restore without a stamped baseline")
	assert.True(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut)

	// With the baseline stamped the same absent entry gives the product back.
	ie.exec(`DROP TRIGGER t ON hubrise_connection`)
	res := ie.sync()
	assert.Equal(t, 1, res.Released)
	assert.True(t, ie.available(ie.fx.SalmonSushi.ID))
}

func TestInventoryBaselinePushesTheShopsSoldOutProductsOnce(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.setAvailable(ie.fx.SalmonSushi.ID, false)
	ie.setAvailable(ie.fx.TunaSushi.ID, false)
	// Tuna is unavailable because HubRise's catalog disables it, and the tea is sold out by inventory:
	// HubRise already knows both.
	tuna := ie.catalogLink(domain.EntityProduct, refTuna)
	require.NoError(t, ie.mod.CatalogLinks.SetCatalogDisabled(t.Context(), tuna.ID, true))
	tea := ie.catalogLink(domain.EntityProduct, refTea)
	_, err := ie.mod.Availability.TakeOver(t.Context(), tea, domain.InventoryState{SyncedAt: ie.clock.Now()})
	require.NoError(t, err)

	n, err := ie.inv.Service.Baseline(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the salmon is the shop's own decision")
	ie.drainAll()
	assert.Equal(t, []pkghubrise.InventoryEntry{pkghubrise.SoldOutSku(refSalmon, nil)}, ie.srv.Inventory())

	again, err := ie.inv.Service.Baseline(t.Context())
	require.NoError(t, err)
	assert.Zero(t, again, "the baseline runs once")
}

func TestInventoryOptionsSellOutEveryChoiceWithTheRef(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	onSalmon := ie.newChoice(ie.fx.SalmonSushi.ID, refSalmon+"|SAUCE|"+refSauce, refSauce)
	onTuna := ie.newChoice(ie.fx.TunaSushi.ID, refTuna+"|SAUCE|"+refSauce, refSauce)
	other := ie.newChoice(ie.fx.TunaSushi.ID, refTuna+"|SAUCE|"+refOtherOpt, refOtherOpt)

	ie.setInventory(optionEntry(refSauce, "0"))
	res := ie.sync()
	assert.Equal(t, 2, res.SoldOut)
	assert.False(t, ie.choiceAvailable(onSalmon))
	assert.False(t, ie.choiceAvailable(onTuna))
	assert.True(t, ie.choiceAvailable(other))
	assert.True(t, ie.available(ie.fx.SalmonSushi.ID), "the products themselves stay orderable")
	assert.ElementsMatch(t, []uuid.UUID{ie.fx.SalmonSushi.ID, ie.fx.TunaSushi.ID}, ie.notes.all(),
		"the product of each choice is announced")
	assert.Zero(t, ie.outboxCount(domain.OutboxInventory), "no echo")

	ie.setInventory()
	res = ie.sync()
	assert.Equal(t, 2, res.Released)
	assert.True(t, ie.choiceAvailable(onSalmon))
	assert.True(t, ie.choiceAvailable(onTuna))
}

func TestInventoryHubRiseDisabledEntriesStayUnavailable(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	choice := ie.newChoice(ie.fx.SalmonSushi.ID, refSalmon+"|SAUCE|"+refSauce, refSauce)

	// The catalog disables the option (restrictions) and the inventory sells it out as well: the
	// choice is unavailable twice over, and inventory owns its half.
	l := ie.catalogLink(domain.EntityChoice, refSalmon+"|SAUCE|"+refSauce)
	require.NoError(t, ie.mod.CatalogLinks.SetCatalogDisabled(t.Context(), l.ID, true))
	ie.exec(`UPDATE product_choices SET is_available = false WHERE id = $1`, choice)
	ie.setInventory(optionEntry(refSauce, "0"))
	ie.sync()
	assert.True(t, ie.catalogLink(domain.EntityChoice, refSalmon+"|SAUCE|"+refSauce).InventorySoldOut)

	// The stock comes back: the option is still disabled by the catalog, so it stays unavailable.
	ie.setInventory()
	ie.sync()
	assert.False(t, ie.catalogLink(domain.EntityChoice, refSalmon+"|SAUCE|"+refSauce).InventorySoldOut)
	assert.False(t, ie.choiceAvailable(choice), "effective availability is not catalog_disabled and not sold out")
}

func TestInventoryExpirySweeperReleasesOnlyWhatInventoryTookAway(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	soon := ie.clock.Now().Add(10 * time.Minute).Truncate(time.Second)
	later := ie.clock.Now().Add(5 * time.Hour).Truncate(time.Second)
	ie.setAvailable(ie.fx.MochiIce.ID, false) // the shop's own sell out, with an entry that also expires

	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, &soon), pkghubrise.SoldOutSku(refTuna, &later), pkghubrise.SoldOutSku(refMochi, &soon))
	ie.sync()
	require.False(t, ie.available(ie.fx.SalmonSushi.ID))
	require.False(t, ie.available(ie.fx.TunaSushi.ID))
	ie.notes.reset()

	n, err := ie.inv.Service.ReleaseExpired(t.Context())
	require.NoError(t, err)
	assert.Zero(t, n, "nothing has expired yet")

	ie.clock.Advance(15 * time.Minute)
	ran, err := ie.inv.Jobs.SweepOnce(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	assert.True(t, ie.available(ie.fx.SalmonSushi.ID), "inventory took it away, the expiry gives it back")
	assert.False(t, ie.available(ie.fx.TunaSushi.ID), "its expiry is later")
	assert.False(t, ie.available(ie.fx.MochiIce.ID), "the shop's own unavailability is never released")
	assert.Equal(t, []uuid.UUID{ie.fx.SalmonSushi.ID}, ie.notes.all())
	salmon := ie.catalogLink(domain.EntityProduct, refSalmon)
	assert.False(t, salmon.InventorySoldOut)
	assert.Nil(t, salmon.SoldOutUntil)

	// The sweeper shares its cadence between replicas: another one does not run within the minute.
	ran, err = ie.replica("replica-b").Jobs.SweepOnce(t.Context())
	require.NoError(t, err)
	assert.False(t, ran)
}

func TestInventoryLocalToggleClearsProvenanceAndPushesTheCurrentState(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
	ie.sync()
	require.False(t, ie.available(ie.fx.SalmonSushi.ID))
	ie.notes.reset()

	// Staff make the product available again in the dashboard.
	ie.setAvailable(ie.fx.SalmonSushi.ID, true)
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.SalmonSushi.ID, true))
	salmon := ie.catalogLink(domain.EntityProduct, refSalmon)
	assert.False(t, salmon.InventorySoldOut, "the shop owns the state now")
	assert.Equal(t, 1, ie.count(`SELECT count(*) FROM hubrise_outbox WHERE kind = 'inventory' AND state = 'pending' AND dedupe_key = $1`,
		domain.InventoryDedupeKey(refSalmon)))

	// An inventory read that still shows the old sell out (the PATCH has not left) must not put the
	// product back to sleep.
	ie.sync()
	assert.True(t, ie.available(ie.fx.SalmonSushi.ID), "a pending local change wins over a stale answer")
	assert.False(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut)

	// The worker sends the state the product has now: the entry is removed at HubRise.
	ie.drainAll()
	assert.Empty(t, ie.srv.Inventory())
	patches := ie.srv.RequestsTo(http.MethodPatch, "/v1/location/inventory")
	require.Len(t, patches, 1)

	// Selling it out again by hand is pushed as stock 0 and never owned by inventory.
	ie.setAvailable(ie.fx.SalmonSushi.ID, false)
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.SalmonSushi.ID, false))
	ie.drainAll()
	assert.Equal(t, []pkghubrise.InventoryEntry{pkghubrise.SoldOutSku(refSalmon, nil)}, ie.srv.Inventory())
	ie.sync()
	assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
	assert.False(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut)
}

func TestInventoryTogglesCollapseAndSendTheLatestState(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()

	// Off and on again before the worker looked: one pending row, and the payload is built at send time.
	ie.setAvailable(ie.fx.SalmonSushi.ID, false)
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.SalmonSushi.ID, false))
	ie.setAvailable(ie.fx.SalmonSushi.ID, true)
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.SalmonSushi.ID, true))
	assert.Equal(t, 1, ie.outboxCount(domain.OutboxInventory))
	ie.drainAll()
	assert.Empty(t, ie.srv.Inventory())

	// A toggle made while the previous row is being sent is never merged into it: it queues its own
	// row, sent after.
	ie.setAvailable(ie.fx.TunaSushi.ID, false)
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.TunaSushi.ID, false))
	toggled := false
	ie.opts.Clients = fixedClients{c: ie.srv.NewClient(t, pkghubrise.WithHTTPClient(&http.Client{Transport: hookTransport{
		next: http.DefaultTransport,
		before: func(r *http.Request) {
			if !toggled && r.Method == http.MethodPatch {
				toggled = true
				ie.setAvailable(ie.fx.TunaSushi.ID, true)
				require.NoError(t, ie.inv.Service.AvailabilityChanged(r.Context(), ie.fx.TunaSushi.ID, true))
			}
		},
	}}))}
	ie.push = ie.rebuild(ie.opts)
	ie.inv = ie.mod.NewInventory(ie.push, hubrise.InventoryOptions{Holder: "replica-a", Notifier: ie.notes})
	ie.drainAll()
	assert.True(t, toggled)
	assert.Empty(t, ie.srv.Inventory(), "the first row sold tuna out, the second one made it available again")
	assert.Len(t, ie.srv.RequestsTo(http.MethodPatch, "/v1/location/inventory"), 3)
}

func TestInventoryHookIgnoresWhatIsNotLinkedOrNotActive(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkProduct(ie.fx.SalmonSushi.ID, refSalmon)
	ctx := t.Context()

	// Unlinked product: nothing to tell HubRise.
	require.NoError(t, ie.inv.Service.AvailabilityChanged(ctx, ie.fx.TunaSushi.ID, false))
	assert.Zero(t, ie.outboxCount(domain.OutboxInventory))

	// Retired link.
	salmon := ie.catalogLink(domain.EntityProduct, refSalmon)
	now := ie.clock.Now()
	require.NoError(t, ie.mod.CatalogLinks.SetRetired(ctx, salmon.ID, &now))
	require.NoError(t, ie.inv.Service.AvailabilityChanged(ctx, ie.fx.SalmonSushi.ID, false))
	assert.Zero(t, ie.outboxCount(domain.OutboxInventory))
	require.NoError(t, ie.mod.CatalogLinks.SetRetired(ctx, salmon.ID, nil))

	// The toggles of the connection and its state.
	for name, set := range map[string]string{
		"disabled":           `enabled = false`,
		"inventory sync off": `sync_inventory = false`,
		"relink pending":     `relink_required_at = now()`,
	} {
		ie.exec(`UPDATE hubrise_connection SET ` + set)
		require.NoError(t, ie.inv.Service.AvailabilityChanged(ctx, ie.fx.SalmonSushi.ID, false), name)
		assert.Zero(t, ie.outboxCount(domain.OutboxInventory), name)
		ie.exec(`UPDATE hubrise_connection SET enabled = true, sync_inventory = true, auth_error_at = NULL, relink_required_at = NULL`)
	}

	ie.exec(`DELETE FROM hubrise_connection`)
	require.NoError(t, ie.inv.Service.AvailabilityChanged(ctx, ie.fx.SalmonSushi.ID, false))
	assert.Zero(t, ie.outboxCount(domain.OutboxInventory), "not connected")
}

// An authorization failure pauses the pipeline, not the shop: what staff change meanwhile is queued
// and goes out when the connection works again, so HubRise and the shop do not stay apart.
func TestInventoryTogglesMadeWhilePausedByAnAuthorizationErrorAreSentAfterwards(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
	ie.sync()
	require.False(t, ie.available(ie.fx.SalmonSushi.ID))
	require.True(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut)

	require.NoError(t, ie.mod.Connections.RecordAuthError(t.Context(), ie.clock.Now(), "revoked"))
	ie.setAvailable(ie.fx.SalmonSushi.ID, true) // staff bring the product back
	ie.setAvailable(ie.fx.TunaSushi.ID, false)  // and sell another one out
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.SalmonSushi.ID, true))
	require.NoError(t, ie.inv.Service.AvailabilityChanged(t.Context(), ie.fx.TunaSushi.ID, false))
	assert.Equal(t, 2, ie.outboxCount(domain.OutboxInventory), "queued, not lost")
	assert.False(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut, "the stale provenance cannot release a staff decision later")
	ie.srv.ClearRequests()
	ie.drainAll()
	assert.Empty(t, ie.srv.Requests(), "nothing leaves while the pause lasts")

	require.NoError(t, ie.mod.Connections.ClearAuthError(t.Context()))
	ie.drainAll()
	assert.Equal(t, []pkghubrise.InventoryEntry{pkghubrise.SoldOutSku(refTuna, nil)}, ie.srv.Inventory(), "HubRise now has the shop's state")
}

func TestInventoryPausedOrDisconnectedDoesNothing(t *testing.T) {
	for name, set := range map[string]string{
		"disabled":              `enabled = false`,
		"inventory sync off":    `sync_inventory = false`,
		"authorization failure": `auth_error_at = now()`,
		"disconnected":          `access_token_enc = NULL`,
	} {
		t.Run(name, func(t *testing.T) {
			ie := newInvEnv(t)
			ie.linkAll()
			ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil))
			ie.setAvailable(ie.fx.TunaSushi.ID, false)
			ie.exec(`UPDATE hubrise_connection SET ` + set)
			ie.srv.ClearRequests()

			res, err := ie.inv.Service.Sync(t.Context(), false)
			require.NoError(t, err)
			assert.Equal(t, application.InventoryResult{}, res)
			n, err := ie.inv.Service.Baseline(t.Context())
			require.NoError(t, err)
			assert.Zero(t, n)
			released, err := ie.inv.Service.ReleaseExpired(t.Context())
			require.NoError(t, err)
			assert.Zero(t, released)
			_, err = ie.inv.Jobs.PollOnce(t.Context())
			require.NoError(t, err)

			assert.Empty(t, ie.srv.Requests(), "no HubRise call")
			assert.True(t, ie.available(ie.fx.SalmonSushi.ID))
			assert.Zero(t, ie.outboxCount(domain.OutboxInventory))
			conn, err := ie.mod.Connections.Get(t.Context())
			require.NoError(t, err)
			assert.Nil(t, conn.InventoryBaselineAt)
		})
	}

	t.Run("no connection at all", func(t *testing.T) {
		ie := newInvEnv(t, withoutConnection())
		res, err := ie.inv.Service.Sync(t.Context(), false)
		require.NoError(t, err)
		assert.Equal(t, application.InventoryResult{}, res)
		assert.Empty(t, ie.srv.Requests())
	})
}

func TestInventoryTwoReplicasAreSafe(t *testing.T) {
	ie := newInvEnv(t)
	ie.linkAll()
	ie.stampBaseline()
	other := ie.replica("replica-b")
	ie.setInventory(pkghubrise.SoldOutSku(refSalmon, nil), pkghubrise.SoldOutSku(refTuna, nil))

	t.Run("a replica that finds the other one syncing waits for its turn", func(t *testing.T) {
		ok, err := ie.mod.Leases.Acquire(t.Context(), application.LeaseInventorySync, "replica-b", ie.clock.Now(), time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = ie.inv.Service.Sync(t.Context(), false)
		assert.ErrorIs(t, err, application.ErrInventoryBusy)
		_, err = ie.inv.Service.ReleaseExpired(t.Context())
		assert.ErrorIs(t, err, application.ErrInventoryBusy)
		// The periodic jobs take it as "somebody else is doing it".
		_, err = ie.inv.Jobs.PollOnce(t.Context())
		assert.NoError(t, err)
		_, err = ie.inv.Jobs.SweepOnce(t.Context())
		assert.NoError(t, err)
		assert.True(t, ie.available(ie.fx.SalmonSushi.ID))
		require.NoError(t, ie.mod.Leases.Release(t.Context(), application.LeaseInventorySync, "replica-b"))
	})

	t.Run("both syncing at once end in the same state, each change announced by whoever made it", func(t *testing.T) {
		var wg sync.WaitGroup
		for _, svc := range []*application.InventoryService{ie.inv.Service, other.Service} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 5 {
					if _, err := svc.Sync(t.Context(), false); err != nil {
						assert.ErrorIs(t, err, application.ErrInventoryBusy)
					}
				}
			}()
		}
		wg.Wait()
		assert.False(t, ie.available(ie.fx.SalmonSushi.ID))
		assert.False(t, ie.available(ie.fx.TunaSushi.ID))
		assert.True(t, ie.catalogLink(domain.EntityProduct, refSalmon).InventorySoldOut)
		assert.Len(t, ie.notes.all(), 2, "two products changed once, whoever applied them")
		assert.Zero(t, ie.outboxCount(domain.OutboxInventory))
	})
}

func TestInventoryPollIsABackgroundCallAndEventsAreNot(t *testing.T) {
	var mu sync.Mutex
	classes := map[string]pkghubrise.Class{}
	e := newInvEnv(t, withClients(func(e *env) clientProvider {
		return fixedClients{c: e.srv.NewClient(t, pkghubrise.WithHTTPClient(&http.Client{Transport: hookTransport{
			next: http.DefaultTransport,
			before: func(r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				classes[r.URL.Path] = pkghubrise.ClassFrom(r.Context())
			},
		}}))}
	}))
	e.stampBaseline()

	ran, err := e.inv.Jobs.PollOnce(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)
	mu.Lock()
	assert.Equal(t, pkghubrise.ClassBackground, classes["/v1/location/inventory"], "the poll is background work")
	classes = map[string]pkghubrise.Class{}
	mu.Unlock()

	_, err = e.inv.Service.Sync(t.Context(), false)
	require.NoError(t, err)
	mu.Lock()
	assert.Equal(t, pkghubrise.ClassCritical, classes["/v1/location/inventory"], "an event driven read is not")
	mu.Unlock()

	ran, err = e.replica("replica-b").Jobs.PollOnce(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "the poll runs once per interval across replicas")
}

// The lease treats its own holder as a renewal, so two syncs of one process must not share a holder:
// the second is turned away while the first is reading the inventory.
func TestInventoryTwoSyncsOfOneProcessDoNotRunAtTheSameTime(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	e := newInvEnv(t, withClients(func(e *env) clientProvider {
		return fixedClients{c: e.srv.NewClient(t, pkghubrise.WithHTTPClient(&http.Client{Transport: hookTransport{
			next: http.DefaultTransport,
			before: func(r *http.Request) {
				if r.URL.Path != "/v1/location/inventory" {
					return
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-time.After(10 * time.Second): // a failing test must not hang
				}
			},
		}}))}
	}))
	e.linkAll()
	e.stampBaseline()

	first := make(chan error, 1)
	go func() {
		_, err := e.inv.Service.Sync(context.WithoutCancel(t.Context()), false)
		first <- err
	}()
	<-entered // the first sync holds the lease and waits in the read

	_, err := e.inv.Service.Sync(t.Context(), false)
	assert.ErrorIs(t, err, application.ErrInventoryBusy)
	_, err = e.inv.Service.ReleaseExpired(t.Context())
	assert.ErrorIs(t, err, application.ErrInventoryBusy)

	close(release)
	require.NoError(t, <-first)
	_, err = e.inv.Service.Sync(t.Context(), false)
	assert.NoError(t, err, "the lease is free again")
}
