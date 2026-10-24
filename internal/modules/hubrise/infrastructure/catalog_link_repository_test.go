package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

func TestCatalogLinkRepository(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewCatalogLinkRepository(e.db)
	ctx := t.Context()
	groupID, choiceID := e.newChoice(t)

	productLink := func(key string, id uuid.UUID) *domain.CatalogLink {
		l := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: key, HubriseRef: key, ContentHash: "h1", Origin: domain.OriginImport}
		l.SetLocalID(id)
		return l
	}

	t.Run("upsert creates then updates by key, keeping inventory state", func(t *testing.T) {
		l := productLink("SKU-A", e.fx.SalmonSushi.ID)
		hid := "hid-1"
		l.HubriseID = &hid
		require.NoError(t, repo.Upsert(ctx, l))
		assert.NotZero(t, l.ID)
		assert.Equal(t, "h1", l.ContentHash)
		assert.False(t, l.CreatedAt.IsZero())
		firstID := l.ID

		// Inventory state is written by its own method...
		until := time.Now().Add(time.Hour).Truncate(time.Microsecond)
		stock := decimal.RequireFromString("0")
		require.NoError(t, repo.UpdateInventory(ctx, l.ID, domain.InventoryState{Stock: &stock, SoldOut: true, SoldOutUntil: &until, SyncedAt: time.Now()}))

		// ...and an upsert of the same key (a re-import) must not erase it.
		again := productLink("SKU-A", e.fx.SalmonSushi.ID)
		again.ContentHash = "h2"
		again.Origin = domain.OriginAdopted
		md5 := "abc"
		again.ImageMD5 = &md5
		require.NoError(t, repo.Upsert(ctx, again))
		assert.Equal(t, firstID, again.ID)
		assert.Equal(t, "h2", again.ContentHash)
		assert.Equal(t, domain.OriginAdopted, again.Origin)
		assert.Equal(t, "abc", *again.ImageMD5)
		assert.True(t, again.InventorySoldOut)
		assert.WithinDuration(t, until, *again.SoldOutUntil, time.Millisecond)
		require.NotNil(t, again.Stock)
		assert.True(t, again.Stock.IsZero())
		assert.Nil(t, again.HubriseID, "hubrise_id follows the upsert")
	})

	t.Run("lookups", func(t *testing.T) {
		byKey, err := repo.FindByKey(ctx, domain.EntityProduct, "SKU-A")
		require.NoError(t, err)
		assert.Equal(t, e.fx.SalmonSushi.ID, byKey.LocalID())

		byLocal, err := repo.FindByLocal(ctx, domain.EntityProduct, e.fx.SalmonSushi.ID)
		require.NoError(t, err)
		assert.Equal(t, byKey.ID, byLocal.ID)

		_, err = repo.FindByKey(ctx, domain.EntityProduct, "nope")
		assert.ErrorIs(t, err, domain.ErrNotFound)
		_, err = repo.FindByKey(ctx, domain.EntityCategory, "SKU-A")
		assert.ErrorIs(t, err, domain.ErrNotFound, "keys are per entity type")
		_, err = repo.FindByLocal(ctx, domain.EntityProduct, uuid.New())
		assert.ErrorIs(t, err, domain.ErrNotFound)
		_, err = repo.FindByLocal(ctx, "deal", uuid.New())
		assert.ErrorIs(t, err, domain.ErrInvalidLink)
	})

	t.Run("every entity type can be linked and is cleaned up with its local row", func(t *testing.T) {
		cat := &domain.CatalogLink{EntityType: domain.EntityCategory, HubriseKey: "cat-1", HubriseRef: "cat-1", Origin: domain.OriginImport}
		cat.SetLocalID(e.fx.SushiCategory.ID)
		grp := &domain.CatalogLink{EntityType: domain.EntityChoiceGroup, HubriseKey: "SKU-A|L1", HubriseRef: "L1", Origin: domain.OriginImport}
		grp.SetLocalID(groupID)
		ch := &domain.CatalogLink{EntityType: domain.EntityChoice, HubriseKey: "SKU-A|L1|O1", HubriseRef: "O1", Origin: domain.OriginImport}
		ch.SetLocalID(choiceID)
		for _, l := range []*domain.CatalogLink{cat, grp, ch} {
			require.NoError(t, repo.Upsert(ctx, l))
			got, err := repo.FindByLocal(ctx, l.EntityType, l.LocalID())
			require.NoError(t, err)
			assert.Equal(t, l.ID, got.ID)
		}

		// Deleting the choice in the dashboard cleans its link (ON DELETE CASCADE).
		_, err := e.db.ExecContext(ctx, `DELETE FROM product_choices WHERE id = $1`, choiceID)
		require.NoError(t, err)
		_, err = repo.FindByKey(ctx, domain.EntityChoice, "SKU-A|L1|O1")
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})

	t.Run("one link per local row and per key", func(t *testing.T) {
		dup := productLink("SKU-OTHER", e.fx.SalmonSushi.ID)
		require.Error(t, repo.Upsert(ctx, dup), "the product is already linked under SKU-A")

		bad := &domain.CatalogLink{EntityType: domain.EntityProduct}
		assert.ErrorIs(t, repo.Upsert(ctx, bad), domain.ErrInvalidLink)
	})

	t.Run("lists, counts, retirement", func(t *testing.T) {
		tuna := productLink("SKU-B", e.fx.TunaSushi.ID)
		tuna.HubriseRef = "SKU-B"
		require.NoError(t, repo.Upsert(ctx, tuna))
		other := productLink("SKU-C|v", e.fx.GreenTea.ID)
		other.HubriseRef = "SKU-B" // two links sharing a ref (one option used by several SKUs)
		require.NoError(t, repo.Upsert(ctx, other))

		all, err := repo.ListByType(ctx, domain.EntityProduct, false)
		require.NoError(t, err)
		require.Len(t, all, 3)
		assert.Less(t, all[0].ID, all[1].ID)

		byRef, err := repo.ListByRef(ctx, domain.EntityProduct, "SKU-B")
		require.NoError(t, err)
		assert.Len(t, byRef, 2)

		n, err := repo.Count(ctx, false)
		require.NoError(t, err)
		assert.Equal(t, 5, n) // 3 products, 1 category, 1 choice group (the choice was cleaned up)

		now := time.Now()
		require.NoError(t, repo.SetRetired(ctx, tuna.ID, &now))
		live, _ := repo.ListByType(ctx, domain.EntityProduct, false)
		assert.Len(t, live, 2)
		withRetired, _ := repo.ListByType(ctx, domain.EntityProduct, true)
		assert.Len(t, withRetired, 3)
		n, _ = repo.Count(ctx, false)
		assert.Equal(t, 4, n)
		n, _ = repo.Count(ctx, true)
		assert.Equal(t, 5, n)

		require.NoError(t, repo.SetRetired(ctx, tuna.ID, nil))
		live, _ = repo.ListByType(ctx, domain.EntityProduct, false)
		assert.Len(t, live, 3)
	})

	t.Run("flags and inventory", func(t *testing.T) {
		l, err := repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		require.NoError(t, err)
		assert.False(t, l.CatalogDisabled)
		require.NoError(t, repo.SetCatalogDisabled(ctx, l.ID, true))
		l, _ = repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		assert.True(t, l.CatalogDisabled)

		stock := decimal.RequireFromString("12.5")
		require.NoError(t, repo.UpdateInventory(ctx, l.ID, domain.InventoryState{Stock: &stock, SyncedAt: time.Now()}))
		l, _ = repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		assert.Equal(t, "12.5", l.Stock.String())
		assert.False(t, l.InventorySoldOut)
		assert.NotNil(t, l.InventorySyncedAt)

		until := time.Now().Add(time.Hour)
		require.NoError(t, repo.UpdateInventory(ctx, l.ID, domain.InventoryState{SoldOut: true, SoldOutUntil: &until, SyncedAt: time.Now()}))
		l, _ = repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		assert.Nil(t, l.Stock, "no stock figure: the entry is unlimited or unknown")
		assert.True(t, l.InventorySoldOut)

		require.NoError(t, repo.ClearSoldOut(ctx, l.ID))
		l, _ = repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		assert.False(t, l.InventorySoldOut)
		assert.Nil(t, l.SoldOutUntil)
	})

	t.Run("a missing link is reported", func(t *testing.T) {
		now := time.Now()
		assert.ErrorIs(t, repo.SetRetired(ctx, 999999, &now), domain.ErrNotFound)
		assert.ErrorIs(t, repo.SetCatalogDisabled(ctx, 999999, true), domain.ErrNotFound)
		assert.ErrorIs(t, repo.UpdateInventory(ctx, 999999, domain.InventoryState{SyncedAt: now}), domain.ErrNotFound)
		assert.ErrorIs(t, repo.ClearSoldOut(ctx, 999999), domain.ErrNotFound)
		assert.ErrorIs(t, repo.Delete(ctx, 999999), domain.ErrNotFound)
	})

	t.Run("delete one, delete all (local rows stay)", func(t *testing.T) {
		l, _ := repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		require.NoError(t, repo.Delete(ctx, l.ID))
		_, err := repo.FindByKey(ctx, domain.EntityProduct, "SKU-B")
		assert.ErrorIs(t, err, domain.ErrNotFound)

		n, err := repo.DeleteAll(ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 4, n)
		count, _ := repo.Count(ctx, true)
		assert.Zero(t, count)

		var products int
		require.NoError(t, e.db.GetContext(ctx, &products, `SELECT count(*) FROM products WHERE id = $1`, e.fx.SalmonSushi.ID))
		assert.Equal(t, 1, products, "forgetting links never touches products")
	})
}

