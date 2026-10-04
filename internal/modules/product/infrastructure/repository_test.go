package infrastructure

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/product/domain"
	"tsb-service/pkg/db"
	"tsb-service/pkg/utils"
)

var dec = decimal.RequireFromString

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }

type env struct {
	tdb  *testhelpers.TestDatabase
	repo domain.ProductRepository
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tdb := testhelpers.SetupTestDatabase(t)
	return &env{tdb: tdb, repo: NewProductRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})}
}

func tr(lang, name string) domain.Translation {
	d := "desc " + name
	return domain.Translation{Language: lang, Name: name, Description: &d}
}

func (e *env) newProduct(t *testing.T, category uuid.UUID, mutate func(*domain.Product)) *domain.Product {
	t.Helper()
	p := &domain.Product{
		ID: uuid.New(), Price: dec("12.50"), Code: sp("S" + uuid.NewString()[:4]), CategoryID: category,
		IsVisible: true, IsAvailable: true, IsDiscountable: true, VatCategory: domain.VatCategoryFood,
		Translations: []domain.Translation{tr("fr", "Saumon "+uuid.NewString()[:6]), tr("en", "Salmon"), tr("nl", "Zalm")},
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, e.repo.Create(t.Context(), p))
	return p
}

func ids(ps []*domain.Product) []uuid.UUID {
	out := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func TestProductRepositoryProducts(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	sushi := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi", "en": "Sushi EN"})
	drinks := testhelpers.SeedCategory(t, e.tdb.DB, 2, map[string]string{"fr": "Boissons"})
	bare := testhelpers.SeedCategory(t, e.tdb.DB, 3, nil)

	t.Run("Create stores the product with every flag and translation, and derives the slug from the French names", func(t *testing.T) {
		p := e.newProduct(t, sushi, func(p *domain.Product) {
			p.Code = sp("S10")
			p.PieceCount = ip(8)
			p.IsHalal, p.IsVegetarian, p.IsSpicy, p.IsLunchOnly = true, true, true, true
			p.IsDiscountable = false
			p.VatCategory = domain.VatCategoryBeverage
			p.Translations = []domain.Translation{tr("fr", "Saumon"), tr("en", "Salmon"), tr("nl", "Zalm")}
		})
		require.NotNil(t, p.Slug)
		assert.Equal(t, "sushi-saumon", *p.Slug)

		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		assert.True(t, got.Price.Equal(dec("12.50")))
		assert.Equal(t, "S10", *got.Code)
		assert.Equal(t, "sushi-saumon", *got.Slug)
		assert.Equal(t, 8, *got.PieceCount)
		assert.True(t, got.IsVisible && got.IsAvailable && got.IsHalal && got.IsVegetarian && got.IsSpicy && got.IsLunchOnly)
		assert.False(t, got.IsDiscountable)
		assert.Equal(t, domain.VatCategoryBeverage, got.VatCategory)
		assert.Equal(t, sushi, got.CategoryID)
		assert.False(t, got.CreatedAt.IsZero())
		assert.ElementsMatch(t, p.Translations, got.Translations)
	})

	t.Run("Create without a French name keeps the given slug", func(t *testing.T) {
		p := e.newProduct(t, sushi, func(p *domain.Product) {
			p.Slug = sp("custom-slug")
			p.Translations = []domain.Translation{tr("en", "Tuna"), tr("nl", "Tonijn"), tr("zh", "金枪鱼")}
		})
		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, "custom-slug", *got.Slug)
	})

	t.Run("Create fails, and stores nothing, when the category has no French name", func(t *testing.T) {
		p := &domain.Product{ID: uuid.New(), Price: dec("1"), CategoryID: bare, VatCategory: domain.VatCategoryFood, Translations: []domain.Translation{tr("fr", "X")}}
		require.ErrorContains(t, e.repo.Create(ctx, p), "failed to fetch category translation name")
		_, err := e.repo.FindByID(ctx, p.ID)
		require.Error(t, err)
	})

	t.Run("Create rolls back when a translation cannot be inserted", func(t *testing.T) {
		p := &domain.Product{ID: uuid.New(), Price: dec("1"), Code: sp("RB1"), CategoryID: sushi, VatCategory: domain.VatCategoryFood,
			Translations: []domain.Translation{tr("fr", "Rollback"), tr("fr", "Duplicate language")}}
		require.ErrorContains(t, e.repo.Create(ctx, p), "failed to insert product translation")
		_, err := e.repo.FindByID(ctx, p.ID)
		assert.ErrorContains(t, err, "product not found", "the product row was rolled back with its translations")
	})

	t.Run("Create rejects an unknown category or VAT category", func(t *testing.T) {
		p := &domain.Product{ID: uuid.New(), Price: dec("1"), CategoryID: uuid.New(), VatCategory: domain.VatCategoryFood, Translations: []domain.Translation{tr("en", "x")}}
		require.ErrorContains(t, e.repo.Create(ctx, p), "failed to insert product")
		p = &domain.Product{ID: uuid.New(), Price: dec("1"), CategoryID: sushi, VatCategory: "bogus", Translations: []domain.Translation{tr("en", "x")}}
		require.ErrorContains(t, e.repo.Create(ctx, p), "failed to insert product")
	})

	// BUG(product decision pending): the slug is derived from "<category> <name>" and is UNIQUE, so
	// two products with the same French name in the same category collide. The repository returns
	// the raw driver error (wrapped as "failed to insert product") instead of a typed, user-facing
	// "name already used" error, so the API reports it as "Internal server error". Replace the
	// pq assertions with the typed error once the owner decides how to report duplicates.
	t.Run("Create with a duplicate slug fails with a raw unique violation", func(t *testing.T) {
		first := e.newProduct(t, sushi, func(p *domain.Product) {
			p.Translations = []domain.Translation{tr("fr", "Doublon")}
		})
		require.Equal(t, "sushi-doublon", *first.Slug)

		dup := &domain.Product{ID: uuid.New(), Price: dec("1"), Code: sp("DUP1"), CategoryID: sushi, VatCategory: domain.VatCategoryFood,
			Translations: []domain.Translation{tr("fr", "Doublon"), tr("en", "Duplicate")}}
		err := e.repo.Create(ctx, dup)
		require.ErrorContains(t, err, "failed to insert product")
		var pqErr *pq.Error
		require.ErrorAs(t, err, &pqErr, "the driver error leaks through untyped")
		assert.Equal(t, pq.ErrorCode("23505"), pqErr.Code)
		assert.Equal(t, "products_slug_unique", pqErr.Constraint)

		_, ferr := e.repo.FindByID(ctx, dup.ID)
		assert.ErrorContains(t, ferr, "product not found", "nothing of the duplicate was stored")
	})

	t.Run("Update changes fields, regenerates the slug and upserts translations without dropping others", func(t *testing.T) {
		p := e.newProduct(t, sushi, nil)
		p.Price = dec("15.00")
		p.IsAvailable = false
		p.CategoryID = drinks
		p.Translations = []domain.Translation{tr("fr", "Thon"), tr("zh", "金枪鱼")}
		require.NoError(t, e.repo.Update(ctx, p))

		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		assert.True(t, got.Price.Equal(dec("15.00")))
		assert.False(t, got.IsAvailable)
		assert.Equal(t, drinks, got.CategoryID)
		assert.Equal(t, "boissons-thon", *got.Slug)
		byLang := map[string]string{}
		for _, tt := range got.Translations {
			byLang[tt.Language] = tt.Name
		}
		assert.Equal(t, map[string]string{"fr": "Thon", "en": "Salmon", "nl": "Zalm", "zh": "金枪鱼"}, byLang)
	})

	t.Run("Update without a French name leaves the slug alone", func(t *testing.T) {
		p := e.newProduct(t, sushi, nil)
		before := *p.Slug
		p.Translations = []domain.Translation{tr("en", "Salmon v2")}
		require.NoError(t, e.repo.Update(ctx, p))
		got, err := e.repo.FindByID(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, before, *got.Slug)
	})

	t.Run("Update failures: category without a French name, bad VAT category", func(t *testing.T) {
		p := e.newProduct(t, sushi, nil)
		p.CategoryID = bare
		require.ErrorContains(t, e.repo.Update(ctx, p), "failed to fetch category translation name")

		p.CategoryID = sushi
		p.VatCategory = "bogus"
		require.ErrorContains(t, e.repo.Update(ctx, p), "failed to update product")
	})

	t.Run("FindByID reports an unknown product", func(t *testing.T) {
		_, err := e.repo.FindByID(ctx, uuid.New())
		assert.EqualError(t, err, "product not found")
	})

	t.Run("a product without translations is still returned, with an empty list", func(t *testing.T) {
		id := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: sushi, Code: "BARE1", Price: "3.00"})
		got, err := e.repo.FindByID(ctx, id)
		require.NoError(t, err)
		assert.Empty(t, got.Translations)
	})
}

