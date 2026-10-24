package resolver

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/apperr"
	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	hubriseDomain "tsb-service/internal/modules/hubrise/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// CatalogGuard is the ownership guard of the HubRise module as the resolvers use it: it compares the
// neutral field sets of a product, choice group or choice before and after an edit.
type CatalogGuard interface {
	CheckProduct(ctx context.Context, current, updated hubriseDomain.ProductFields) error
	CheckChoiceGroup(ctx context.Context, current, updated *hubriseDomain.GroupFields) error
	CheckChoice(ctx context.Context, current, updated *hubriseDomain.ChoiceFields) error
}

// CatalogGuardHooks is the part of HubriseHooks that casts the catalog vetoes. The other hooks do
// nothing; the real HubRise hooks embed it, or implement the guards by calling it.
type CatalogGuardHooks struct {
	NoopHubriseHooks
	Guard CatalogGuard
}

var _ HubriseHooks = CatalogGuardHooks{}

// ProductGuard refuses an edit of the HubRise-owned fields of a linked product.
func (h CatalogGuardHooks) ProductGuard(ctx context.Context, current, updated *productDomain.Product) error {
	return HubriseError(h.Guard.CheckProduct(ctx, productFields(current), productFields(updated)))
}

// ChoiceGroupGuard refuses to create, edit or delete a choice group HubRise owns.
func (h CatalogGuardHooks) ChoiceGroupGuard(ctx context.Context, current, updated *productDomain.ProductChoiceGroup) error {
	return HubriseError(h.Guard.CheckChoiceGroup(ctx, groupFields(current, nil), groupFields(updated, current)))
}

// ChoiceGuard refuses to create, edit or delete a choice HubRise owns.
func (h CatalogGuardHooks) ChoiceGuard(ctx context.Context, current, updated *productDomain.ProductChoice) error {
	return HubriseError(h.Guard.CheckChoice(ctx, choiceFields(current, nil), choiceFields(updated, current)))
}

func productFields(p *productDomain.Product) hubriseDomain.ProductFields {
	f := hubriseDomain.ProductFields{ID: p.ID, CategoryID: p.CategoryID, Price: p.Price, VatCategory: string(p.VatCategory)}
	if p.Code != nil {
		f.Code = *p.Code
	}
	return f
}

// choiceNames are the translations of a choice or group by language. A save that names only some
// languages leaves the others as they are, so the names after an edit are the current ones with the
// edit laid over them: base is the row as it was (nil for the "before" side itself).
func choiceNames(own []productDomain.ChoiceTranslation, base []productDomain.ChoiceTranslation) map[string]string {
	names := map[string]string{}
	for _, t := range base {
		names[t.Locale] = t.Name
	}
	for _, t := range own {
		names[t.Locale] = t.Name
	}
	return names
}

// groupFields converts one side of a group change; self may be nil (a creation has no current
// group, a deletion no updated one), and the result is then nil too. base is the group as it was,
// under the names of an edited group.
func groupFields(self, base *productDomain.ProductChoiceGroup) *hubriseDomain.GroupFields {
	if self == nil {
		return nil
	}
	var baseNames []productDomain.ChoiceTranslation
	if base != nil {
		baseNames = base.Translations
	}
	return &hubriseDomain.GroupFields{ID: self.ID, ProductID: self.ProductID, Min: self.MinSelections, Max: self.MaxSelections,
		Sort: self.SortOrder, Names: choiceNames(self.Translations, baseNames)}
}

func choiceFields(self, base *productDomain.ProductChoice) *hubriseDomain.ChoiceFields {
	if self == nil {
		return nil
	}
	var baseNames []productDomain.ChoiceTranslation
	if base != nil {
		baseNames = base.Translations
	}
	return &hubriseDomain.ChoiceFields{ID: self.ID, ProductID: self.ProductID, GroupID: self.ChoiceGroupID, PriceModifier: self.PriceModifier,
		Sort: self.SortOrder, Names: choiceNames(self.Translations, baseNames)}
}

