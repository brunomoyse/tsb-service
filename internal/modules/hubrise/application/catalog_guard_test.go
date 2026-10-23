package application_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
)

func managedFields(t *testing.T, err error) []string {
	t.Helper()
	var m *domain.ManagedError
	require.ErrorAs(t, err, &m)
	assert.ErrorIs(t, err, domain.ErrManaged)
	return m.Fields
}

// guarded imports the sushi menu and returns the ids of what the guard looks at.
type guarded struct {
	e             *cenv
	product       productRow
	group, choice uuid.UUID
}

func newGuarded(t *testing.T) (*guarded, *cenv) {
	e := newCatEnv(t)
	e.publish(sushiMenu())
	e.fullImport()
	g := &guarded{e: e, product: e.product("SAUMON-6")}
	gl, err := e.links.FindByKey(t.Context(), domain.EntityChoiceGroup, "SAUMON-6|SAUCE")
	require.NoError(t, err)
	cl, err := e.links.FindByKey(t.Context(), domain.EntityChoice, "SAUMON-6|SAUCE|SOJA")
	require.NoError(t, err)
	g.group, g.choice = gl.LocalID(), cl.LocalID()
	return g, e
}

func (g *guarded) fields() domain.ProductFields {
	return domain.ProductFields{ID: g.product.ID, CategoryID: g.product.CategoryID, Price: dec(g.product.Price), Code: *g.product.Code, VatCategory: g.product.VatCategory}
}

func TestGuardLocksTheFieldsHubRiseOwns(t *testing.T) {
	g, e := newGuarded(t)
	ctx := t.Context()
	cur := g.fields()

	same := cur
	assert.NoError(t, e.guard.CheckProduct(ctx, cur, same), "an edit that changes nothing owned")

	upd := cur
	upd.Price = dec("9")
	upd.CategoryID = uuid.New()
	upd.VatCategory = domain.VatBeverage
	upd.Code = "Z9"
	assert.ElementsMatch(t, []string{domain.LockedPrice, domain.LockedCategory, domain.LockedVatCategory, domain.LockedCode},
		managedFields(t, e.guard.CheckProduct(ctx, cur, upd)))

	// A product without a code may be given one.
	noCode := cur
	noCode.Code = ""
	withCode := cur
	withCode.Code = "X1"
	assert.NoError(t, e.guard.CheckProduct(ctx, noCode, withCode))
}

func TestGuardLocksChoiceGroupsAndChoices(t *testing.T) {
	g, e := newGuarded(t)
	ctx := t.Context()
	curG := &domain.GroupFields{ID: g.group, ProductID: g.product.ID, Min: 1, Max: 1, Sort: 0, Names: map[string]string{"fr": "Sauce"}}
	curC := &domain.ChoiceFields{ID: g.choice, ProductID: g.product.ID, GroupID: g.group, PriceModifier: decimal.Zero, Names: map[string]string{"fr": "Soja"}}

	// The names in the other languages are the shop's; the French one is HubRise's.
	other := *curG
	other.Names = map[string]string{"fr": "Sauce", "nl": "Saus"}
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, curG, &other))
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, curG, curG))
	changed := *curG
	changed.Min, changed.Max, changed.Sort, changed.Names = 0, 3, 4, map[string]string{"fr": "Sauces"}
	assert.ElementsMatch(t, []string{domain.LockedLimits, domain.LockedSort, domain.LockedName}, managedFields(t, e.guard.CheckChoiceGroup(ctx, curG, &changed)))
	assert.Equal(t, []string{domain.LockedChoiceGroups}, managedFields(t, e.guard.CheckChoiceGroup(ctx, nil, &domain.GroupFields{ProductID: g.product.ID})), "creation")
	assert.Equal(t, []string{domain.LockedChoiceGroups}, managedFields(t, e.guard.CheckChoiceGroup(ctx, curG, nil)), "deletion")

	otherC := *curC
	otherC.Names = map[string]string{"fr": "Soja", "zh": "酱油"}
	assert.NoError(t, e.guard.CheckChoice(ctx, curC, &otherC))
	assert.NoError(t, e.guard.CheckChoice(ctx, curC, curC))
	changedC := *curC
	changedC.PriceModifier, changedC.Sort, changedC.Names = decimal.NewFromInt(2), 3, map[string]string{"fr": "Soja bio"}
	assert.ElementsMatch(t, []string{domain.LockedPriceMod, domain.LockedSort, domain.LockedName}, managedFields(t, e.guard.CheckChoice(ctx, curC, &changedC)))
	moved := *curC
	moved.GroupID = uuid.New()
	assert.Equal(t, []string{domain.LockedSort}, managedFields(t, e.guard.CheckChoice(ctx, curC, &moved)))
	assert.Equal(t, []string{domain.LockedChoices}, managedFields(t, e.guard.CheckChoice(ctx, nil, &domain.ChoiceFields{GroupID: g.group})), "creation in a managed group")
	assert.Equal(t, []string{domain.LockedChoices}, managedFields(t, e.guard.CheckChoice(ctx, curC, nil)), "deletion")
}

