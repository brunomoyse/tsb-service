package graphql_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// inventoryRig is the HubRise inventory sync wired to the resolver the way the composition root does
// it: the staff toggle hook, and the resolver as the product notifier.
type inventoryRig struct {
	env  *covEnv
	srv  *hubrisetest.Server
	push *hubrise.Push
	inv  *hubrise.Inventory
}

// spyHooks counts the availability hooks.
type spyHooks struct {
	resolver.NoopHubriseHooks
	mu    sync.Mutex
	calls []bool
}

func (s *spyHooks) AvailabilityChanged(_ context.Context, _ uuid.UUID, available bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, available)
	return nil
}

func (s *spyHooks) all() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.calls...)
}

func (e *covEnv) withHubriseInventory(t *testing.T) *inventoryRig {
	t.Helper()
	srv, push := e.withHubrise(t)
	cfg := hubrise.Config{
		ClientID: hubrisetest.ClientID, ClientSecret: hubDomain.Redacted(hubrisetest.ClientSecret),
		TokenKey: hubtest.TestTokenKey, APIBaseURL: srv.APIBaseURL(), OAuthBaseURL: srv.OAuthBaseURL(),
	}
	mod, err := hubrise.NewModule(cfg, e.DB.DB, nil)
	require.NoError(t, err)
	inv := mod.NewInventory(push, hubrise.InventoryOptions{Notifier: e.Resolver})
	e.Resolver.Hubrise = resolver.HubriseInventoryHooks{Inventory: inv.Service}
	_, err = e.DB.DB.ExecContext(t.Context(), `UPDATE hubrise_connection SET inventory_baseline_at = now()`)
	require.NoError(t, err)
	return &inventoryRig{env: e, srv: srv, push: push, inv: inv}
}

func (r *inventoryRig) drainAll(t *testing.T) {
	t.Helper()
	for range 20 {
		n, err := r.push.Worker.DrainOnce(t.Context())
		require.NoError(t, err)
		if n == 0 {
			return
		}
	}
	t.Fatal("the outbox never emptied")
}

func (e *covEnv) linkProduct(t *testing.T, id uuid.UUID, ref string) {
	t.Helper()
	_, err := e.DB.DB.ExecContext(t.Context(), `
		INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin) VALUES ('product', $1, $2, $2, 'manual')`, id, ref)
	require.NoError(t, err)
}

// An availability change from the dashboard or the MCP tools reaches the hook once, and only when the
// value really changed.
func TestUpdateProductCallsTheAvailabilityHookOnlyOnAChange(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	spy := &spyHooks{}
	env.Resolver.Hubrise = spy
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	id := env.Fixtures.SalmonSushi.ID
	update := func(in model.UpdateProductInput) {
		t.Helper()
		_, err := env.Resolver.Mutation().UpdateProduct(ctx, id, in)
		require.NoError(t, err)
	}

	update(model.UpdateProductInput{IsAvailable: new(true)}) // already available
	update(model.UpdateProductInput{Price: new("13.00")})    // not about availability
	assert.Empty(t, spy.all())

	update(model.UpdateProductInput{IsAvailable: new(false)})
	update(model.UpdateProductInput{IsAvailable: new(false)}) // no change
	update(model.UpdateProductInput{IsAvailable: new(true)})
	assert.Equal(t, []bool{false, true}, spy.all())

	t.Run("a restaurant without HubRise is untouched", func(t *testing.T) {
		env.Resolver.Hubrise = nil
		update(model.UpdateProductInput{IsAvailable: new(false)})
		update(model.UpdateProductInput{IsAvailable: new(true)})
	})
}

func TestStaffSoldOutToggleIsPushedToHubRise(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	rig := env.withHubriseInventory(t)
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	id := env.Fixtures.SalmonSushi.ID
	env.linkProduct(t, id, "MAKI-SAUMON-6")

	_, err := env.Resolver.Mutation().UpdateProduct(ctx, id, model.UpdateProductInput{IsAvailable: new(false)})
	require.NoError(t, err)
	rig.drainAll(t)
	assert.Equal(t, []pkghubrise.InventoryEntry{pkghubrise.SoldOutSku("MAKI-SAUMON-6", nil)}, rig.srv.Inventory())

	_, err = env.Resolver.Mutation().UpdateProduct(ctx, id, model.UpdateProductInput{IsAvailable: new(true)})
	require.NoError(t, err)
	rig.drainAll(t)
	assert.Empty(t, rig.srv.Inventory(), "available again: the entry is removed")

	t.Run("a product HubRise does not know about is left alone", func(t *testing.T) {
		rig.srv.ClearRequests()
		_, err := env.Resolver.Mutation().UpdateProduct(ctx, env.Fixtures.TunaSushi.ID, model.UpdateProductInput{IsAvailable: new(false)})
		require.NoError(t, err)
		rig.drainAll(t)
		assert.Empty(t, rig.srv.Requests())
	})
}