// HubriseError turns the refusals of the HubRise module into the GraphQL errors the dashboard shows.
// Any other error is returned as it is.
func HubriseError(err error) error {
	if err == nil {
		return nil
	}
	if managed, ok := errors.AsType[*hubriseDomain.ManagedError](err); ok {
		return apperr.Newf(apperr.CodeHubriseManaged, "%s is managed by HubRise and cannot be edited here", strings.Join(managed.Fields, ", ")).
			With("fields", managed.Fields)
	}
	if confirm, ok := errors.AsType[*hubriseDomain.RetireConfirmError](err); ok {
		return apperr.New(apperr.CodeHubriseRetireConfirmRequired, confirm.Error()).
			With("retiring", confirm.Retiring).With("linked", confirm.Linked)
	}
	if invalid, ok := errors.AsType[*hubriseDomain.InvalidSettingError](err); ok {
		return apperr.New(apperr.CodeUserError, invalid.Message).With("field", invalid.Field)
	}
	if blocked, ok := errors.AsType[*hubriseDomain.PublishBlockedError](err); ok {
		codes := make([]string, 0, len(blocked.Blockers))
		for _, b := range blocked.Blockers {
			codes = append(codes, string(b.Code))
		}
		return apperr.New(apperr.CodeHubrisePublishBlocked, blocked.Error()).With("blockers", codes)
	}
	if he, ok := errors.AsType[*pkghubrise.Error](err); ok {
		if he.Kind == pkghubrise.KindRateLimited {
			return apperr.New(apperr.CodeHubriseRateLimited, "HubRise's read limit is reached, try again in a moment").
				With("retryAfterSeconds", int(math.Ceil(he.RetryAfter.Seconds())))
		}
		return hubriseUpstream(he)
	}
	switch {
	case errors.Is(err, hubriseDomain.ErrRelinkRequired):
		return apperr.New(apperr.CodeHubriseRelinkRequired, err.Error())
	case errors.Is(err, hubriseDomain.ErrPreviewRequired):
		return apperr.New(apperr.CodeHubrisePreviewRequired, err.Error())
	case errors.Is(err, hubriseDomain.ErrNotConnected):
		return apperr.New(apperr.CodeHubriseNotConnected, "HubRise is not connected")
	case errors.Is(err, hubriseDomain.ErrNotEnabled):
		return apperr.New(apperr.CodeHubriseNotEnabled, "the HubRise integration is not enabled")
	case errors.Is(err, hubriseDomain.ErrEmptyCatalog):
		return apperr.New(apperr.CodeHubriseEmptyCatalog, "the HubRise catalog has no product")
	case errors.Is(err, hubriseDomain.ErrImportInProgress), errors.Is(err, hubriseApplication.ErrInventoryBusy):
		return apperr.New(apperr.CodeHubriseBusy, "another HubRise synchronization is running, try again in a moment")
	case errors.Is(err, hubriseDomain.ErrPublishMode):
		return apperr.New(apperr.CodeHubrisePublishMode, "the dashboard owns the menu, a full import from HubRise is not available")
	case errors.Is(err, hubriseDomain.ErrDirectionConflict):
		return apperr.New(apperr.CodeHubriseDirectionConflict, "the dashboard owns the menu: switch the catalog direction before turning catalog sync on")
	case errors.Is(err, hubriseDomain.ErrReconnectRequired):
		return apperr.New(apperr.CodeHubriseReconnectRequired, "reconnect HubRise to grant the catalog write permission")
	case errors.Is(err, hubriseDomain.ErrAccountCatalog):
		return apperr.New(apperr.CodeHubriseAccountCatalog, err.Error())
	case errors.Is(err, hubriseDomain.ErrNotPublishMode):
		return apperr.New(apperr.CodeHubriseNotPublishMode, "the dashboard does not own the menu")
	case errors.Is(err, hubriseDomain.ErrPreviewStale):
		return apperr.New(apperr.CodeHubrisePreviewStale, err.Error())
	case errors.Is(err, hubriseDomain.ErrPublishConfirmRequired):
		return apperr.New(apperr.CodeHubrisePublishConfirmRequired, err.Error())
	case errors.Is(err, hubriseDomain.ErrPublishPendingChanges):
		return apperr.New(apperr.CodeHubrisePublishPendingChanges, err.Error())
	case errors.Is(err, hubriseDomain.ErrEmptyMenu):
		return apperr.New(apperr.CodeHubriseEmptyMenu, "the menu has no product, nothing is published")
	case errors.Is(err, hubriseDomain.ErrPublishBusy):
		return apperr.New(apperr.CodeHubriseBusy, "a catalog publish is already running, try again in a moment")
	case errors.Is(err, hubriseDomain.ErrSKULinked):
		return apperr.New(apperr.CodeHubriseSKULinked, "that HubRise SKU is already linked to another product")
	}
	return err
}

// ProductsChanged publishes productUpdated for the products an import changed, so the open
// dashboards refresh. It satisfies the HubRise catalog service's notifier port.
func (r *Resolver) ProductsChanged(ctx context.Context, ids []uuid.UUID) {
	lang := utils.GetLang(ctx)
	for _, id := range ids {
		p, err := r.ProductService.GetProduct(ctx, id)
		if err != nil {
			logging.FromContext(ctx).Warn("hubrise: publish product update", zap.String("product_id", id.String()), zap.Error(err))
			continue
		}
		r.Broker.Publish("productUpdated", ToGQLProduct(p, lang))
	}
}