func TestCatalogLinkCheckConstraint(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	// The table itself refuses a link that names the wrong column, no target or two targets.
	for name, args := range map[string][]any{
		"product type with a category id": {`INSERT INTO hubrise_catalog_links (entity_type, category_id, hubrise_key, hubrise_ref) VALUES ('product', $1, 'k', 'k')`, e.fx.SushiCategory.ID},
		"no target":                       {`INSERT INTO hubrise_catalog_links (entity_type, hubrise_key, hubrise_ref) VALUES ('product', 'k', 'k')`},
		"two targets":                     {`INSERT INTO hubrise_catalog_links (entity_type, product_id, category_id, hubrise_key, hubrise_ref) VALUES ('product', $1, $2, 'k', 'k')`, e.fx.SalmonSushi.ID, e.fx.SushiCategory.ID},
		"unknown origin":                  {`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref, origin) VALUES ('product', $1, 'k', 'k', 'guess')`, e.fx.SalmonSushi.ID},
		"unknown entity type":             {`INSERT INTO hubrise_catalog_links (entity_type, product_id, hubrise_key, hubrise_ref) VALUES ('deal', $1, 'k', 'k')`, e.fx.SalmonSushi.ID},
	} {
		_, err := e.db.ExecContext(ctx, args[0].(string), args[1:]...)
		assert.Error(t, err, name)
	}
}