func TestProductRepositoryListingsAndOrdering(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	cat := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi"})
	other := testhelpers.SeedCategory(t, e.tdb.DB, 2, map[string]string{"fr": "Autres"})
	mk := func(code, fr string) uuid.UUID {
		return testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: code, Price: "1.00", Names: map[string]string{"fr": fr, "en": "EN " + fr}})
	}
	// Natural ordering: letters first, then the number inside the code, then the French name.
	b2, a10, a2, a2dup := mk("B2", "B deux"), mk("A10", "A dix"), mk("A2", "A deux zeta"), mk("A2", "A deux alpha")
	noCode := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "", Price: "1.00", Names: map[string]string{"fr": "Sans code"}})
	_, err := e.tdb.DB.ExecContext(ctx, `UPDATE products SET code = NULL WHERE id = $1`, noCode)
	require.NoError(t, err)
	elsewhere := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: other, Code: "Z1", Price: "1.00", Names: map[string]string{"nl": "Alleen NL"}})

	t.Run("FindAll orders by code letters, then number, then French name; products without a code come first", func(t *testing.T) {
		all, err := e.repo.FindAll(ctx)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{noCode, a2dup, a2, a10, b2, elsewhere}, ids(all))
	})

	t.Run("FindByCategoryID and FindByCategoryIDs filter and group", func(t *testing.T) {
		in, err := e.repo.FindByCategoryID(ctx, cat.String())
		require.NoError(t, err)
		assert.Len(t, in, 5)

		grouped, err := e.repo.FindByCategoryIDs(ctx, []string{cat.String(), other.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, grouped, 2)
		assert.Len(t, grouped[cat.String()], 5)
		assert.Equal(t, []uuid.UUID{elsewhere}, ids(grouped[other.String()]))
	})

	t.Run("BatchGetProductByIDs keys by id and short-circuits an empty list", func(t *testing.T) {
		got, err := e.repo.BatchGetProductByIDs(ctx, []string{a2.String(), b2.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, a2, got[a2.String()][0].ID)

		empty, err := newEnvClosed(t).BatchGetProductByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, empty)
	})

	t.Run("translation batches group per product / category", func(t *testing.T) {
		got, err := e.repo.BatchGetProductTranslations(ctx, []string{a2.String(), elsewhere.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Len(t, got[a2.String()], 2)
		require.Len(t, got[elsewhere.String()], 1)
		assert.Equal(t, "nl", got[elsewhere.String()][0].Language)
		assert.Equal(t, "Alleen NL", got[elsewhere.String()][0].Name)

		cats, err := e.repo.BatchGetCategoryTranslations(ctx, []string{cat.String(), other.String()})
		require.NoError(t, err)
		assert.Equal(t, "Sushi", cats[cat.String()][0].Name)
		assert.Equal(t, "fr", cats[other.String()][0].Language)

		closed := newEnvClosed(t)
		none, err := closed.BatchGetProductTranslations(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, none)
		none2, err := closed.BatchGetCategoryTranslations(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, none2)
	})

	t.Run("products sharing a code are ordered by their French name, else their first name, else empty", func(t *testing.T) {
		nlOnly := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: other, Code: "Q1", Price: "1.00", Names: map[string]string{"nl": "Beta"}})
		noNames := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: other, Code: "Q1", Price: "1.00"})
		withFr := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: other, Code: "Q1", Price: "1.00", Names: map[string]string{"fr": "Gamma"}})
		got, err := e.repo.FindByCategoryID(ctx, other.String())
		require.NoError(t, err)
		var q []uuid.UUID
		for _, p := range got {
			if *p.Code == "Q1" {
				q = append(q, p.ID)
			}
		}
		assert.Equal(t, []uuid.UUID{noNames, nlOnly, withFr}, q)
	})
}

