package resolver

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"tsb-service/internal/api/graphql/model"
	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	hubriseDomain "tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/db"
)

// The helpers behind the catalog publishing operations of hubrise.graphql (the dashboard-owned menu).

// maxPreviewChanges caps the changes a preview lists; removals come first.
const maxPreviewChanges = 500

func (r *Resolver) setHubriseCatalogDirection(ctx context.Context, to model.HubriseCatalogDirection, acknowledge bool) (*model.HubriseStatus, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	if _, err := admin.SetCatalogDirection(ctx, hubriseDomain.CatalogDirection(to), acknowledge); err != nil {
		return nil, hubriseFail(ctx, err)
	}
	return r.hubriseStatus(ctx)
}

func (r *Resolver) acknowledgeHubriseExternalChange(ctx context.Context) (*model.HubriseStatus, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	if err := admin.AcknowledgeExternalChange(ctx); err != nil {
		return nil, hubriseFail(ctx, err)
	}
	return r.hubriseStatus(ctx)
}

func (r *Resolver) previewHubriseCatalogPublish(ctx context.Context, removeForeign bool) (*model.HubrisePublishPreview, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	prev, err := admin.PreviewPublish(ctx, removeForeign)
	if err != nil {
		return nil, hubriseFail(ctx, err)
	}
	return toHubrisePublishPreview(prev), nil
}

func (r *Resolver) publishHubriseCatalog(ctx context.Context, confirmRemovals, removeForeign bool, planHash *string) (*model.HubrisePublishResult, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	req := hubriseApplication.PublishRequest{ConfirmRemovals: confirmRemovals, RemoveForeign: removeForeign}
	if planHash != nil {
		req.PlanHash = strings.TrimSpace(*planHash)
	}
	out, err := admin.PublishCatalog(ctx, req)
	if err != nil {
		return nil, hubriseFail(ctx, err)
	}
	st, err := admin.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &model.HubrisePublishResult{Published: out.Published, Queued: out.Queued, Status: toHubrisePublishStatus(st.Publish)}, nil
}

func toHubriseDirection(d hubriseDomain.CatalogDirection) model.HubriseCatalogDirection {
	if d == "" {
		return model.HubriseCatalogDirectionNone
	}
	return model.HubriseCatalogDirection(d)
}

func toHubrisePublishStatus(s *hubriseApplication.PublishStatus) *model.HubrisePublishStatus {
	if s == nil {
		s = &hubriseApplication.PublishStatus{}
	}
	out := &model.HubrisePublishStatus{
		Enabled: s.Enabled, Active: s.Active, Pending: s.Pending, PendingSince: s.PendingSince,
		NextPublishAt: s.NextPublishAt, Publishing: s.Publishing, LastPublishedAt: s.LastPublishedAt,
		FirstPublishConfirmedAt: s.FirstPublishConfirmedAt, AwaitingFirstPublish: s.AwaitingFirstPublish,
		LastError: s.LastError, LastErrorAt: s.LastErrorAt, ExternalChangeAt: s.ExternalChangeAt,
		NeedsReconnectForWrite: s.NeedsReconnectForWrite, AccountCatalogDetected: s.AccountCatalogDetected,
		PublishImages: s.PublishImages, PublishedProducts: s.PublishedProducts, PendingProducts: s.PendingProducts,
	}
	if s.LastErrorCode != "" {
		code := model.HubrisePublishErrorCode(s.LastErrorCode)
		out.LastErrorCode = &code
	}
	return out
}

func toHubriseEntity(e hubriseDomain.EntityType) model.HubriseCatalogEntity {
	return model.HubriseCatalogEntity(strings.ToUpper(string(e)))
}

func toHubriseIssues(in []hubriseDomain.Warning) []*model.HubriseCatalogIssue {
	out := make([]*model.HubriseCatalogIssue, 0, len(in))
	for _, w := range in {
		out = append(out, &model.HubriseCatalogIssue{Code: string(w.Code), Key: optional(w.Key), Message: w.Message})
	}
	return out
}