func TestCatalogLinkRecordStockAndExpiry(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewCatalogLinkRepository(e.db)
	store := infrastructure.NewAvailabilityStore(e.db)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	_, choiceID := e.newChoice(t)

	sold := e.link(t, repo, domain.EntityProduct, e.fx.SalmonSushi.ID, "SKU-A")
	soldChoice := e.link(t, repo, domain.EntityChoice, choiceID, "SKU-A|L|O")
	later := e.link(t, repo, domain.EntityProduct, e.fx.TunaSushi.ID, "SKU-B")
	free := e.link(t, repo, domain.EntityProduct, e.fx.GreenTea.ID, "SKU-C")
	retired := e.link(t, repo, domain.EntityProduct, e.fx.MochiIce.ID, "SKU-D")
	zero := decimal.Zero
	for link, until := range map[*domain.CatalogLink]time.Time{
		sold: now.Add(-time.Minute), soldChoice: now.Add(-time.Second), later: now.Add(time.Hour), retired: now.Add(-time.Hour),
	} {
		u := until
		_, err := store.TakeOver(ctx, link, domain.InventoryState{Stock: &zero, SoldOut: true, SoldOutUntil: &u, SyncedAt: now})
		require.NoError(t, err)
	}
	require.NoError(t, repo.SetRetired(ctx, retired.ID, &now))

	expired, err := repo.ListSoldOutExpired(ctx, now)
	require.NoError(t, err)
	var keys []string
	for _, l := range expired {
		keys = append(keys, l.HubriseKey)
	}
	assert.Equal(t, []string{"SKU-A", "SKU-A|L|O"}, keys, "sold out by inventory, expired, not retired, in id order")

	t.Run("RecordStock stores the figures and leaves the provenance flag alone", func(t *testing.T) {
		five := decimal.RequireFromString("5")
		require.NoError(t, repo.RecordStock(ctx, free.ID, &five, nil, now))
		got, err := repo.FindByKey(ctx, domain.EntityProduct, "SKU-C")
		require.NoError(t, err)
		require.NotNil(t, got.Stock)
		assert.True(t, got.Stock.Equal(five))
		assert.False(t, got.InventorySoldOut)
		assert.WithinDuration(t, now, *got.InventorySyncedAt, time.Millisecond)

		got, _ = repo.FindByKey(ctx, domain.EntityProduct, "SKU-A")
		require.NoError(t, repo.RecordStock(ctx, got.ID, nil, nil, now))
		got, _ = repo.FindByKey(ctx, domain.EntityProduct, "SKU-A")
		assert.True(t, got.InventorySoldOut, "still owned: only the figures changed")
		assert.Nil(t, got.Stock)
		assert.Nil(t, got.SoldOutUntil)

		assert.ErrorIs(t, repo.RecordStock(ctx, 0, nil, nil, now), domain.ErrNotFound)
	})
}
