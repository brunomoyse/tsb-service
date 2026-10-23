package resolver

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	hubriseDomain "tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/db"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"

	"go.uber.org/zap"
)

// The helpers behind the hubrise* operations of hubrise.graphql, the HubRise fields of Product,
// Order and ProductChoice, and the mapping of the module's errors.

// hubriseAdmin returns the connection management, or HUBRISE_NOT_CONFIGURED on a deployment
// without HubRise.
func (r *Resolver) hubriseAdmin() (*hubriseApplication.Admin, error) {
	if r.HubriseAdmin == nil {
		return nil, apperr.New(apperr.CodeHubriseNotConfigured, "HubRise is not configured on this server")
	}
	return r.HubriseAdmin, nil
}

// hubriseFail maps an error of the HubRise module for the response. A cause that is not the
// operator's doing is logged here (the presenter then reports it).
func hubriseFail(ctx context.Context, err error) error {
	mapped := HubriseError(err)
	if app, ok := apperr.From(mapped); ok && app.Code == apperr.CodeHubriseUpstream {
		logging.FromContext(ctx).Warn("hubrise: upstream call failed", zap.Error(err))
	}
	return mapped
}

// ---- status ----

// hubriseStatus reads the status; available is false on a deployment without HubRise.
func (r *Resolver) hubriseStatus(ctx context.Context) (*model.HubriseStatus, error) {
	if r.HubriseAdmin == nil {
		return toHubriseStatus(nil, false), nil
	}
	st, err := r.HubriseAdmin.Status(ctx)
	if err != nil {
		return nil, err
	}
	return toHubriseStatus(st, true), nil
}

func toHubriseStatus(st *hubriseApplication.Status, available bool) *model.HubriseStatus {
	out := &model.HubriseStatus{
		Available: available, CatalogLanguage: hubriseDomain.LanguageFR,
		Features: &model.HubriseFeatures{}, Backlog: &model.HubriseBacklog{},
	}
	if st == nil {
		return out
	}
	out.Backlog = &model.HubriseBacklog{Pending: st.Backlog.Pending, Parked: st.Backlog.Parked}
	out.UnlinkedProducts, out.Attention = st.UnlinkedProducts, st.Attention
	c := st.Connection
	if c == nil {
		return out
	}
	out.Connected, out.Enabled = c.Connected(), c.Enabled
	out.AccountID, out.AccountName = c.AccountID, c.AccountName
	out.LocationID, out.LocationName = c.LocationID, c.LocationName
	out.CatalogID, out.CatalogName = c.CatalogID, c.CatalogName
	out.CustomerListID, out.CustomerListName = c.CustomerListID, c.CustomerListName
	out.Features = &model.HubriseFeatures{
		PushOrders: c.PushOrders, SyncStatus: c.SyncStatus, SyncInventory: c.SyncInventory, SyncCatalog: c.SyncCatalog,
	}
	out.SyncCustomers, out.CatalogAutoImport = c.SyncCustomers, c.CatalogAutoImport
	out.CatalogUpdateAvailable = c.CatalogUpdateAvailableAt != nil
	out.CatalogPendingReview = c.CatalogPendingReview()
	out.AutoRefundOnCancel = c.AutoRefundOnCancel
	out.CatalogLanguage = c.CatalogLanguage
	out.VariantRef, out.ChannelName = c.VariantRef, c.ChannelName
	out.DeliveryChargeRef, out.PaymentFeeChargeRef = c.DeliveryChargeRef, c.PaymentFeeChargeRef
	out.ImportImages = c.ImportImages
	out.LastCatalogSyncAt, out.LastEventAt, out.AuthErrorAt = c.LastCatalogSyncAt, c.LastEventAt, c.AuthErrorAt
	if c.LastError != nil {
		msg := strings.TrimPrefix(*c.LastError, hubriseDomain.PendingReviewPrefix)
		out.LastError = &msg
	}
	out.RelinkRequired = c.RelinkRequiredAt != nil
	return out
}

// ---- connection ----

func (r *Resolver) startHubriseConnection(ctx context.Context) (string, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return "", err
	}
	adminID, err := uuid.Parse(utils.GetUserID(ctx))
	if err != nil {
		return "", apperr.New(apperr.CodeForbidden, "the connection must be started by an admin account")
	}
	url, err := admin.StartConnection(ctx, adminID)
	return url, hubriseFail(ctx, err)
}

func (r *Resolver) disconnectHubrise(ctx context.Context, forgetLinks bool) (*model.HubriseStatus, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	if err := admin.Disconnect(ctx, forgetLinks); err != nil {
		return nil, hubriseFail(ctx, err)
	}
	return r.hubriseStatus(ctx)
}