func TestProductRepositoryOrderDetails(t *testing.T) {
	e := newEnv(t)
	cat := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi", "nl": "Sushi NL", "en": ""})
	catNoNL := testhelpers.SeedCategory(t, e.tdb.DB, 2, map[string]string{"fr": "Boissons"})
	full := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "A1", Price: "10.00", Names: map[string]string{"fr": "Saumon", "nl": "Zalm", "en": ""}, VatCategory: "food"})
	frOnly := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: catNoNL, Code: "B1", Price: "3.50", Names: map[string]string{"fr": "Cola"}, VatCategory: "beverage", LunchOnly: true, NotDiscountable: true})
	soldOut := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "C1", Price: "5.00", Names: map[string]string{"fr": "Épuisé"}, SoldOut: true})
	untranslated := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Code: "D1", Price: "1.00"})
	all := []string{full.String(), frOnly.String(), soldOut.String(), untranslated.String(), uuid.NewString()}
	byID := func(ds []*domain.ProductOrderDetails) map[uuid.UUID]*domain.ProductOrderDetails {
		m := map[uuid.UUID]*domain.ProductOrderDetails{}
		for _, d := range ds {
			m[d.ID] = d
		}
		return m
	}

	t.Run("FindForPricing returns sold-out products flagged, names in the request language with fallback", func(t *testing.T) {
		nl, err := e.repo.FindForPricing(utils.SetLang(t.Context(), "nl"), all)
		require.NoError(t, err)
		m := byID(nl)
		require.Len(t, m, 4, "unknown ids are simply absent")
		assert.Equal(t, "Zalm", m[full].Name)
		assert.Equal(t, "Sushi NL", m[full].CategoryName)
		assert.Equal(t, "Cola", m[frOnly].Name, "no NL translation: falls back to French")
		assert.Equal(t, "Boissons", m[frOnly].CategoryName)
		assert.True(t, m[frOnly].IsLunchOnly)
		assert.False(t, m[frOnly].IsDiscountable)
		assert.Equal(t, domain.VatCategoryBeverage, m[frOnly].VatCategory)
		assert.True(t, m[frOnly].Price.Equal(dec("3.50")))
		assert.False(t, m[soldOut].IsAvailable)
		assert.True(t, m[full].IsAvailable)
		assert.Equal(t, "", m[untranslated].Name, "no translation at all is an empty name, not a missing product")
		assert.Equal(t, "A1", *nl[0].Code, "ordered by code")

		fr, err := e.repo.FindForPricing(utils.SetLang(t.Context(), "fr"), all)
		require.NoError(t, err)
		assert.Equal(t, "Saumon", byID(fr)[full].Name)
		assert.Equal(t, "Sushi", byID(fr)[full].CategoryName)
	})

	t.Run("FindByIDs refuses the whole request when any product is sold out", func(t *testing.T) {
		_, err := e.repo.FindByIDs(t.Context(), all)
		require.ErrorContains(t, err, "some products are not available")
		assert.ErrorContains(t, err, soldOut.String())
	})

	t.Run("FindByIDs returns details when everything is on sale", func(t *testing.T) {
		got, err := e.repo.FindByIDs(utils.SetLang(t.Context(), "nl"), []string{full.String(), frOnly.String()})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "Zalm", got[0].Name)
		assert.Equal(t, "Cola", got[1].Name)
	})

	t.Run("FindNamesByIDs ignores availability (invoices of orders that sold out since)", func(t *testing.T) {
		got, err := e.repo.FindNamesByIDs(utils.SetLang(t.Context(), "fr"), []string{soldOut.String(), full.String()})
		require.NoError(t, err)
		m := byID(got)
		assert.Equal(t, "Épuisé", m[soldOut].Name)
		assert.Equal(t, "Saumon", m[full].Name)
	})

	t.Run("an empty-name translation never shadows a real one", func(t *testing.T) {
		en, err := e.repo.FindNamesByIDs(utils.SetLang(t.Context(), "en"), []string{full.String()})
		require.NoError(t, err)
		assert.Equal(t, "Saumon", en[0].Name, "the blank English row is skipped for the French one")
		assert.Equal(t, "Sushi", en[0].CategoryName)
	})
}

