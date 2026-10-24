package application

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/logging"

	"go.uber.org/zap"
)

// Applier writes a Diff to the local menu, one transaction per batch (a category with its products,
// option groups and choices, then one batch for the retirements). Each batch lands whole or not at
// all; a failed batch is reported and the others still run, so one bad category does not stop the
// menu from updating. Re-running the same diff changes nothing: the plan skips what the content
// hashes say is unchanged.
type Applier struct {
	Local domain.LocalCatalogRepository
	Clock domain.Clock
}

// AppliedChange is a change that landed, with the local row it ended on.
type AppliedChange struct {
	Change  domain.Change
	LocalID uuid.UUID
}

// BatchFailure is a batch that was rolled back.
type BatchFailure struct {
	Batch string
	Err   error
}

// ApplyResult is what Apply did.
type ApplyResult struct {
	Applied []AppliedChange
	Failed  []BatchFailure
}

// ChangedProducts lists the local products touched by the applied changes (the products
// themselves, and the owners of changed option groups and choices), each once.
func (r *ApplyResult) ChangedProducts() []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, a := range r.Applied {
		if a.Change.Entity != domain.EntityProduct || a.LocalID == uuid.Nil || seen[a.LocalID] {
			continue
		}
		seen[a.LocalID] = true
		out = append(out, a.LocalID)
	}
	for _, a := range r.Applied {
		if (a.Change.Entity != domain.EntityChoiceGroup && a.Change.Entity != domain.EntityChoice) || a.Change.ProductKey == "" {
			continue
		}
		for _, p := range r.Applied {
			if p.Change.Entity == domain.EntityProduct && p.Change.Key == a.Change.ProductKey && !seen[p.LocalID] {
				seen[p.LocalID] = true
				out = append(out, p.LocalID)
			}
		}
	}
	return out
}

// Count counts the applied changes of an entity type and action.
func (r *ApplyResult) Count(t domain.EntityType, action domain.Action) int {
	n := 0
	for _, a := range r.Applied {
		if a.Change.Entity == t && a.Change.Action == action {
			n++
		}
	}
	return n
}

// Apply runs the batches of diff. In LINK_ONLY mode it only records links, whatever the diff holds.
func (a *Applier) Apply(ctx context.Context, diff *domain.Diff, mode domain.SyncMode) *ApplyResult {
	res := &ApplyResult{}
	for _, batch := range diff.Batches() {
		var done []AppliedChange
		err := a.Local.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
			done = done[:0]
			for _, ch := range batch {
				id, err := a.applyChange(ctx, tx, ch, mode)
				if err != nil {
					return fmt.Errorf("%s %s %q: %w", ch.Action, ch.Entity, ch.Key, err)
				}
				done = append(done, AppliedChange{Change: ch, LocalID: id})
			}
			return nil
		})
		if err != nil {
			logging.FromContext(ctx).Error("hubrise: catalog batch rolled back", zap.String("batch", batch[0].Batch), zap.Error(err))
			res.Failed = append(res.Failed, BatchFailure{Batch: batch[0].Batch, Err: err})
			continue
		}
		res.Applied = append(res.Applied, done...)
	}
	return res
}

func (a *Applier) applyChange(ctx context.Context, tx domain.CatalogTx, ch domain.Change, mode domain.SyncMode) (uuid.UUID, error) {
	linkOnly := mode == domain.SyncLinkOnly
	if ch.Action == domain.ActionRetire {
		return ch.LocalID, a.retire(ctx, tx, ch)
	}
	var (
		id  uuid.UUID
		err error
	)
	switch ch.Entity {
	case domain.EntityCategory:
		id, err = a.category(ctx, tx, ch, linkOnly)
	case domain.EntityProduct:
		id, err = a.product(ctx, tx, ch, linkOnly)
	case domain.EntityChoiceGroup:
		id, err = a.group(ctx, tx, ch, linkOnly)
	case domain.EntityChoice:
		id, err = a.choice(ctx, tx, ch, linkOnly)
	default:
		err = fmt.Errorf("unknown entity %q", ch.Entity)
	}
	return id, err
}

// local resolves the local row of a parent entry through its link.
func local(ctx context.Context, tx domain.CatalogTx, t domain.EntityType, key string) (uuid.UUID, error) {
	l, err := tx.Links().FindByKey(ctx, t, key)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s %q has no link: %w", t, key, err)
	}
	return l.LocalID(), nil
}