func (r *Resolver) updateHubriseSettings(ctx context.Context, in model.HubriseSettingsInput) (*model.HubriseStatus, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	_, err = admin.UpdateSettings(ctx, hubriseDomain.SettingsPatch{
		Enabled: in.Enabled, PushOrders: in.PushOrders, SyncStatus: in.SyncStatus, SyncInventory: in.SyncInventory,
		SyncCatalog: in.SyncCatalog, SyncCustomers: in.SyncCustomers, CatalogAutoImport: in.CatalogAutoImport,
		AutoRefundOnCancel: in.AutoRefundOnCancel, ImportImages: in.ImportImages, CatalogLanguage: in.CatalogLanguage,
		VariantRef: in.VariantRef, ChannelName: in.ChannelName,
		DeliveryChargeRef: in.DeliveryChargeRef, PaymentFeeChargeRef: in.PaymentFeeChargeRef,
	})
	if err != nil {
		return nil, hubriseFail(ctx, err)
	}
	return r.hubriseStatus(ctx)
}

// ---- catalog and inventory ----

func (r *Resolver) previewHubriseCatalogSync(ctx context.Context, mode model.HubriseSyncMode) (*model.HubriseCatalogDiff, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	res, err := admin.PreviewCatalog(ctx, hubriseDomain.SyncMode(mode))
	if err != nil {
		return nil, hubriseFail(ctx, err)
	}
	return toHubriseDiff(res.Diff), nil
}

func (r *Resolver) syncHubriseCatalog(ctx context.Context, mode model.HubriseSyncMode, confirmRetire bool) (*model.HubriseSyncResult, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	res, err := admin.SyncCatalog(ctx, hubriseDomain.SyncMode(mode), confirmRetire)
	if err != nil {
		return nil, hubriseFail(ctx, err)
	}
	out := &model.HubriseSyncResult{
		Mode: mode, Applied: res.Applied, PendingReview: res.PendingReview,
		Diff: toHubriseDiff(res.Diff), FailedBatches: []string{},
	}
	if res.Result != nil {
		for _, f := range res.Result.Failed {
			out.FailedBatches = append(out.FailedBatches, f.Batch)
		}
	}
	return out, nil
}

func toHubriseDiff(d *hubriseDomain.Diff) *model.HubriseCatalogDiff {
	out := &model.HubriseCatalogDiff{
		CatalogHash: d.CatalogHash, Changes: make([]*model.HubriseCatalogChange, 0, len(d.Changes)),
		Warnings: []*model.HubriseCatalogIssue{}, Conflicts: []*model.HubriseCatalogIssue{},
		Unchanged: d.Unchanged, RetiringProducts: d.RetiringProducts, LinkedProducts: d.LinkedProducts,
		RetireNeedsConfirm: d.RetireNeedsConfirm,
	}
	for i := range d.Changes {
		c := &d.Changes[i]
		fields := c.Fields
		if fields == nil {
			fields = []string{}
		}
		out.Changes = append(out.Changes, &model.HubriseCatalogChange{
			Entity: model.HubriseCatalogEntity(strings.ToUpper(string(c.Entity))),
			Action: model.HubriseCatalogAction(strings.ToUpper(string(c.Action))),
			Key:    c.Key, Name: c.Name, Fields: fields,
		})
	}
	for _, w := range d.Warnings {
		out.Warnings = append(out.Warnings, &model.HubriseCatalogIssue{Code: string(w.Code), Key: optional(w.Key), Message: w.Message})
	}
	for _, c := range d.Conflicts {
		msg := c.Reason
		if len(c.Candidates) > 0 {
			msg += " (" + strings.Join(c.Candidates, ", ") + ")"
		}
		out.Conflicts = append(out.Conflicts, &model.HubriseCatalogIssue{Code: string(c.Entity), Key: optional(c.Key), Message: c.Name + ": " + msg})
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Resolver) forgetHubriseLinks(ctx context.Context) (int, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return 0, err
	}
	n, err := admin.ForgetLinks(ctx)
	return int(n), hubriseFail(ctx, err)
}

func (r *Resolver) syncHubriseInventory(ctx context.Context) (int, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return 0, err
	}
	n, err := admin.SyncInventory(ctx)
	return n, hubriseFail(ctx, err)
}

func (r *Resolver) linkHubriseProduct(ctx context.Context, productID uuid.UUID, skuRef *string) (bool, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return false, err
	}
	p, err := r.ProductService.GetProduct(ctx, productID)
	if (err != nil && errors.Is(err, sql.ErrNoRows)) || (err == nil && p == nil) {
		return false, apperr.New(apperr.CodeProductNotFound, "the product does not exist").With("productId", productID.String())
	}
	if err != nil {
		return false, err
	}
	if err := admin.LinkProduct(ctx, productID, skuRef); err != nil {
		return false, hubriseFail(ctx, err)
	}
	return true, nil
}

// ---- outbox ----

