package application

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise/domain"
)

// Guard enforces the ownership model of a HubRise-managed menu: while the integration is enabled
// with catalog sync on, the fields HubRise owns on a linked product (price, category, VAT category,
// the code it supplied, its choice groups and choices) cannot be edited in the shop, because the next
// import would silently undo the edit. Everything else stays editable: names and descriptions
// (an edit becomes a manual override that imports respect), the other languages, the dietary flags,
// visibility, availability, the image.
//
// With HubRise not connected, disabled, catalog sync off, or an entry not linked (or retired from
// HubRise), the guard does nothing: managed products simply become ordinary products again.
type Guard struct {
	Connections domain.ConnectionRepository
	Links       domain.CatalogLinkRepository
}

// active returns the connection when the lock is in force.
func (g *Guard) active(ctx context.Context) (*domain.Connection, bool, error) {
	conn, err := g.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return conn, conn.Connected() && conn.Enabled && conn.SyncCatalog, nil
}

// managed reports whether the local row has a live (not retired) link.
func (g *Guard) managed(ctx context.Context, t domain.EntityType, id uuid.UUID) (bool, error) {
	l, err := g.Links.FindByLocal(ctx, t, id)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return l.RetiredAt == nil, nil
}

func managedError(fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	return &domain.ManagedError{Fields: fields}
}

// CheckProduct refuses an edit of the HubRise-owned fields of a linked product. A code is locked
// once the product has one, because a code on a linked product comes from HubRise or was matched to it.
func (g *Guard) CheckProduct(ctx context.Context, current, updated domain.ProductFields) error {
	_, on, err := g.active(ctx)
	if err != nil || !on {
		return err
	}
	linked, err := g.managed(ctx, domain.EntityProduct, current.ID)
	if err != nil || !linked {
		return err
	}
	var fields []string
	if !current.Price.Equal(updated.Price) {
		fields = append(fields, domain.LockedPrice)
	}
	if current.CategoryID != updated.CategoryID {
		fields = append(fields, domain.LockedCategory)
	}
	if current.VatCategory != updated.VatCategory {
		fields = append(fields, domain.LockedVatCategory)
	}
	if current.Code != "" && current.Code != updated.Code {
		fields = append(fields, domain.LockedCode)
	}
	return managedError(fields)
}

// CheckChoiceGroup guards a choice group edit. current is nil for a creation (updated.ProductID names
// the product) and updated is nil for a deletion.
func (g *Guard) CheckChoiceGroup(ctx context.Context, current, updated *domain.GroupFields) error {
	conn, on, err := g.active(ctx)
	if err != nil || !on {
		return err
	}
	switch {
	case current == nil: // creation: HubRise owns the set of groups of a linked product
		linked, err := g.managed(ctx, domain.EntityProduct, updated.ProductID)
		if err != nil || !linked {
			return err
		}
		return managedError([]string{domain.LockedChoiceGroups})
	case updated == nil:
		linked, err := g.managed(ctx, domain.EntityChoiceGroup, current.ID)
		if err != nil || !linked {
			return err
		}
		return managedError([]string{domain.LockedChoiceGroups})
	}
	linked, err := g.managed(ctx, domain.EntityChoiceGroup, current.ID)
	if err != nil || !linked {
		return err
	}
	var fields []string
	if current.Min != updated.Min || current.Max != updated.Max {
		fields = append(fields, domain.LockedLimits)
	}
	if current.Sort != updated.Sort {
		fields = append(fields, domain.LockedSort)
	}
	if nameChanged(conn, current.Names, updated.Names) {
		fields = append(fields, domain.LockedName)
	}
	return managedError(fields)
}

// CheckChoice guards a choice edit, with the same current/updated convention as CheckChoiceGroup
// (updated.GroupID names the group of a new choice).
func (g *Guard) CheckChoice(ctx context.Context, current, updated *domain.ChoiceFields) error {
	conn, on, err := g.active(ctx)
	if err != nil || !on {
		return err
	}
	switch {
	case current == nil:
		linked, err := g.managed(ctx, domain.EntityChoiceGroup, updated.GroupID)
		if err != nil || !linked {
			return err
		}
		return managedError([]string{domain.LockedChoices})
	case updated == nil:
		linked, err := g.managed(ctx, domain.EntityChoice, current.ID)
		if err != nil || !linked {
			return err
		}
		return managedError([]string{domain.LockedChoices})
	}
	linked, err := g.managed(ctx, domain.EntityChoice, current.ID)
	if err != nil || !linked {
		return err
	}
	var fields []string
	if !current.PriceModifier.Equal(updated.PriceModifier) {
		fields = append(fields, domain.LockedPriceMod)
	}
	if current.GroupID != updated.GroupID || current.Sort != updated.Sort {
		fields = append(fields, domain.LockedSort)
	}
	if nameChanged(conn, current.Names, updated.Names) {
		fields = append(fields, domain.LockedName)
	}
	return managedError(fields)
}

// nameChanged compares the name in the catalog language, the one HubRise writes. The other
// languages are the shop's.
func nameChanged(conn *domain.Connection, before, after map[string]string) bool {
	lang := conn.CatalogLanguage
	return strings.TrimSpace(before[lang]) != strings.TrimSpace(after[lang])
}