func toHubrisePublishPreview(prev *hubriseApplication.PublishPreview) *model.HubrisePublishPreview {
	plan := prev.Plan
	out := &model.HubrisePublishPreview{
		PlanHash: plan.PlanHash, FirstPublish: prev.FirstPublish,
		Added:     plan.Count(hubriseApplication.ActionPublishAdd),
		Updated:   plan.Count(hubriseApplication.ActionPublishUpdate),
		Removed:   plan.Count(hubriseApplication.ActionPublishRemove),
		Unchanged: plan.Unchanged, Foreign: len(plan.Foreign),
		Counts: []*model.HubrisePublishCount{}, Changes: []*model.HubrisePublishChange{},
		ForeignItems: []*model.HubrisePublishForeignItem{}, Preserved: plan.Preserved,
		Warnings: toHubriseIssues(plan.Warnings), Blockers: toHubriseIssues(plan.Blockers),
		RemovalsNeedConfirm: plan.RemovalsNeedConfirm, GeneratedAt: prev.GeneratedAt,
	}
	if out.Preserved == nil {
		out.Preserved = []string{}
	}

	perEntity := map[hubriseDomain.EntityType]*model.HubrisePublishCount{}
	var entities []hubriseDomain.EntityType
	for _, c := range plan.Changes {
		n, ok := perEntity[c.Entity]
		if !ok {
			n = &model.HubrisePublishCount{Entity: toHubriseEntity(c.Entity)}
			perEntity[c.Entity] = n
			entities = append(entities, c.Entity)
		}
		switch c.Action {
		case hubriseApplication.ActionPublishAdd:
			n.Added++
		case hubriseApplication.ActionPublishUpdate:
			n.Updated++
		case hubriseApplication.ActionPublishRemove:
			n.Removed++
		}
	}
	sort.SliceStable(entities, func(i, j int) bool { return entityRank(entities[i]) < entityRank(entities[j]) })
	for _, e := range entities {
		out.Counts = append(out.Counts, perEntity[e])
	}

	// Removals first, then additions, then updates; the order inside an action is the plan's.
	changes := append([]hubriseApplication.PublishChange(nil), plan.Changes...)
	sort.SliceStable(changes, func(i, j int) bool { return actionRank(changes[i].Action) < actionRank(changes[j].Action) })
	if len(changes) > maxPreviewChanges {
		changes, out.Truncated = changes[:maxPreviewChanges], true
	}
	for _, c := range changes {
		fields := c.Fields
		if fields == nil {
			fields = []string{}
		}
		out.Changes = append(out.Changes, &model.HubrisePublishChange{
			Entity: toHubriseEntity(c.Entity), Action: model.HubrisePublishAction(c.Action),
			Key: c.Key, Ref: c.Ref, Name: c.Name, Fields: fields,
		})
	}
	for _, f := range plan.Foreign {
		out.ForeignItems = append(out.ForeignItems, &model.HubrisePublishForeignItem{Entity: toHubriseEntity(f.Entity), Ref: f.Ref, Name: f.Name})
	}
	return out
}

func entityRank(e hubriseDomain.EntityType) int {
	switch e {
	case hubriseDomain.EntityCategory:
		return 0
	case hubriseDomain.EntityProduct:
		return 1
	case hubriseDomain.EntityChoiceGroup:
		return 2
	}
	return 3
}

func actionRank(a hubriseApplication.PublishAction) int {
	switch a {
	case hubriseApplication.ActionPublishRemove:
		return 0
	case hubriseApplication.ActionPublishAdd:
		return 1
	}
	return 2
}

// ---- Product.hubrisePublish ----

type hubrisePublishLoaderKey struct{}

// publishEntry is one product's publish info; ok is false outside PUBLISH mode.
type publishEntry struct {
	info hubriseApplication.ProductPublishInfo
	ok   bool
}

type hubrisePublishLoader struct {
	loader *db.TypedLoader[publishEntry]
}

// attachPublishLoader puts the DataLoader behind Product.hubrisePublish in the request context, so a
// list of products costs one read of the menu.
func attachPublishLoader(ctx context.Context, admin *hubriseApplication.Admin) context.Context {
	loader := db.NewTypedLoader[publishEntry](func(ctx context.Context, keys []string) (map[string][]publishEntry, error) {
		ids := make([]uuid.UUID, 0, len(keys))
		for _, k := range keys {
			if id, err := uuid.Parse(k); err == nil {
				ids = append(ids, id)
			}
		}
		infos, err := admin.ProductPublishInfos(ctx, ids)
		if err != nil {
			return nil, err
		}
		out := make(map[string][]publishEntry, len(keys))
		for _, k := range keys {
			id, _ := uuid.Parse(k)
			info, ok := infos[id]
			out[k] = []publishEntry{{info: info, ok: ok}}
		}
		return out, nil
	}, "failed to read the HubRise publish state")
	return context.WithValue(ctx, hubrisePublishLoaderKey{}, &hubrisePublishLoader{loader: loader})
}

// hubrisePublishInfo is Product.hubrisePublish: nil unless the dashboard owns the menu.
func (r *Resolver) hubrisePublishInfo(ctx context.Context, id uuid.UUID) (*model.HubriseProductPublishInfo, error) {
	if r.HubriseAdmin == nil {
		return nil, nil //nolint:nilnil // no HubRise is not an error
	}
	var entry publishEntry
	if l, ok := ctx.Value(hubrisePublishLoaderKey{}).(*hubrisePublishLoader); ok {
		res, err := l.loader.Load(ctx, id.String())
		if err != nil || len(res) == 0 {
			return nil, err
		}
		entry = res[0]
	} else {
		infos, err := r.HubriseAdmin.ProductPublishInfos(ctx, []uuid.UUID{id})
		if err != nil {
			return nil, err
		}
		entry.info, entry.ok = infos[id]
	}
	if !entry.ok {
		return nil, nil
	}
	out := &model.HubriseProductPublishInfo{State: model.HubrisePublishState(entry.info.State), PublishedAt: entry.info.PublishedAt}
	if entry.info.Ref != "" {
		ref := entry.info.Ref
		out.Ref = &ref
	}
	return out, nil
}