func TestHubRiseSellOutReachesTheShopAndTheOpenDashboards(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	rig := env.withHubriseInventory(t)
	id := env.Fixtures.SalmonSushi.ID
	env.linkProduct(t, id, "MAKI-SAUMON-6")
	ctx := env.ctxFor(env.Fixtures.AdminUser.ID.String(), true, "en")
	updates, err := env.Resolver.Subscription().ProductUpdated(ctx)
	require.NoError(t, err)

	require.NoError(t, rig.srv.SetInventory([]pkghubrise.InventoryEntry{pkghubrise.SoldOutSku("MAKI-SAUMON-6", nil)}))
	_, err = rig.inv.Service.Sync(t.Context(), false)
	require.NoError(t, err)

	got := recvProduct(t, updates)
	assert.Equal(t, id, got.ID)
	assert.False(t, got.IsAvailable, "the dashboard sees the sell out as it happens")
	rig.srv.ClearRequests()
	rig.drainAll(t)
	assert.Empty(t, rig.srv.RequestsTo("PATCH", "/v1/location/inventory"), "and nothing is echoed back to HubRise")

	t.Run("a product that cannot be read is logged, not published", func(t *testing.T) {
		env.Resolver.PublishProductUpdated(t.Context(), uuid.New())
		select {
		case p := <-updates:
			t.Fatalf("unexpected publication: %+v", p)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

// A sold-out option cannot be ordered: the order is refused with the existing SELECTION_INVALID code,
// at quote time and at order time, and works again once HubRise has stock.
func TestSoldOutOptionBlocksTheOrderWithSelectionInvalid(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	rig := env.withHubriseInventory(t)
	productID := env.Fixtures.SalmonSushi.ID
	groupID, soy, sweet := uuid.New(), uuid.New(), uuid.New()
	db := env.DB.DB
	_, err := db.ExecContext(t.Context(), `INSERT INTO product_choice_groups (id, product_id) VALUES ($1, $2)`, groupID, productID)
	require.NoError(t, err)
	for _, c := range []struct {
		id  uuid.UUID
		ref string
	}{{soy, "SAUCE-SOJA"}, {sweet, "SAUCE-SUCREE"}} {
		_, err = db.ExecContext(t.Context(), `INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier) VALUES ($1, $2, $3, 0.50)`, c.id, productID, groupID)
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `
			INSERT INTO hubrise_catalog_links (entity_type, choice_id, hubrise_key, hubrise_ref, origin) VALUES ('choice', $1, $2, $3, 'manual')`,
			c.id, "MAKI-SAUMON-6|SAUCE|"+c.ref, c.ref)
		require.NoError(t, err)
	}
	_, token := testhelpers.SeedCustomer(t, db, "options")
	order := func(choice uuid.UUID) map[string]any {
		return map[string]any{"orderType": "PICKUP", "isOnlinePayment": false, "items": []map[string]any{{
			"productId": productID.String(), "quantity": 1,
			"selections": []map[string]any{{"groupId": groupID.String(), "choiceId": choice.String(), "quantity": 1}},
		}}}
	}

	require.NoError(t, rig.srv.SetInventory([]pkghubrise.InventoryEntry{{OptionRef: "SAUCE-SOJA", Stock: new("0")}}))
	_, err = rig.inv.Service.Sync(t.Context(), false)
	require.NoError(t, err)

	got, orderErr := createOrderAs(t, env.TestContext, token, "fr", order(soy))
	requireOrderError(t, got, orderErr, apperr.CodeSelectionInvalid)
	quote := quoteAs(t, env.TestContext, token, "fr", order(soy))
	require.Len(t, quote.Lines, 1)
	require.NotEmpty(t, quote.Lines[0].Issues)
	assert.Equal(t, string(apperr.CodeSelectionInvalid), quote.Lines[0].Issues[0].Code)
	mustCreateOrder(t, env.TestContext, token, "fr", order(sweet)) // the other sauce is fine

	// HubRise has soy again.
	require.NoError(t, rig.srv.SetInventory(nil))
	_, err = rig.inv.Service.Sync(t.Context(), false)
	require.NoError(t, err)
	mustCreateOrder(t, env.TestContext, token, "fr", order(soy))
}