func TestProductRepositoryCategories(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	first := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi", "en": "Sushi EN"})
	second := testhelpers.SeedCategory(t, e.tdb.DB, 2, map[string]string{"fr": "Boissons"})
	third := testhelpers.SeedCategory(t, e.tdb.DB, 3, nil)

	t.Run("FindAllCategories sorts by order and keeps categories without translations", func(t *testing.T) {
		got, err := e.repo.FindAllCategories(ctx)
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, []uuid.UUID{first, second, third}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID})
		assert.Len(t, got[0].Translations, 2)
		assert.Equal(t, "Sushi", got[0].GetTranslationFor("fr").Name)
		assert.Equal(t, "Sushi EN", got[0].GetTranslationFor("en").Name)
		assert.Empty(t, got[2].Translations)
		assert.Equal(t, 3, got[2].Order)
		assert.NotEmpty(t, got[2].Slug)
	})

	t.Run("FindCategoryByID and FindCategoryBySlug load one category", func(t *testing.T) {
		byID, err := e.repo.FindCategoryByID(ctx, first)
		require.NoError(t, err)
		assert.Len(t, byID.Translations, 2)
		bySlug, err := e.repo.FindCategoryBySlug(ctx, byID.Slug)
		require.NoError(t, err)
		assert.Equal(t, first, bySlug.ID)

		bare, err := e.repo.FindCategoryByID(ctx, third)
		require.NoError(t, err)
		assert.Empty(t, bare.Translations)
	})

	t.Run("an unknown category is sql.ErrNoRows", func(t *testing.T) {
		_, err := e.repo.FindCategoryByID(ctx, uuid.New())
		assert.ErrorIs(t, err, sql.ErrNoRows)
		_, err = e.repo.FindCategoryBySlug(ctx, "nope")
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("FindCategoriesByProductIDs maps each product to its category with translations", func(t *testing.T) {
		p1 := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: first, Price: "1"})
		p2 := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: third, Price: "1"})
		got, err := e.repo.FindCategoriesByProductIDs(ctx, []string{p1.String(), p2.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Len(t, got[p1.String()], 1)
		assert.Equal(t, first, got[p1.String()][0].ID)
		assert.Len(t, got[p1.String()][0].Translations, 2)
		require.Len(t, got[p2.String()], 1, "a category without translations is still the product's category")
		assert.Empty(t, got[p2.String()][0].Translations)
	})
}