// writeLink records the link of a change. A link-only import stores no hash and no disabled flag,
// so the first full import re-evaluates the entry completely. An existing link keeps its origin,
// its image checksum and its inventory state; writing it also lifts a retirement.
func writeLink(ctx context.Context, tx domain.CatalogTx, ch domain.Change, localID uuid.UUID, ref, hubriseID string, linkOnly bool) error {
	links := tx.Links()
	link := &domain.CatalogLink{EntityType: ch.Entity, HubriseKey: ch.Key, HubriseRef: ref, Origin: ch.Origin}
	if hubriseID != "" {
		link.HubriseID = &hubriseID
	}
	if !linkOnly {
		link.ContentHash = ch.Hash
	}
	if existing, err := links.FindByKey(ctx, ch.Entity, ch.Key); err == nil {
		link.Origin, link.ImageMD5 = existing.Origin, existing.ImageMD5
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	link.SetLocalID(localID)
	if err := links.Upsert(ctx, link); err != nil {
		return err
	}
	if !linkOnly && link.CatalogDisabled != ch.Disabled {
		return links.SetCatalogDisabled(ctx, link.ID, ch.Disabled)
	}
	return nil
}

func textWrites(texts map[string]domain.Text) []domain.TextWrite {
	out := make([]domain.TextWrite, 0, len(texts))
	for lang, t := range texts {
		out = append(out, domain.TextWrite{Language: lang, Name: t.Name, Description: t.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}

func nameWrites(names map[string]string) []domain.TextWrite {
	texts := make(map[string]domain.Text, len(names))
	for lang, n := range names {
		texts[lang] = domain.Text{Name: n}
	}
	return textWrites(texts)
}

func (a *Applier) category(ctx context.Context, tx domain.CatalogTx, ch domain.Change, linkOnly bool) (uuid.UUID, error) {
	c := ch.Category
	id := ch.LocalID
	if ch.Action == domain.ActionCreate {
		var err error
		if id, err = tx.CreateCategory(ctx, domain.NewCategory{Order: ch.Order, SlugBase: ch.Name}); err != nil {
			return uuid.Nil, err
		}
	}
	if !linkOnly {
		if err := tx.UpsertTexts(ctx, domain.TextCategory, id, nameWrites(c.Names)); err != nil {
			return uuid.Nil, err
		}
	}
	return id, writeLink(ctx, tx, ch, id, c.Key, c.HubriseID, linkOnly)
}

func (a *Applier) product(ctx context.Context, tx domain.CatalogTx, ch domain.Change, linkOnly bool) (uuid.UUID, error) {
	p := ch.Product
	id := ch.LocalID
	if ch.Action == domain.ActionCreate {
		catID, err := local(ctx, tx, domain.EntityCategory, ch.ParentKey)
		if err != nil {
			return uuid.Nil, err
		}
		vat := p.VatCategory
		if vat == "" { // unmapped: the column is mandatory, the product is created hidden
			vat = domain.VatFood
		}
		id, err = tx.CreateProduct(ctx, domain.NewProduct{
			CategoryID: catID, Price: p.Price, Code: p.Code, VatCategory: vat,
			Visible: derefBool(ch.SetVisible, false), Available: derefBool(ch.SetAvailable, true),
			Halal: p.Halal, Vegetarian: p.Vegetarian, Spicy: p.Spicy, Discountable: p.Discountable,
			SlugName: ch.Name,
		})
		if err != nil {
			return uuid.Nil, err
		}
	} else if !linkOnly {
		patch := domain.ProductPatch{Visible: ch.SetVisible, Available: ch.SetAvailable}
		for _, f := range ch.Fields {
			switch f {
			case domain.FieldPrice:
				patch.Price = &p.Price
			case domain.FieldCategory:
				catID, err := local(ctx, tx, domain.EntityCategory, ch.ParentKey)
				if err != nil {
					return uuid.Nil, err
				}
				patch.CategoryID = &catID
			case domain.FieldVat:
				patch.VatCategory = &p.VatCategory
			case domain.FieldCode:
				patch.Code = &p.Code
			}
		}
		if patch != (domain.ProductPatch{}) {
			if err := tx.PatchProduct(ctx, id, patch); err != nil {
				return uuid.Nil, err
			}
		}
	}
	if !linkOnly {
		if err := tx.UpsertTexts(ctx, domain.TextProduct, id, textWrites(p.Texts)); err != nil {
			return uuid.Nil, err
		}
	}
	return id, writeLink(ctx, tx, ch, id, p.Key, p.HubriseID, linkOnly)
}

func (a *Applier) group(ctx context.Context, tx domain.CatalogTx, ch domain.Change, linkOnly bool) (uuid.UUID, error) {
	g := ch.Group
	id := ch.LocalID
	if ch.Action == domain.ActionCreate {
		productID, err := local(ctx, tx, domain.EntityProduct, ch.ProductKey)
		if err != nil {
			return uuid.Nil, err
		}
		if id, err = tx.CreateGroup(ctx, domain.NewGroup{ProductID: productID, Min: g.Min, Max: g.Max, Sort: g.Sort}); err != nil {
			return uuid.Nil, err
		}
	} else if !linkOnly {
		patch := domain.GroupPatch{}
		for _, f := range ch.Fields {
			switch f {
			case domain.FieldLimits:
				patch.Min, patch.Max = &g.Min, &g.Max
			case domain.FieldOrder:
				patch.Sort = &g.Sort
			}
		}
		if patch != (domain.GroupPatch{}) {
			if err := tx.PatchGroup(ctx, id, patch); err != nil {
				return uuid.Nil, err
			}
		}
	}
	if !linkOnly {
		if err := tx.UpsertTexts(ctx, domain.TextGroup, id, nameWrites(g.Names)); err != nil {
			return uuid.Nil, err
		}
	}
	return id, writeLink(ctx, tx, ch, id, g.ListRef, g.HubriseID, linkOnly)
}

func (a *Applier) choice(ctx context.Context, tx domain.CatalogTx, ch domain.Change, linkOnly bool) (uuid.UUID, error) {
	c := ch.Choice
	id := ch.LocalID
	if ch.Action == domain.ActionCreate {
		productID, err := local(ctx, tx, domain.EntityProduct, ch.ProductKey)
		if err != nil {
			return uuid.Nil, err
		}
		groupID, err := local(ctx, tx, domain.EntityChoiceGroup, ch.ParentKey)
		if err != nil {
			return uuid.Nil, err
		}
		id, err = tx.CreateChoice(ctx, domain.NewChoice{ProductID: productID, GroupID: groupID, PriceModifier: c.PriceModifier,
			Sort: c.Sort, Unavailable: !derefBool(ch.SetAvailable, true)})
		if err != nil {
			return uuid.Nil, err
		}
	} else if !linkOnly {
		patch := domain.ChoicePatch{}
		for _, f := range ch.Fields {
			switch f {
			case domain.FieldPrice:
				patch.PriceModifier = &c.PriceModifier
			case domain.FieldOrder:
				patch.Sort = &c.Sort
			}
		}
		if ch.SetAvailable != nil {
			unavailable := !*ch.SetAvailable
			patch.Unavailable = &unavailable
		}
		if patch != (domain.ChoicePatch{}) {
			if err := tx.PatchChoice(ctx, id, patch); err != nil {
				return uuid.Nil, err
			}
		}
	}
	if !linkOnly {
		if err := tx.UpsertTexts(ctx, domain.TextChoice, id, nameWrites(c.Names)); err != nil {
			return uuid.Nil, err
		}
	}
	return id, writeLink(ctx, tx, ch, id, c.Ref, c.HubriseID, linkOnly)
}

// retire hides what HubRise no longer has and stamps the link. It never deletes.
func (a *Applier) retire(ctx context.Context, tx domain.CatalogTx, ch domain.Change) error {
	link, err := tx.Links().FindByKey(ctx, ch.Entity, ch.Key)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) { // the link went away since the plan
			return nil
		}
		return err
	}
	no, zero := false, 0
	yes := true
	switch ch.Entity {
	case domain.EntityProduct:
		err = tx.PatchProduct(ctx, link.LocalID(), domain.ProductPatch{Visible: &no, Available: &no})
	case domain.EntityChoiceGroup:
		err = tx.PatchGroup(ctx, link.LocalID(), domain.GroupPatch{Min: &zero})
	case domain.EntityChoice:
		err = tx.PatchChoice(ctx, link.LocalID(), domain.ChoicePatch{Unavailable: &yes})
	}
	if err != nil {
		return err
	}
	now := a.Clock.Now()
	return tx.Links().SetRetired(ctx, link.ID, &now)
}

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
