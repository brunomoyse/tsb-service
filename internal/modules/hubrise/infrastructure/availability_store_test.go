package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

func (e *env) link(t *testing.T, repo domain.CatalogLinkRepository, entity domain.EntityType, id uuid.UUID, key string) *domain.CatalogLink {
	t.Helper()
	l := &domain.CatalogLink{EntityType: entity, HubriseKey: key, HubriseRef: key, Origin: domain.OriginImport}
	l.SetLocalID(id)
	require.NoError(t, repo.Upsert(t.Context(), l))
	return l
}

func (e *env) available(t *testing.T, table string, id uuid.UUID) bool {
	t.Helper()
	var v bool
	require.NoError(t, e.db.GetContext(t.Context(), &v, `SELECT is_available FROM `+table+` WHERE id = $1`, id))
	return v
}

func TestAvailabilityStore(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	links := infrastructure.NewCatalogLinkRepository(e.db)
	store := infrastructure.NewAvailabilityStore(e.db)
	_, choiceID := e.newChoice(t)
	productID := e.fx.SalmonSushi.ID
	until := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	zero := decimal.Zero
	state := domain.InventoryState{Stock: &zero, SoldOut: true, SoldOutUntil: &until, SyncedAt: time.Now().Truncate(time.Microsecond)}

	t.Run("reads the availability of products and choices", func(t *testing.T) {
		_, err := e.db.ExecContext(ctx, `UPDATE products SET is_available = false WHERE id = $1`, e.fx.TunaSushi.ID)
		require.NoError(t, err)
		got, err := store.Availability(ctx, domain.EntityProduct, []uuid.UUID{productID, e.fx.TunaSushi.ID, uuid.New()})
		require.NoError(t, err)
		assert.Equal(t, map[uuid.UUID]domain.Availability{
			productID:         {Available: true, ProductID: productID},
			e.fx.TunaSushi.ID: {Available: false, ProductID: e.fx.TunaSushi.ID},
		}, got, "an id that does not exist is absent")

		got, err = store.Availability(ctx, domain.EntityChoice, []uuid.UUID{choiceID})
		require.NoError(t, err)
		assert.Equal(t, domain.Availability{Available: true, ProductID: productID}, got[choiceID])

		empty, err := store.Availability(ctx, domain.EntityProduct, nil)
		require.NoError(t, err)
		assert.Empty(t, empty)

		_, err = store.Availability(ctx, domain.EntityCategory, []uuid.UUID{productID})
		assert.ErrorIs(t, err, domain.ErrInvalidLink)
	})

	t.Run("taking over a product flips the flag, the expiry and the availability together", func(t *testing.T) {
		l := e.link(t, links, domain.EntityProduct, productID, "SKU-1")
		ch, err := store.TakeOver(ctx, l, state)
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{Done: true, Changed: true, ProductID: productID}, ch)
		assert.False(t, e.available(t, "products", productID))
		got, err := links.FindByKey(ctx, domain.EntityProduct, "SKU-1")
		require.NoError(t, err)
		assert.True(t, got.InventorySoldOut)
		assert.WithinDuration(t, until, *got.SoldOutUntil, time.Millisecond)
		require.NotNil(t, got.Stock)
		assert.True(t, got.Stock.IsZero())
		assert.NotNil(t, got.InventorySyncedAt)

		again, err := store.TakeOver(ctx, got, state)
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{}, again, "the link already owns the state: nothing to do")
	})

	t.Run("releasing gives the availability back and clears the expiry", func(t *testing.T) {
		l, err := links.FindByKey(ctx, domain.EntityProduct, "SKU-1")
		require.NoError(t, err)
		ch, err := store.Release(ctx, l, domain.InventoryState{SyncedAt: time.Now()})
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{Done: true, Changed: true, ProductID: productID}, ch)
		assert.True(t, e.available(t, "products", productID))
		got, _ := links.FindByKey(ctx, domain.EntityProduct, "SKU-1")
		assert.False(t, got.InventorySoldOut)
		assert.Nil(t, got.SoldOutUntil)
		assert.Nil(t, got.Stock)

		again, err := store.Release(ctx, got, domain.InventoryState{SyncedAt: time.Now()})
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{}, again, "the shop owns the state: the release is refused")
	})

	t.Run("a row HubRise disabled stays unavailable when it is released", func(t *testing.T) {
		l, err := links.FindByKey(ctx, domain.EntityProduct, "SKU-1")
		require.NoError(t, err)
		require.NoError(t, links.SetCatalogDisabled(ctx, l.ID, true))
		ch, err := store.TakeOver(ctx, l, state)
		require.NoError(t, err)
		assert.True(t, ch.Done)
		ch, err = store.Release(ctx, l, domain.InventoryState{SyncedAt: time.Now()})
		require.NoError(t, err)
		assert.True(t, ch.Done)
		assert.False(t, ch.Changed, "it was unavailable before and after")
		assert.False(t, e.available(t, "products", productID))
		require.NoError(t, links.SetCatalogDisabled(ctx, l.ID, false))
		_, err = e.db.ExecContext(ctx, `UPDATE products SET is_available = true WHERE id = $1`, productID)
		require.NoError(t, err)
	})

	t.Run("a product staff sold out by hand is not taken over", func(t *testing.T) {
		l := e.link(t, links, domain.EntityProduct, e.fx.TunaSushi.ID, "SKU-2") // unavailable since the first subtest
		ch, err := store.TakeOver(ctx, l, state)
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{}, ch, "refused: the row stays theirs")
		got, err := links.FindByKey(ctx, domain.EntityProduct, "SKU-2")
		require.NoError(t, err)
		assert.False(t, got.InventorySoldOut, "the flip was rolled back")
		assert.Nil(t, got.Stock)
		assert.False(t, e.available(t, "products", e.fx.TunaSushi.ID))
	})

	t.Run("a product HubRise disabled is taken over without a change", func(t *testing.T) {
		l, err := links.FindByKey(ctx, domain.EntityProduct, "SKU-2")
		require.NoError(t, err)
		require.NoError(t, links.SetCatalogDisabled(ctx, l.ID, true))
		ch, err := store.TakeOver(ctx, l, state)
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{Done: true, Changed: false, ProductID: e.fx.TunaSushi.ID}, ch)
	})

	t.Run("a choice staff sold out by hand is not taken over", func(t *testing.T) {
		_, id := e.newChoice(t)
		_, err := e.db.ExecContext(ctx, `UPDATE product_choices SET is_available = false WHERE id = $1`, id)
		require.NoError(t, err)
		l := e.link(t, links, domain.EntityChoice, id, "SKU-1|L|HAND")
		ch, err := store.TakeOver(ctx, l, state)
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{}, ch)
	})

	t.Run("choices are switched and report their product", func(t *testing.T) {
		l := e.link(t, links, domain.EntityChoice, choiceID, "SKU-1|L|OPT")
		ch, err := store.TakeOver(ctx, l, state)
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{Done: true, Changed: true, ProductID: productID}, ch)
		assert.False(t, e.available(t, "product_choices", choiceID))
		ch, err = store.Release(ctx, l, domain.InventoryState{SyncedAt: time.Now()})
		require.NoError(t, err)
		assert.Equal(t, domain.InventoryChange{Done: true, Changed: true, ProductID: productID}, ch)
		assert.True(t, e.available(t, "product_choices", choiceID))
	})

	t.Run("an entity type without availability is refused", func(t *testing.T) {
		cat := &domain.CatalogLink{ID: 1, EntityType: domain.EntityCategory}
		_, err := store.TakeOver(ctx, cat, state)
		assert.ErrorIs(t, err, domain.ErrInvalidLink)
		_, err = store.Release(ctx, cat, state)
		assert.ErrorIs(t, err, domain.ErrInvalidLink)
	})
}