func TestProductRepositoryChoices(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	cat := testhelpers.SeedCategory(t, e.tdb.DB, 1, map[string]string{"fr": "Sushi"})
	p1 := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Price: "10"})
	p2 := testhelpers.SeedProduct(t, e.tdb.DB, testhelpers.ProductSpec{CategoryID: cat, Price: "10"})

	var g1, g2 *domain.ProductChoiceGroup
	t.Run("CreateChoiceGroup stores the group with its translations", func(t *testing.T) {
		g1 = &domain.ProductChoiceGroup{ID: uuid.New(), ProductID: p1, MinSelections: 1, MaxSelections: 2, SortOrder: 2,
			Translations: []domain.ChoiceTranslation{{Locale: "fr", Name: "Sauce"}, {Locale: "en", Name: "Sauce EN"}}}
		g2 = &domain.ProductChoiceGroup{ID: uuid.New(), ProductID: p1, MinSelections: 0, MaxSelections: 1, SortOrder: 1}
		require.NoError(t, e.repo.CreateChoiceGroup(ctx, g1))
		require.NoError(t, e.repo.CreateChoiceGroup(ctx, g2))

		got, err := e.repo.FindChoiceGroupByID(ctx, g1.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, got.MinSelections)
		assert.Equal(t, 2, got.MaxSelections)
		assert.Equal(t, "Sauce", got.GetTranslationFor("fr"))
		assert.Equal(t, "Sauce EN", got.GetTranslationFor("en"))
		assert.Equal(t, "Sauce", got.GetTranslationFor("zh"), "falls back to French")
	})

	t.Run("CreateChoiceGroup rolls back on a bad translation or product", func(t *testing.T) {
		bad := &domain.ProductChoiceGroup{ID: uuid.New(), ProductID: p1, MaxSelections: 1, Translations: []domain.ChoiceTranslation{{Locale: "fr", Name: "A"}, {Locale: "fr", Name: "B"}}}
		require.ErrorContains(t, e.repo.CreateChoiceGroup(ctx, bad), "insert choice group translation")
		_, err := e.repo.FindChoiceGroupByID(ctx, bad.ID)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		orphan := &domain.ProductChoiceGroup{ID: uuid.New(), ProductID: uuid.New(), MaxSelections: 1}
		require.ErrorContains(t, e.repo.CreateChoiceGroup(ctx, orphan), "insert product choice group")
	})

	t.Run("choice group reads: by product (sorted), batches, unknown", func(t *testing.T) {
		byProduct, err := e.repo.FindChoiceGroupsByProductID(ctx, p1)
		require.NoError(t, err)
		require.Len(t, byProduct, 2)
		assert.Equal(t, g2.ID, byProduct[0].ID, "sorted by sort_order")
		assert.Equal(t, g1.ID, byProduct[1].ID)
		assert.Empty(t, byProduct[0].Translations)

		batch, err := e.repo.BatchGetChoiceGroupsByProductIDs(ctx, []string{p1.String(), p2.String()})
		require.NoError(t, err)
		assert.Len(t, batch[p1.String()], 2)
		assert.NotContains(t, batch, p2.String())

		byIDs, err := e.repo.BatchGetChoiceGroupsByIDs(ctx, []string{g1.ID.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, byIDs, 1)
		assert.Equal(t, g1.ID, byIDs[g1.ID.String()][0].ID)

		_, err = e.repo.FindChoiceGroupByID(ctx, uuid.New())
		assert.ErrorIs(t, err, sql.ErrNoRows)

		closed := newEnvClosed(t)
		a, err := closed.BatchGetChoiceGroupsByProductIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, a)
		b, err := closed.BatchGetChoiceGroupsByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, b)
	})

	t.Run("UpdateChoiceGroup changes limits and upserts translations", func(t *testing.T) {
		g1.MinSelections, g1.MaxSelections, g1.SortOrder = 0, 3, 5
		g1.Translations = []domain.ChoiceTranslation{{Locale: "fr", Name: "Sauces"}, {Locale: "zh", Name: "酱"}}
		require.NoError(t, e.repo.UpdateChoiceGroup(ctx, g1))
		got, err := e.repo.FindChoiceGroupByID(ctx, g1.ID)
		require.NoError(t, err)
		assert.Equal(t, 3, got.MaxSelections)
		assert.Equal(t, 5, got.SortOrder)
		names := map[string]string{}
		for _, tt := range got.Translations {
			names[tt.Locale] = tt.Name
		}
		assert.Equal(t, map[string]string{"fr": "Sauces", "en": "Sauce EN", "zh": "酱"}, names)
	})

	t.Run("UpdateChoiceGroup fails on an invalid limit and rolls back", func(t *testing.T) {
		before, err := e.repo.FindChoiceGroupByID(ctx, g2.ID)
		require.NoError(t, err)
		bad := *g2
		bad.MinSelections = -1
		err = e.repo.UpdateChoiceGroup(ctx, &bad) // CHECK (min_selections >= 0) rejects it
		require.ErrorContains(t, err, "update product choice group")
		after, err := e.repo.FindChoiceGroupByID(ctx, g2.ID)
		require.NoError(t, err)
		assert.Equal(t, before.MinSelections, after.MinSelections)
	})

	var c1, c2 *domain.ProductChoice
	t.Run("CreateChoice stores price modifier, sort order and translations", func(t *testing.T) {
		c1 = &domain.ProductChoice{ID: uuid.New(), ProductID: p1, ChoiceGroupID: g1.ID, PriceModifier: dec("0.50"), SortOrder: 2,
			Translations: []domain.ChoiceTranslation{{Locale: "fr", Name: "Soja"}, {Locale: "nl", Name: "Soja NL"}}}
		c2 = &domain.ProductChoice{ID: uuid.New(), ProductID: p1, ChoiceGroupID: g1.ID, PriceModifier: dec("0"), SortOrder: 1}
		require.NoError(t, e.repo.CreateChoice(ctx, c1))
		require.NoError(t, e.repo.CreateChoice(ctx, c2))

		got, err := e.repo.FindChoiceByID(ctx, c1.ID)
		require.NoError(t, err)
		assert.True(t, got.PriceModifier.Equal(dec("0.50")))
		assert.Equal(t, g1.ID, got.ChoiceGroupID)
		assert.Equal(t, p1, got.ProductID)
		assert.Equal(t, "Soja NL", got.GetTranslationFor("nl"))
		assert.Equal(t, c1.ID, got.Translations[0].ProductChoiceID)
	})

	t.Run("CreateChoice failures roll back", func(t *testing.T) {
		dupTr := &domain.ProductChoice{ID: uuid.New(), ProductID: p1, ChoiceGroupID: g1.ID, Translations: []domain.ChoiceTranslation{{Locale: "fr", Name: "A"}, {Locale: "fr", Name: "B"}}}
		require.ErrorContains(t, e.repo.CreateChoice(ctx, dupTr), "insert choice translation")
		_, err := e.repo.FindChoiceByID(ctx, dupTr.ID)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		neg := &domain.ProductChoice{ID: uuid.New(), ProductID: p1, ChoiceGroupID: g1.ID, PriceModifier: dec("-1")}
		require.ErrorContains(t, e.repo.CreateChoice(ctx, neg), "insert product choice", "negative modifiers are refused by the schema")
	})

	t.Run("choice reads: by product (sorted), batches, unknown", func(t *testing.T) {
		byProduct, err := e.repo.FindChoicesByProductID(ctx, p1)
		require.NoError(t, err)
		require.Len(t, byProduct, 2)
		assert.Equal(t, c2.ID, byProduct[0].ID)
		assert.Equal(t, c1.ID, byProduct[1].ID)
		assert.Empty(t, byProduct[0].Translations)
		assert.Len(t, byProduct[1].Translations, 2)

		batch, err := e.repo.BatchGetChoicesByProductIDs(ctx, []string{p1.String(), p2.String()})
		require.NoError(t, err)
		assert.Len(t, batch[p1.String()], 2)
		assert.NotContains(t, batch, p2.String())

		byIDs, err := e.repo.BatchGetChoicesByIDs(ctx, []string{c1.ID.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, byIDs, 1)
		assert.Equal(t, c1.ID, byIDs[c1.ID.String()][0].ID)

		_, err = e.repo.FindChoiceByID(ctx, uuid.New())
		assert.ErrorIs(t, err, sql.ErrNoRows)

		closed := newEnvClosed(t)
		a, err := closed.BatchGetChoicesByProductIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, a)
		b, err := closed.BatchGetChoicesByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, b)
	})

	t.Run("UpdateChoice moves a choice between groups, changes the modifier and upserts translations", func(t *testing.T) {
		c1.ChoiceGroupID = g2.ID
		c1.PriceModifier = dec("1.25")
		c1.SortOrder = 9
		c1.Translations = []domain.ChoiceTranslation{{Locale: "fr", Name: "Soja salé"}}
		require.NoError(t, e.repo.UpdateChoice(ctx, c1))
		got, err := e.repo.FindChoiceByID(ctx, c1.ID)
		require.NoError(t, err)
		assert.Equal(t, g2.ID, got.ChoiceGroupID)
		assert.True(t, got.PriceModifier.Equal(dec("1.25")))
		assert.Equal(t, 9, got.SortOrder)
		assert.Equal(t, "Soja salé", got.GetTranslationFor("fr"))
		assert.Equal(t, "Soja NL", got.GetTranslationFor("nl"), "other locales are kept")

		neg := *c1
		neg.PriceModifier = dec("-2")
		require.ErrorContains(t, e.repo.UpdateChoice(ctx, &neg), "update product choice")
	})

	t.Run("DeleteChoice and DeleteChoiceGroup cascade", func(t *testing.T) {
		require.NoError(t, e.repo.DeleteChoice(ctx, c1.ID))
		_, err := e.repo.FindChoiceByID(ctx, c1.ID)
		assert.ErrorIs(t, err, sql.ErrNoRows)
		var n int
		require.NoError(t, e.tdb.DB.GetContext(ctx, &n, `SELECT count(*) FROM product_choice_translations WHERE product_choice_id = $1`, c1.ID))
		assert.Zero(t, n, "translations are deleted with the choice")

		require.NoError(t, e.repo.DeleteChoiceGroup(ctx, g1.ID))
		_, err = e.repo.FindChoiceGroupByID(ctx, g1.ID)
		assert.ErrorIs(t, err, sql.ErrNoRows)
		_, err = e.repo.FindChoiceByID(ctx, c2.ID)
		assert.ErrorIs(t, err, sql.ErrNoRows, "choices of a deleted group go with it")

		require.NoError(t, e.repo.DeleteChoice(ctx, uuid.New()), "deleting an unknown choice is a no-op")
		require.NoError(t, e.repo.DeleteChoiceGroup(ctx, uuid.New()))
	})
}

