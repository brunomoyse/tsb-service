package domain_test

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"tsb-service/internal/modules/hubrise/domain"
)

func TestSyncModesAndVatCategories(t *testing.T) {
	for _, m := range []domain.SyncMode{domain.SyncDryRun, domain.SyncLinkOnly, domain.SyncFull} {
		assert.True(t, m.Valid(), m)
	}
	assert.False(t, domain.SyncMode("").Valid())
	assert.False(t, domain.SyncMode("full").Valid())
	for _, v := range []string{domain.VatFood, domain.VatBeverage, domain.VatZeroRated, domain.VatOutOfScope} {
		assert.True(t, domain.ValidVatCategory(v), v)
	}
	assert.False(t, domain.ValidVatCategory("alcohol"))
}

func TestCatalogHashesFollowTheContentTheImportWrites(t *testing.T) {
	p := domain.CatalogProduct{Key: "S", Price: decimal.NewFromInt(3), Visible: true,
		Groups: []domain.CatalogGroup{{Key: "S|L", Choices: []domain.CatalogChoice{{Key: "S|L|O"}}}}}
	base := p.ProductHash()

	// The groups have their own hash and do not move the product's.
	q := p
	q.Groups = nil
	assert.Equal(t, base, q.ProductHash())
	// A price moves it, the visibility only moves the suffix.
	q = p
	q.Price = decimal.NewFromInt(4)
	assert.NotEqual(t, base, q.ProductHash())
	q = p
	q.Visible = false
	assert.NotEqual(t, base, q.ProductHash())
	vis, known := domain.HashVisible(base)
	assert.True(t, vis)
	assert.True(t, known)
	vis, known = domain.HashVisible(q.ProductHash())
	assert.False(t, vis)
	assert.True(t, known)
	_, known = domain.HashVisible("abc")
	assert.False(t, known)
	_, known = domain.HashVisible("")
	assert.False(t, known)

	g := p.Groups[0]
	g2 := g
	g2.Choices = nil
	assert.Equal(t, g.Hash(), g2.Hash(), "a group hash leaves its choices out")
	assert.NotEqual(t, (&domain.CatalogChoice{Key: "a"}).Hash(), (&domain.CatalogChoice{Key: "b"}).Hash())
	assert.NotEqual(t, (&domain.CatalogCategory{Key: "a"}).Hash(), (&domain.CatalogCategory{Key: "b"}).Hash())

	c := &domain.Catalog{Language: "fr", Products: []domain.CatalogProduct{p}}
	h := c.Hash()
	c.Warnings = []domain.Warning{{Code: domain.WarnVATUnmapped}}
	assert.Equal(t, h, c.Hash(), "warnings are not content")
	c.Products[0].Price = decimal.NewFromInt(9)
	assert.NotEqual(t, h, c.Hash())
}

func TestDiffCountsAndBatches(t *testing.T) {
	d := &domain.Diff{Changes: []domain.Change{
		{Entity: domain.EntityProduct, Action: domain.ActionRetire, Key: "r", Batch: domain.RetireBatch},
		{Entity: domain.EntityCategory, Action: domain.ActionCreate, Key: "A", Batch: "A"},
		{Entity: domain.EntityProduct, Action: domain.ActionCreate, Key: "p1", Batch: "A"},
		{Entity: domain.EntityProduct, Action: domain.ActionCreate, Key: "p2", Batch: "B"},
		{Entity: domain.EntityProduct, Action: domain.ActionUpdate, Key: "p3", Batch: "A"},
	}}
	assert.Equal(t, 2, d.Count(domain.EntityProduct, domain.ActionCreate))
	assert.Equal(t, 0, d.Count(domain.EntityChoice, domain.ActionCreate))

	batches := d.Batches()
	assert.Len(t, batches, 3)
	assert.Equal(t, "A", batches[0][0].Batch)
	assert.Equal(t, []string{"A", "p1", "p3"}, []string{batches[0][0].Key, batches[0][1].Key, batches[0][2].Key}, "the order inside a batch is kept")
	assert.Equal(t, "B", batches[1][0].Batch)
	assert.Equal(t, domain.RetireBatch, batches[2][0].Batch, "retirements run last")

	assert.Empty(t, (&domain.Diff{}).Batches())
	only := &domain.Diff{Changes: []domain.Change{{Key: "x", Batch: "A"}}}
	assert.Len(t, only.Batches(), 1)
}

func TestMassRetirementThreshold(t *testing.T) {
	assert.False(t, domain.TripsMassRetire(0, 0))
	assert.False(t, domain.TripsMassRetire(4, 4), "under the minimum")
	assert.False(t, domain.TripsMassRetire(5, 17), "5 of 17 is under 30 %")
	assert.True(t, domain.TripsMassRetire(6, 17))
	assert.True(t, domain.TripsMassRetire(5, 5))
}

func TestPendingReviewMarker(t *testing.T) {
	var nilConn *domain.Connection
	assert.False(t, nilConn.CatalogPendingReview())
	assert.False(t, (&domain.Connection{}).CatalogPendingReview())
	short := "CATALOG"
	assert.False(t, (&domain.Connection{LastError: &short}).CatalogPendingReview())
	other := "something else entirely, longer than the prefix"
	assert.False(t, (&domain.Connection{LastError: &other}).CatalogPendingReview())
	pending := domain.PendingReviewPrefix + "6 of 8"
	assert.True(t, (&domain.Connection{LastError: &pending}).CatalogPendingReview())
}

func TestCatalogErrorsMatchTheirSentinels(t *testing.T) {
	managed := &domain.ManagedError{Fields: []string{"price", "code"}}
	assert.ErrorIs(t, managed, domain.ErrManaged)
	assert.NotErrorIs(t, managed, domain.ErrNotFound)
	assert.Contains(t, managed.Error(), "price, code")

	retire := &domain.RetireConfirmError{Retiring: 6, Linked: 8}
	assert.ErrorIs(t, retire, domain.ErrRetireConfirmRequired)
	assert.NotErrorIs(t, retire, domain.ErrManaged)
	assert.Contains(t, retire.Error(), "6 of 8")

	var target *domain.RetireConfirmError
	assert.True(t, errors.As(error(retire), &target))
}