// Every failure of the switch is reported and leaves both the link and the local row as they were.
func TestAvailabilityStoreFailuresRollBack(t *testing.T) {
	raise := `CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;`
	for name, ddl := range map[string]string{
		"link":   raise + `CREATE TRIGGER t BEFORE UPDATE ON hubrise_catalog_links FOR EACH ROW EXECUTE FUNCTION f()`,
		"local":  raise + `CREATE TRIGGER t BEFORE UPDATE ON products FOR EACH ROW EXECUTE FUNCTION f()`,
		"commit": raise + `CREATE CONSTRAINT TRIGGER t AFTER UPDATE ON products DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION f()`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			ctx := t.Context()
			links := infrastructure.NewCatalogLinkRepository(e.db)
			store := infrastructure.NewAvailabilityStore(e.db)
			l := e.link(t, links, domain.EntityProduct, e.fx.SalmonSushi.ID, "SKU-F")
			_, err := e.db.ExecContext(ctx, ddl)
			require.NoError(t, err)

			_, err = store.TakeOver(ctx, l, domain.InventoryState{SyncedAt: time.Now()})
			require.Error(t, err)
			got, _ := links.FindByKey(ctx, domain.EntityProduct, "SKU-F")
			assert.False(t, got.InventorySoldOut, "rolled back")
			assert.True(t, e.available(t, "products", e.fx.SalmonSushi.ID), "rolled back")
		})
	}

	t.Run("a link naming a choice that does not exist", func(t *testing.T) {
		e := newEnv(t)
		links := infrastructure.NewCatalogLinkRepository(e.db)
		store := infrastructure.NewAvailabilityStore(e.db)
		_, choiceID := e.newChoice(t)
		l := e.link(t, links, domain.EntityChoice, choiceID, "SKU|L|O")
		ghost := *l
		ghost.ChoiceID = ptr(uuid.New())
		_, err := store.TakeOver(t.Context(), &ghost, domain.InventoryState{SyncedAt: time.Now()})
		assert.Error(t, err)
		got, _ := links.FindByKey(t.Context(), domain.EntityChoice, "SKU|L|O")
		assert.False(t, got.InventorySoldOut, "rolled back")
	})

	t.Run("the store is down", func(t *testing.T) {
		store := infrastructure.NewAvailabilityStore(testhelpers.ClosedDB(t))
		link := &domain.CatalogLink{ID: 1, EntityType: domain.EntityProduct}
		link.SetLocalID(uuid.New())
		_, err := store.Availability(t.Context(), domain.EntityProduct, []uuid.UUID{uuid.New()})
		assert.Error(t, err)
		_, err = store.TakeOver(t.Context(), link, domain.InventoryState{})
		assert.Error(t, err)
		_, err = store.Release(t.Context(), link, domain.InventoryState{})
		assert.Error(t, err)
	})
}