func newEnvClosed(t *testing.T) domain.ProductRepository {
	t.Helper()
	conn, err := sqlx.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	return NewProductRepository(&db.DBPool{Customer: conn, Admin: conn})
}

func TestProductRepositoryClosedConnection(t *testing.T) {
	repo := newEnvClosed(t)
	ctx := t.Context()
	id := uuid.New()
	s := []string{id.String()}
	p := &domain.Product{ID: id, Translations: []domain.Translation{tr("fr", "x")}}

	assert.Error(t, repo.Create(ctx, p))
	assert.Error(t, repo.Update(ctx, p))
	_, err := repo.FindByID(ctx, id)
	assert.Error(t, err)
	_, err = repo.FindAll(ctx)
	assert.Error(t, err)
	_, err = repo.FindByIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.FindForPricing(ctx, s)
	assert.Error(t, err)
	_, err = repo.FindNamesByIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.FindByCategoryID(ctx, id.String())
	assert.Error(t, err)
	_, err = repo.FindAllCategories(ctx)
	assert.Error(t, err)
	_, err = repo.FindCategoryByID(ctx, id)
	assert.Error(t, err)
	_, err = repo.FindCategoriesByProductIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.FindByCategoryIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.BatchGetProductByIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.BatchGetProductTranslations(ctx, s)
	assert.ErrorContains(t, err, "failed to query product translations")
	_, err = repo.BatchGetCategoryTranslations(ctx, s)
	assert.ErrorContains(t, err, "failed to query category translations")
	_, err = repo.FindChoiceGroupsByProductID(ctx, id)
	assert.Error(t, err)
	_, err = repo.FindChoiceGroupByID(ctx, id)
	assert.Error(t, err)
	_, err = repo.BatchGetChoiceGroupsByProductIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.BatchGetChoiceGroupsByIDs(ctx, s)
	assert.Error(t, err)
	assert.Error(t, repo.CreateChoiceGroup(ctx, &domain.ProductChoiceGroup{}))
	assert.Error(t, repo.UpdateChoiceGroup(ctx, &domain.ProductChoiceGroup{}))
	assert.Error(t, repo.DeleteChoiceGroup(ctx, id))
	_, err = repo.FindChoicesByProductID(ctx, id)
	assert.Error(t, err)
	_, err = repo.FindChoiceByID(ctx, id)
	assert.Error(t, err)
	_, err = repo.BatchGetChoicesByProductIDs(ctx, s)
	assert.Error(t, err)
	_, err = repo.BatchGetChoicesByIDs(ctx, s)
	assert.Error(t, err)
	assert.Error(t, repo.CreateChoice(ctx, &domain.ProductChoice{}))
	assert.Error(t, repo.UpdateChoice(ctx, &domain.ProductChoice{}))
	assert.Error(t, repo.DeleteChoice(ctx, id))
}
