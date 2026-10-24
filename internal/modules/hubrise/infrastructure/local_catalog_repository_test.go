package infrastructure_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestLocalCatalogRepositorySnapshotReadsTheWholeMenu(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	groupID, choiceID := e.newChoice(t)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO product_choice_group_translations (product_choice_group_id, locale, name, source) VALUES ($1, 'fr', 'Sauce', 'hubrise')`, []any{groupID}},
		{`INSERT INTO product_choice_translations (product_choice_id, locale, name) VALUES ($1, 'fr', 'Soja')`, []any{choiceID}},
		{`UPDATE product_choices SET is_available = FALSE WHERE id = $1`, []any{choiceID}},
	} {
		_, err := e.db.ExecContext(t.Context(), q.sql, q.args...)
		require.NoError(t, err)
	}

	snap, err := repo.Snapshot(t.Context())

	require.NoError(t, err)
	assert.NotEmpty(t, snap.Categories)
	assert.NotEmpty(t, snap.Products)
	var found bool
	for _, p := range snap.Products {
		if p.ID == e.fx.SalmonSushi.ID {
			found = true
			assert.NotEmpty(t, p.Texts["fr"].Name)
			assert.Equal(t, domain.SourceManual, p.Texts["fr"].Source)
			assert.NotEmpty(t, p.VatCategory)
		}
	}
	assert.True(t, found)
	require.Len(t, snap.Groups, 1)
	assert.Equal(t, domain.LocalText{Name: "Sauce", Source: domain.SourceHubrise}, snap.Groups[0].Names["fr"])
	require.Len(t, snap.Choices, 1)
	assert.True(t, snap.Choices[0].Unavailable)
	assert.Equal(t, "Soja", snap.Choices[0].Names["fr"].Name)
	assert.True(t, snap.Choices[0].PriceModifier.Equal(dec("0.50")))
}

func TestLocalCatalogRepositoryBatchWritesAndRollsBack(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()
	var catID, prodID, groupID, choiceID uuid.UUID

	err := repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		var err error
		if catID, err = tx.CreateCategory(ctx, domain.NewCategory{Order: 50, SlugBase: "Entrées Chaudes"}); err != nil {
			return err
		}
		if err = tx.UpsertTexts(ctx, domain.TextCategory, catID, []domain.TextWrite{{Language: "fr", Name: "Entrées chaudes"}, {Language: "en", Name: ""}}); err != nil {
			return err
		}
		if prodID, err = tx.CreateProduct(ctx, domain.NewProduct{CategoryID: catID, Price: dec("4.5"), Code: "E1", VatCategory: domain.VatFood,
			Visible: true, Available: true, Halal: true, SlugName: "Gyoza porc"}); err != nil {
			return err
		}
		if groupID, err = tx.CreateGroup(ctx, domain.NewGroup{ProductID: prodID, Min: 1, Max: 2, Sort: 1}); err != nil {
			return err
		}
		if choiceID, err = tx.CreateChoice(ctx, domain.NewChoice{ProductID: prodID, GroupID: groupID, PriceModifier: dec("0.5"), Sort: 2, Unavailable: true}); err != nil {
			return err
		}
		return tx.UpsertTexts(ctx, domain.TextProduct, prodID, []domain.TextWrite{{Language: "fr", Name: "Gyoza", Description: "Au porc"}})
	})
	require.NoError(t, err)

	var slug string
	require.NoError(t, e.db.GetContext(ctx, &slug, `SELECT slug FROM product_categories WHERE id = $1`, catID))
	assert.Equal(t, "entrees-chaudes", slug)
	require.NoError(t, e.db.GetContext(ctx, &slug, `SELECT slug FROM products WHERE id = $1`, prodID))
	assert.Equal(t, "entrees-chaudes-gyoza-porc", slug, "category name plus product name")
	var desc *string
	require.NoError(t, e.db.GetContext(ctx, &desc, `SELECT description FROM product_translations WHERE product_id = $1 AND language = 'fr'`, prodID))
	assert.Equal(t, "Au porc", *desc)
	var ok bool
	require.NoError(t, e.db.GetContext(ctx, &ok, `SELECT NOT is_available FROM product_choices WHERE id = $1`, choiceID))
	assert.True(t, ok)
	var langs int
	require.NoError(t, e.db.GetContext(ctx, &langs, `SELECT count(*) FROM product_category_translations WHERE product_category_id = $1`, catID))
	assert.Equal(t, 1, langs, "an empty name writes nothing")

	// Slugs are unique: the same names again get a suffix.
	err = repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		c2, err := tx.CreateCategory(ctx, domain.NewCategory{Order: 51, SlugBase: "Entrées Chaudes"})
		require.NoError(t, err)
		require.NoError(t, tx.UpsertTexts(ctx, domain.TextCategory, c2, []domain.TextWrite{{Language: "fr", Name: "Entrées chaudes"}}))
		p2, err := tx.CreateProduct(ctx, domain.NewProduct{CategoryID: c2, Price: dec("1"), VatCategory: domain.VatFood, SlugName: "Gyoza porc"})
		require.NoError(t, err)
		require.NoError(t, e.db.GetContext(ctx, &slug, `SELECT 1::text`)) // the connection pool is usable inside the batch
		_ = p2
		return nil
	})
	require.NoError(t, err)
	var slugs []string
	require.NoError(t, e.db.SelectContext(ctx, &slugs, `SELECT slug FROM product_categories WHERE slug LIKE 'entrees-chaudes%' ORDER BY slug`))
	assert.Equal(t, []string{"entrees-chaudes", "entrees-chaudes-2"}, slugs)
	require.NoError(t, e.db.SelectContext(ctx, &slugs, `SELECT slug FROM products WHERE slug LIKE 'entrees-chaudes%' ORDER BY slug`))
	assert.Equal(t, []string{"entrees-chaudes-gyoza-porc", "entrees-chaudes-gyoza-porc-2"}, slugs)

	// A failing batch leaves nothing behind.
	boom := errors.New("boom")
	var rolled uuid.UUID
	err = repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		var err error
		rolled, err = tx.CreateCategory(ctx, domain.NewCategory{Order: 99, SlugBase: "Perdue"})
		require.NoError(t, err)
		return boom
	})
	assert.ErrorIs(t, err, boom)
	var n int
	require.NoError(t, e.db.GetContext(ctx, &n, `SELECT count(*) FROM product_categories WHERE id = $1`, rolled))
	assert.Zero(t, n)
}

func TestLocalCatalogRepositoryPatchesAndNeverBlanks(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()
	groupID, choiceID := e.newChoice(t)
	p := e.fx.SalmonSushi.ID
	var code *string
	require.NoError(t, e.db.GetContext(ctx, &code, `UPDATE products SET code = 'KEEP' WHERE id = $1 RETURNING code`, p))

	newPrice, vat, no, yes, one := dec("7.25"), domain.VatBeverage, false, true, 1
	err := repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		require.NoError(t, tx.PatchProduct(ctx, p, domain.ProductPatch{Price: &newPrice, VatCategory: &vat, Visible: &no, Available: &no}))
		require.NoError(t, tx.PatchGroup(ctx, groupID, domain.GroupPatch{Min: &one, Max: &one, Sort: &one}))
		return tx.PatchChoice(ctx, choiceID, domain.ChoicePatch{PriceModifier: &newPrice, Sort: &one, Unavailable: &yes})
	})
	require.NoError(t, err)

	var got struct {
		Price   string  `db:"price"`
		Vat     string  `db:"vat_category"`
		Code    *string `db:"code"`
		Visible bool    `db:"is_visible"`
	}
	require.NoError(t, e.db.GetContext(ctx, &got, `SELECT price::text, vat_category, code, is_visible FROM products WHERE id = $1`, p))
	assert.Equal(t, "7.25", got.Price)
	assert.Equal(t, "beverage", got.Vat)
	assert.Equal(t, "KEEP", *got.Code, "an empty patch field leaves the column alone")
	assert.False(t, got.Visible)
	var avail bool
	var sortOrder int
	require.NoError(t, e.db.GetContext(ctx, &avail, `SELECT is_available FROM product_choices WHERE id = $1`, choiceID))
	assert.False(t, avail)
	require.NoError(t, e.db.GetContext(ctx, &sortOrder, `SELECT sort_order FROM product_choice_groups WHERE id = $1`, groupID))
	assert.Equal(t, 1, sortOrder)

	// A patch of a row that does not exist fails the batch.
	missing := uuid.New()
	for name, patch := range map[string]func(ctx context.Context, tx domain.CatalogTx) error{
		"product": func(ctx context.Context, tx domain.CatalogTx) error {
			return tx.PatchProduct(ctx, missing, domain.ProductPatch{Visible: &no})
		},
		"group": func(ctx context.Context, tx domain.CatalogTx) error {
			return tx.PatchGroup(ctx, missing, domain.GroupPatch{Min: &one})
		},
		"choice": func(ctx context.Context, tx domain.CatalogTx) error {
			return tx.PatchChoice(ctx, missing, domain.ChoicePatch{Sort: &one})
		},
	} {
		assert.ErrorIs(t, repo.InBatch(ctx, patch), domain.ErrNotFound, name)
	}
}

func TestLocalCatalogRepositoryTextsRespectTheirSource(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()
	groupID, choiceID := e.newChoice(t)
	p := e.fx.SalmonSushi.ID
	cat := e.fx.SalmonSushi.CategoryID

	write := func(target domain.TextTarget, id uuid.UUID, name string) {
		require.NoError(t, repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
			return tx.UpsertTexts(ctx, target, id, []domain.TextWrite{{Language: "fr", Name: name}, {Language: "nl", Name: name + " nl"}})
		}))
	}
	read := func(query string, id uuid.UUID) (name, source string) {
		var row struct {
			Name   string `db:"name"`
			Source string `db:"source"`
		}
		require.NoError(t, e.db.GetContext(ctx, &row, query, id))
		return row.Name, row.Source
	}

	// The fixture's French product name is a manual row: untouched. Dutch is new: written as hubrise.
	before, _ := read(`SELECT name, source FROM product_translations WHERE product_id = $1 AND language = 'fr'`, p)
	write(domain.TextProduct, p, "HubRise name")
	name, source := read(`SELECT name, source FROM product_translations WHERE product_id = $1 AND language = 'fr'`, p)
	assert.Equal(t, before, name)
	assert.Equal(t, "manual", source)
	name, source = read(`SELECT name, source FROM product_translations WHERE product_id = $1 AND language = 'nl'`, p)
	assert.Equal(t, "HubRise name nl", name)
	assert.Equal(t, "hubrise", source)
	write(domain.TextProduct, p, "Second name")
	name, _ = read(`SELECT name, source FROM product_translations WHERE product_id = $1 AND language = 'nl'`, p)
	assert.Equal(t, "Second name nl", name, "a hubrise row is refreshed")

	write(domain.TextCategory, cat, "Cat")
	name, source = read(`SELECT name, source FROM product_category_translations WHERE product_category_id = $1 AND language = 'nl'`, cat)
	assert.Equal(t, "Cat nl", name)
	assert.Equal(t, "hubrise", source)
	write(domain.TextGroup, groupID, "Grp")
	write(domain.TextGroup, groupID, "Grp2")
	name, source = read(`SELECT name, source FROM product_choice_group_translations WHERE product_choice_group_id = $1 AND locale = 'fr'`, groupID)
	assert.Equal(t, "Grp2", name)
	assert.Equal(t, "hubrise", source)
	write(domain.TextChoice, choiceID, "Ch")
	name, _ = read(`SELECT name, source FROM product_choice_translations WHERE product_choice_id = $1 AND locale = 'nl'`, choiceID)
	assert.Equal(t, "Ch nl", name)

	err := repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		return tx.UpsertTexts(ctx, "bogus", p, []domain.TextWrite{{Language: "fr", Name: "x"}})
	})
	assert.ErrorContains(t, err, "unknown translation target")
}

func TestLocalCatalogRepositoryBatchesAreSerialized(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()
	inside, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = repo.InBatch(ctx, func(context.Context, domain.CatalogTx) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside

	second := make(chan struct{})
	go func() {
		defer close(second)
		_ = repo.InBatch(ctx, func(context.Context, domain.CatalogTx) error { return nil })
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-second:
		t.Fatal("a second batch ran while the first held the import lock")
	default:
	}
	close(release)
	<-done
	<-second
}

func TestCatalogLinkRepositoryInATransaction(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	tx, err := e.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	links := infrastructure.NewCatalogLinkRepositoryTx(tx)
	l := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: "S", HubriseRef: "S", Origin: domain.OriginImport}
	l.SetLocalID(e.fx.SalmonSushi.ID)
	require.NoError(t, links.Upsert(ctx, l))
	require.NoError(t, links.SetImageMD5(ctx, l.ID, "abc"))
	got, err := links.FindByKey(ctx, domain.EntityProduct, "S")
	require.NoError(t, err)
	assert.Equal(t, "abc", *got.ImageMD5)
	assert.ErrorIs(t, links.SetImageMD5(ctx, 999999, "x"), domain.ErrNotFound)
	require.NoError(t, tx.Rollback())

	_, err = infrastructure.NewCatalogLinkRepository(e.db).FindByKey(ctx, domain.EntityProduct, "S")
	assert.ErrorIs(t, err, domain.ErrNotFound, "the rolled back link is gone")
}

func TestLocalCatalogRepositoryReportsStorageErrors(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()

	// Every table the snapshot reads, missing in turn.
	for _, table := range []string{"product_categories", "product_category_translations", "products", "product_translations",
		"product_choice_groups", "product_choice_group_translations", "product_choices", "product_choice_translations"} {
		_, err := e.db.ExecContext(ctx, `ALTER TABLE `+table+` RENAME TO `+table+`_away`)
		require.NoError(t, err, table)
		_, snapErr := repo.Snapshot(ctx)
		assert.Error(t, snapErr, table)
		_, err = e.db.ExecContext(ctx, `ALTER TABLE `+table+`_away RENAME TO `+table)
		require.NoError(t, err, table)
	}

	// A cancelled context stops a batch before it starts.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.Error(t, repo.InBatch(cancelled, func(context.Context, domain.CatalogTx) error { return nil }))

	// Writes against a missing table fail the batch.
	_, err := e.db.ExecContext(ctx, `ALTER TABLE product_categories RENAME TO product_categories_away`)
	require.NoError(t, err)
	err = repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		_, cerr := tx.CreateCategory(ctx, domain.NewCategory{Order: 1, SlugBase: "x"})
		return cerr
	})
	assert.Error(t, err)
}

func TestLocalCatalogRepositoryEdgeCases(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()

	var catID, prodID uuid.UUID
	err := repo.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		// A slug base without letters still gives a slug.
		cat, err := tx.CreateCategory(ctx, domain.NewCategory{Order: 70, SlugBase: "!!!"})
		require.NoError(t, err)
		// A category without any name: the product slug is its own name.
		prod, err := tx.CreateProduct(ctx, domain.NewProduct{CategoryID: cat, Price: dec("1"), VatCategory: domain.VatFood, SlugName: "Riz"})
		require.NoError(t, err)
		catID, prodID = cat, prod
		// The link repository of the batch is bound to the same transaction.
		l := &domain.CatalogLink{EntityType: domain.EntityCategory, HubriseKey: "C", HubriseRef: "C", Origin: domain.OriginImport}
		l.SetLocalID(cat)
		return tx.Links().Upsert(ctx, l)
	})
	require.NoError(t, err)
	var slug string
	require.NoError(t, e.db.GetContext(ctx, &slug, `SELECT slug FROM product_categories WHERE id = $1`, catID))
	assert.Equal(t, "item", slug)
	require.NoError(t, e.db.GetContext(ctx, &slug, `SELECT slug FROM products WHERE id = $1`, prodID))
	assert.Equal(t, "riz", slug)
}

func TestLocalCatalogRepositoryWriteErrorsFailTheBatch(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()
	groupID, _ := e.newChoice(t)
	p := e.fx.SalmonSushi.ID
	yes := true

	cases := []struct {
		table string
		run   func(ctx context.Context, tx domain.CatalogTx) error
	}{
		{"products", func(ctx context.Context, tx domain.CatalogTx) error {
			_, err := tx.CreateProduct(ctx, domain.NewProduct{CategoryID: e.fx.SalmonSushi.CategoryID, VatCategory: domain.VatFood, SlugName: "x"})
			return err
		}},
		{"product_category_translations", func(ctx context.Context, tx domain.CatalogTx) error {
			_, err := tx.CreateProduct(ctx, domain.NewProduct{CategoryID: e.fx.SalmonSushi.CategoryID, VatCategory: domain.VatFood, SlugName: "x"})
			return err
		}},
		{"product_choice_groups", func(ctx context.Context, tx domain.CatalogTx) error {
			_, err := tx.CreateGroup(ctx, domain.NewGroup{ProductID: p})
			return err
		}},
		{"product_choices", func(ctx context.Context, tx domain.CatalogTx) error {
			_, err := tx.CreateChoice(ctx, domain.NewChoice{ProductID: p, GroupID: groupID})
			return err
		}},
		{"product_translations", func(ctx context.Context, tx domain.CatalogTx) error {
			return tx.UpsertTexts(ctx, domain.TextProduct, p, []domain.TextWrite{{Language: "nl", Name: "x"}})
		}},
		{"products", func(ctx context.Context, tx domain.CatalogTx) error {
			return tx.PatchProduct(ctx, p, domain.ProductPatch{Visible: &yes})
		}},
	}
	for _, c := range cases {
		_, err := e.db.ExecContext(ctx, `ALTER TABLE `+c.table+` RENAME TO `+c.table+`_away`)
		require.NoError(t, err, c.table)
		assert.Error(t, repo.InBatch(ctx, c.run), c.table)
		_, err = e.db.ExecContext(ctx, `ALTER TABLE `+c.table+`_away RENAME TO `+c.table)
		require.NoError(t, err, c.table)
	}
}

func TestSnapshotWithLinksReportsAMissingTable(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLocalCatalogRepository(e.db)
	ctx := t.Context()
	for _, table := range []string{"products", "hubrise_catalog_links"} {
		_, err := e.db.ExecContext(ctx, `ALTER TABLE `+table+` RENAME TO `+table+`_away`)
		require.NoError(t, err, table)
		_, _, snapErr := repo.SnapshotWithLinks(ctx)
		assert.Error(t, snapErr, table)
		_, err = e.db.ExecContext(ctx, `ALTER TABLE `+table+`_away RENAME TO `+table)
		require.NoError(t, err, table)
	}
	snap, links, err := repo.SnapshotWithLinks(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, snap.Products)
	assert.Empty(t, links)
}