func TestGuardLeavesUnlinkedAndRetiredRowsAlone(t *testing.T) {
	g, e := newGuarded(t)
	ctx := t.Context()
	cat := newLocalCategory(t, e, 20, "Maison")
	own := e.localProduct(cat, "", "Plat maison", "7")
	ownGroup := e.localGroup(own, "Sauce")
	ownChoice := e.localChoice(ownGroup, own, "Soja")
	edit := domain.ProductFields{ID: own, CategoryID: uuid.New(), Price: dec("9"), VatCategory: domain.VatBeverage, Code: "N1"}
	cur := domain.ProductFields{ID: own, CategoryID: cat, Price: dec("7"), VatCategory: domain.VatFood, Code: "N0"}

	assert.NoError(t, e.guard.CheckProduct(ctx, cur, edit))
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, nil, &domain.GroupFields{ProductID: own}))
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, &domain.GroupFields{ID: ownGroup}, nil))
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, &domain.GroupFields{ID: ownGroup, Min: 1}, &domain.GroupFields{ID: ownGroup, Min: 0}))
	assert.NoError(t, e.guard.CheckChoice(ctx, nil, &domain.ChoiceFields{GroupID: ownGroup}))
	assert.NoError(t, e.guard.CheckChoice(ctx, &domain.ChoiceFields{ID: ownChoice}, nil))
	assert.NoError(t, e.guard.CheckChoice(ctx, &domain.ChoiceFields{ID: ownChoice}, &domain.ChoiceFields{ID: ownChoice, Sort: 4}))

	// A product HubRise retired is the shop's again.
	e.exec(`UPDATE hubrise_catalog_links SET retired_at = now() WHERE product_id = $1`, g.product.ID)
	retiredEdit := g.fields()
	retiredEdit.Price = dec("99")
	assert.NoError(t, e.guard.CheckProduct(ctx, g.fields(), retiredEdit))
}

func TestGuardIsOffWhenTheIntegrationIsNotManagingTheCatalog(t *testing.T) {
	g, e := newGuarded(t)
	ctx := t.Context()
	cur := g.fields()
	edit := cur
	edit.Price = dec("99")
	require.Error(t, e.guard.CheckProduct(ctx, cur, edit), "locked while managed")

	e.setting(domain.SettingsPatch{SyncCatalog: offSwitch()})
	assert.NoError(t, e.guard.CheckProduct(ctx, cur, edit), "catalog sync off")
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, nil, &domain.GroupFields{ProductID: g.product.ID}))
	assert.NoError(t, e.guard.CheckChoice(ctx, nil, &domain.ChoiceFields{GroupID: g.group}))

	e.setting(domain.SettingsPatch{SyncCatalog: on()})
	require.Error(t, e.guard.CheckProduct(ctx, cur, edit))
	e.setting(domain.SettingsPatch{Enabled: offSwitch()})
	assert.NoError(t, e.guard.CheckProduct(ctx, cur, edit), "integration disabled")

	e.setting(domain.SettingsPatch{Enabled: on()})
	e.exec(`UPDATE hubrise_connection SET access_token_enc = NULL`)
	assert.NoError(t, e.guard.CheckProduct(ctx, cur, edit), "disconnected")

	e.exec(`DELETE FROM hubrise_connection`)
	assert.NoError(t, e.guard.CheckProduct(ctx, cur, edit), "never connected: standalone")
	assert.NoError(t, e.guard.CheckChoiceGroup(ctx, nil, &domain.GroupFields{ProductID: g.product.ID}))
	assert.NoError(t, e.guard.CheckChoice(ctx, nil, &domain.ChoiceFields{GroupID: g.group}))
}
