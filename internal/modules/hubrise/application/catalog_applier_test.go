package application_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
)

func TestChangedProductsCountsTheOwnersOfChangedOptionsOnce(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	res := &application.ApplyResult{Applied: []application.AppliedChange{
		{Change: domain.Change{Entity: domain.EntityProduct, Key: "A"}, LocalID: a},
		{Change: domain.Change{Entity: domain.EntityChoiceGroup, Key: "A|S", ProductKey: "A"}, LocalID: uuid.New()},
		{Change: domain.Change{Entity: domain.EntityChoice, Key: "B|S|X", ProductKey: "B"}, LocalID: uuid.New()},
		{Change: domain.Change{Entity: domain.EntityProduct, Key: "B"}, LocalID: b},
		{Change: domain.Change{Entity: domain.EntityCategory, Key: "C"}, LocalID: uuid.New()},
		{Change: domain.Change{Entity: domain.EntityChoice, Key: "Z|S|X", ProductKey: "Z"}, LocalID: uuid.New()},
		{Change: domain.Change{Entity: domain.EntityChoice, Key: "?", ProductKey: ""}, LocalID: uuid.New()},
		{Change: domain.Change{Entity: domain.EntityProduct, Key: "N"}, LocalID: uuid.Nil},
	}}
	assert.ElementsMatch(t, []uuid.UUID{a, b}, res.ChangedProducts())
}

func TestApplierHandlesTheEdgesOfAPlan(t *testing.T) {
	e := newCatEnv(t)
	app := &application.Applier{Local: e.local, Clock: e.clock}
	ctx := t.Context()

	// An unknown entity fails its batch and leaves the rest alone.
	res := app.Apply(ctx, &domain.Diff{Changes: []domain.Change{{Entity: "bogus", Action: domain.ActionCreate, Key: "X", Batch: "B"}}}, domain.SyncFull)
	require.Len(t, res.Failed, 1)
	assert.ErrorContains(t, res.Failed[0].Err, "unknown entity")

	// Retiring an entry whose link vanished since the plan is a no-op.
	res = app.Apply(ctx, &domain.Diff{Changes: []domain.Change{{Entity: domain.EntityProduct, Action: domain.ActionRetire, Key: "GONE", Batch: domain.RetireBatch}}}, domain.SyncFull)
	assert.Empty(t, res.Failed)

	// A product created without any visibility decision is hidden and available.
	cat := &domain.CatalogCategory{Key: "C", Names: names("Carte")}
	prod := &domain.CatalogProduct{Key: "P", CategoryKey: "C", Texts: map[string]domain.Text{"fr": {Name: "Plat"}}, Price: dec("3")}
	res = app.Apply(ctx, &domain.Diff{Changes: []domain.Change{
		{Entity: domain.EntityCategory, Action: domain.ActionCreate, Key: "C", Name: "Carte", Order: 1, Batch: "C", Category: cat, Origin: domain.OriginImport},
		{Entity: domain.EntityProduct, Action: domain.ActionCreate, Key: "P", Name: "Plat", ParentKey: "C", Batch: "C", Product: prod, Origin: domain.OriginImport},
	}}, domain.SyncFull)
	require.Empty(t, res.Failed)
	p := e.product("P")
	assert.False(t, p.Visible)
	assert.True(t, p.Available)
	assert.Equal(t, "food", p.VatCategory, "an unmapped VAT category keeps the column valid")
}

func TestReorderedOptionListsAndOptionsAreUpdated(t *testing.T) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()

	m := sushiMenu()
	m.d.Products[0].Skus[0] = codeSku("SAUMON-6", "5.50", "M1", "EXTRA", "SAUCE")
	m.d.OptionLists[0].Options[0], m.d.OptionLists[0].Options[1] = m.d.OptionLists[0].Options[1], m.d.OptionLists[0].Options[0]
	e.publish(m)
	res := e.fullImport()

	assert.Equal(t, 2, res.Result.Count(domain.EntityChoiceGroup, domain.ActionUpdate))
	assert.Equal(t, 2, res.Result.Count(domain.EntityChoice, domain.ActionUpdate))
	var sort int
	require.NoError(t, e.db.GetContext(t.Context(), &sort, `
		SELECT g.sort_order FROM product_choice_groups g JOIN hubrise_catalog_links l ON l.choice_group_id = g.id WHERE l.hubrise_key = 'SAUMON-6|SAUCE'`))
	assert.Equal(t, 1, sort)
}