func (r *Resolver) hubriseOutbox(ctx context.Context, state *model.HubriseOutboxState, first *int) ([]*model.HubriseOutboxItem, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return nil, err
	}
	var s *hubriseDomain.OutboxState
	if state != nil {
		v := hubriseDomain.OutboxState(strings.ToLower(string(*state)))
		s = &v
	}
	n := 0
	if first != nil {
		n = *first
	}
	items, err := admin.Outbox(ctx, s, n)
	if err != nil {
		return nil, err
	}
	out := make([]*model.HubriseOutboxItem, len(items))
	for i := range items {
		it := &items[i]
		out[i] = &model.HubriseOutboxItem{
			ID: strconv.FormatInt(it.ID, 10), Kind: model.HubriseOutboxKind(strings.ToUpper(string(it.Kind))),
			OrderID: it.OrderID, State: model.HubriseOutboxState(strings.ToUpper(string(it.State))),
			Attempts: it.Attempts, LastError: it.LastError, NextAttemptAt: it.NextAttemptAt,
			CreatedAt: it.CreatedAt, SentAt: it.SentAt, ParkedAt: it.ParkedAt,
		}
	}
	return out, nil
}

func (r *Resolver) retryHubriseOutbox(ctx context.Context, id *string) (int, error) {
	admin, err := r.hubriseAdmin()
	if err != nil {
		return 0, err
	}
	var rowID *int64
	if id != nil {
		v, perr := strconv.ParseInt(strings.TrimSpace(*id), 10, 64)
		if perr != nil || v <= 0 {
			return 0, apperr.New(apperr.CodeUserError, "the outbox id is not a number").With("field", "id")
		}
		rowID = &v
	}
	n, err := admin.RetryOutbox(ctx, rowID)
	return int(n), hubriseFail(ctx, err)
}

// ---- fields of Product and Order ----

type hubriseManagedLoaderKey struct{}

type hubriseManagedLoader struct {
	loader *db.TypedLoader[bool]
}

// AttachHubriseLoaders puts the DataLoader behind Product.hubriseManaged in the request context, so a
// list of products costs one query. Without HubRise it does nothing.
func AttachHubriseLoaders(ctx context.Context, r *Resolver) context.Context {
	if r.HubriseAdmin == nil {
		return ctx
	}
	admin := r.HubriseAdmin
	loader := db.NewTypedLoader[bool](func(ctx context.Context, keys []string) (map[string][]bool, error) {
		ids := make([]uuid.UUID, 0, len(keys))
		for _, k := range keys {
			if id, err := uuid.Parse(k); err == nil {
				ids = append(ids, id)
			}
		}
		managed, err := admin.ManagedProducts(ctx, ids)
		if err != nil {
			return nil, err
		}
		out := make(map[string][]bool, len(keys))
		for _, k := range keys {
			id, _ := uuid.Parse(k)
			out[k] = []bool{managed[id]}
		}
		return out, nil
	}, "failed to read the HubRise links")
	return context.WithValue(ctx, hubriseManagedLoaderKey{}, &hubriseManagedLoader{loader: loader})
}

// hubriseManaged says whether HubRise owns the product, through the request's loader when there is
// one.
func (r *Resolver) hubriseManaged(ctx context.Context, id uuid.UUID) (bool, error) {
	if r.HubriseAdmin == nil {
		return false, nil
	}
	if l, ok := ctx.Value(hubriseManagedLoaderKey{}).(*hubriseManagedLoader); ok {
		res, err := l.loader.Load(ctx, id.String())
		if err != nil || len(res) == 0 {
			return false, err
		}
		return res[0], nil
	}
	managed, err := r.HubriseAdmin.ManagedProducts(ctx, []uuid.UUID{id})
	return managed[id], err
}

// hubriseLockedFields lists the fields of a managed product the dashboard must not offer.
func hubriseLockedFields(managed bool, p *model.Product) []string {
	if !managed {
		return []string{}
	}
	fields := []string{hubriseDomain.LockedPrice, hubriseDomain.LockedCategory, hubriseDomain.LockedVatCategory}
	if p.Code != nil && *p.Code != "" { // a code is locked once the product has one
		fields = append(fields, hubriseDomain.LockedCode)
	}
	return append(fields, hubriseDomain.LockedChoiceGroups)
}

func (r *Resolver) hubriseOrderInfo(ctx context.Context, orderID uuid.UUID) (*model.HubriseOrderInfo, error) {
	if r.HubriseAdmin == nil {
		return nil, nil //nolint:nilnil // no HubRise side is not an error
	}
	info, err := r.HubriseAdmin.Order(ctx, orderID)
	if err != nil || info == nil {
		return nil, err
	}
	return &model.HubriseOrderInfo{
		HubriseOrderID: info.HubriseOrderID, PushState: string(info.PushState), PushedAt: info.PushedAt,
		RemoteStatus: info.LastRemoteStatus, RemoteTotal: info.RemoteTotal, NeedsAttention: info.NeedsAttention,
	}, nil
}

// ---- errors ----

// hubriseUpstream is the operator-facing error of a failed HubRise call.
func hubriseUpstream(he *pkghubrise.Error) error {
	e := apperr.Newf(apperr.CodeHubriseUpstream, "HubRise did not accept the request (%s)", he.Kind).With("kind", he.Kind.String())
	if he.Status != 0 {
		e = e.With("status", he.Status)
	}
	return e
}
