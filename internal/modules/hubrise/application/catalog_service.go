package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

const (
	// catalogLease is the job lease that keeps two instances from importing at the same time (the
	// batches also take an advisory lock; the lease protects the whole read, plan, apply sequence).
	catalogLease    = "catalog_import"
	catalogLeaseTTL = 15 * time.Minute
	// previewMaxAge is how fresh the dry run must be to enable catalog sync.
	previewMaxAge = 24 * time.Hour
)

// CatalogService is the use-case API of the HubRise catalog: preview, import, drift detection, the
// safety rules around turning catalog sync on, and the manual links. The GraphQL layer and the
// workers only call it.
type CatalogService struct {
	Connections domain.ConnectionRepository
	Links       domain.CatalogLinkRepository
	Leases      domain.LeaseRepository
	Local       domain.LocalCatalogRepository
	Clock       domain.Clock
	// NewClient builds the API client of the connection (ClientFactory.CatalogAPI in production).
	NewClient func(*domain.Connection) (CatalogAPI, error)
	// Notifier and Images are optional: without them nothing is published and no image imported.
	Notifier CatalogNotifier
	Images   ImageUploader
	// Holder identifies this instance in the import lease.
	Holder string
	// Publish, Published and PublishState are the catalog publisher's side, all optional: the drift
	// check of a published catalog, and the forgetting of what was published.
	Publish      PublishWatcher
	PublishItems domain.PublishedItemRepository
	PublishState domain.PublishStateRepository
}

// PublishWatcher is the catalog publisher seen by the drift check and the callback poller.
type PublishWatcher interface {
	// CheckRemoteDrift compares the HubRise catalog with what the last publish left there.
	CheckRemoteDrift(ctx context.Context) error
	// NoteExternalChange records an edit made at HubRise; deleted says the catalog was deleted.
	NoteExternalChange(ctx context.Context, deleted bool, firstEventAt time.Time) error
}

// SyncRequest asks for an import.
type SyncRequest struct {
	Mode domain.SyncMode
	// ConfirmRetire accepts a mass retirement (see domain.TripsMassRetire).
	ConfirmRetire bool
	// Scheduled marks an import started by a worker: it counts as background traffic for the rate
	// limiter and never carries a confirmation, so a mass retirement waits for a human.
	Scheduled bool
	// PlanOnly plans the mode and returns the diff without writing anything and without recording
	// a preview: what Preview does for LINK_ONLY.
	PlanOnly bool
}

// SyncResult is what an import did (or, for a dry run, would do).
type SyncResult struct {
	Mode domain.SyncMode
	Diff *domain.Diff
	// Applied is false for a dry run and for an import that waits for confirmation.
	Applied bool
	Result  *ApplyResult
	// PendingReview is true when a scheduled import stopped at the mass-retire threshold.
	PendingReview bool
	// Warnings are the mapping warnings plus the images that failed.
	Warnings []domain.Warning
}

func (s *CatalogService) now() time.Time { return s.Clock.Now() }

// connection loads the connection and requires a token.
func (s *CatalogService) connection(ctx context.Context) (*domain.Connection, error) {
	conn, err := s.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.ErrNotConnected
	}
	if err != nil {
		return nil, err
	}
	if !conn.Connected() {
		return nil, domain.ErrNotConnected
	}
	return conn, nil
}

// fetch downloads the catalog of the connection and reduces it. An empty catalog is an error: it
// must never be mistaken for "retire everything".
func (s *CatalogService) fetch(ctx context.Context, conn *domain.Connection, scheduled bool) (CatalogAPI, *domain.Catalog, error) {
	if conn.CatalogID == nil || *conn.CatalogID == "" {
		return nil, nil, fmt.Errorf("hubrise: the location has no catalog: %w", domain.ErrNotConnected)
	}
	api, err := s.NewClient(conn)
	if err != nil {
		return nil, nil, err
	}
	if scheduled {
		ctx = hubrise.Background(ctx)
	}
	raw, err := api.GetCatalog(ctx, *conn.CatalogID)
	if err != nil {
		s.noteAuthError(ctx, err)
		return nil, nil, err
	}
	variant := ""
	if conn.VariantRef != nil {
		variant = *conn.VariantRef
	}
	cat := MapCatalog(raw, MapOptions{Language: conn.CatalogLanguage, VariantRef: variant})
	if len(cat.Products) == 0 {
		return nil, nil, domain.ErrEmptyCatalog
	}
	return api, cat, nil
}

// noteAuthError pauses the pipeline on a revoked or forbidden token, like every other HubRise call.
func (s *CatalogService) noteAuthError(ctx context.Context, err error) {
	if !errors.Is(err, hubrise.KindUnauthorized) && !errors.Is(err, hubrise.KindForbidden) {
		return
	}
	if rerr := s.Connections.RecordAuthError(context.WithoutCancel(ctx), s.now(), err.Error()); rerr != nil {
		logging.FromContext(ctx).Error("hubrise: record auth error", zap.Error(rerr))
	}
}

// Sync reads the catalog, plans against the local menu and, unless it is a dry run, applies the plan.
//
//   - A dry run writes nothing and records the catalog hash as previewed (the condition for enabling
//     catalog sync).
//   - LINK_ONLY records links for the entries that match existing rows and touches no product data.
//   - FULL adopts, creates, updates and retires. Retiring more than 30 % of the linked products
//     (and at least 5) needs ConfirmRetire; a scheduled import never carries it (only a human
//     confirms, so ConfirmRetire is ignored when Scheduled is set): it stops, stores the situation
//     for the dashboard and applies nothing.
func (s *CatalogService) Sync(ctx context.Context, req SyncRequest) (*SyncResult, error) {
	if !req.Mode.Valid() {
		return nil, fmt.Errorf("hubrise: unknown sync mode %q", req.Mode)
	}
	conn, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	if conn.RelinkRequiredAt != nil {
		return nil, domain.ErrRelinkRequired
	}
	if req.Mode == domain.SyncFull && conn.CatalogDirection() == domain.DirectionPublish {
		return nil, domain.ErrPublishMode
	}
	if req.Mode != domain.SyncDryRun && !req.PlanOnly && !conn.Enabled {
		return nil, domain.ErrNotEnabled
	}

	// The holder is unique per call: the lease treats its own holder as a renewal, so a shared one
	// would let two imports of this process (a double click, a click and the drift job) both in.
	holder := s.Holder + ":" + uuid.NewString()
	ok, err := s.Leases.Acquire(ctx, catalogLease, holder, s.now(), catalogLeaseTTL)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrImportInProgress
	}
	defer func() { _ = s.Leases.Release(context.WithoutCancel(ctx), catalogLease, holder) }()

	api, cat, err := s.fetch(ctx, conn, req.Scheduled)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.Local.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	links, err := s.allLinks(ctx)
	if err != nil {
		return nil, err
	}
	diff := Plan(PlanInput{Catalog: cat, Local: snapshot, Links: NewLinkIndex(links), Mode: req.Mode})
	res := &SyncResult{Mode: req.Mode, Diff: diff, Warnings: diff.Warnings}

	if req.PlanOnly {
		return res, nil
	}
	if req.Mode == domain.SyncDryRun {
		return res, s.Connections.RecordCatalogPreview(ctx, diff.CatalogHash, s.now())
	}

	if diff.RetireNeedsConfirm && (!req.ConfirmRetire || req.Scheduled) {
		confirm := &domain.RetireConfirmError{Retiring: diff.RetiringProducts, Linked: diff.LinkedProducts}
		if !req.Scheduled {
			return res, confirm
		}
		res.PendingReview = true
		msg := domain.PendingReviewPrefix + confirm.Error()
		if err := s.Connections.RecordError(ctx, msg); err != nil {
			return res, err
		}
		logging.FromContext(ctx).Warn("hubrise: scheduled catalog import waits for confirmation", zap.Int("retiring", confirm.Retiring), zap.Int("linked", confirm.Linked))
		return res, nil
	}

	applier := &Applier{Local: s.Local, Clock: s.Clock}
	res.Result = applier.Apply(ctx, diff, req.Mode)
	res.Applied = true

	if req.Mode == domain.SyncFull {
		res.Warnings = append(res.Warnings, s.importImages(ctx, conn, api, res.Result)...)
	}
	if changed := res.Result.ChangedProducts(); len(changed) > 0 && s.Notifier != nil {
		s.Notifier.ProductsChanged(ctx, changed)
	}

	switch {
	case len(res.Result.Failed) > 0:
		first := res.Result.Failed[0]
		msg := fmt.Sprintf("catalog import: %d of the batches failed, first %q: %v", len(res.Result.Failed), first.Batch, first.Err)
		if err := s.Connections.RecordError(ctx, msg); err != nil {
			return res, err
		}
	case req.Mode == domain.SyncFull:
		if err := s.Connections.RecordCatalogSync(ctx, diff.CatalogHash, s.now()); err != nil {
			return res, err
		}
		if conn.CatalogPendingReview() { // the review happened: this import is the answer
			if err := s.Connections.RecordError(ctx, ""); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// Preview plans an import without writing anything. FULL and DRY_RUN show what a full import would
// do and record the catalog as previewed, which is what enabling catalog sync asks for. LINK_ONLY
// shows the links that import would record and does not count as that preview, because it says
// nothing about the products a full import would create or retire.
func (s *CatalogService) Preview(ctx context.Context, mode domain.SyncMode) (*SyncResult, error) {
	if mode == domain.SyncFull {
		if conn, err := s.connection(ctx); err == nil && conn.CatalogDirection() == domain.DirectionPublish {
			return nil, domain.ErrPublishMode
		}
	}
	if mode == domain.SyncLinkOnly {
		return s.Sync(ctx, SyncRequest{Mode: mode, PlanOnly: true})
	}
	return s.Sync(ctx, SyncRequest{Mode: domain.SyncDryRun})
}

func (s *CatalogService) allLinks(ctx context.Context) ([]domain.CatalogLink, error) {
	var all []domain.CatalogLink
	for _, t := range []domain.EntityType{domain.EntityCategory, domain.EntityProduct, domain.EntityChoiceGroup, domain.EntityChoice} {
		links, err := s.Links.ListByType(ctx, t, true)
		if err != nil {
			return nil, err
		}
		all = append(all, links...)
	}
	return all, nil
}

// importImages downloads the images of the products that changed, when the connection asks for it.
// Every failure is a warning, never an error: the menu is already imported.
func (s *CatalogService) importImages(ctx context.Context, conn *domain.Connection, api CatalogAPI, res *ApplyResult) []domain.Warning {
	if !conn.ImportImages || s.Images == nil || conn.CatalogID == nil {
		return nil
	}
	var warnings []domain.Warning
	for _, a := range res.Applied {
		p := a.Change.Product
		if a.Change.Entity != domain.EntityProduct || p == nil || p.ImageID == "" || a.Change.Action == domain.ActionRetire {
			continue
		}
		if err := s.importImage(ctx, api, *conn.CatalogID, a.LocalID, p.ImageID); err != nil {
			logging.FromContext(ctx).Warn("hubrise: image import failed", zap.String("sku", p.Key), zap.Error(err))
			warnings = append(warnings, domain.Warning{Code: domain.WarnImage, Key: p.Key, Message: err.Error()})
		}
	}
	return warnings
}

func (s *CatalogService) importImage(ctx context.Context, api CatalogAPI, catalogID string, productID uuid.UUID, imageID string) error {
	link, err := s.Links.FindByLocal(ctx, domain.EntityProduct, productID)
	if err != nil {
		return err
	}
	meta, err := api.GetImage(ctx, catalogID, imageID)
	if err != nil {
		return err
	}
	if link.ImageMD5 != nil && *link.ImageMD5 == meta.MD5 {
		return nil // already imported: save the download
	}
	img, err := api.GetImageData(ctx, catalogID, imageID)
	if err != nil {
		return err
	}
	if got := md5Hex(img.Data); got != meta.MD5 {
		return errImageChecksum(meta.MD5, got)
	}
	if err := s.Images.UploadProductImage(ctx, productID, imageFilename(productID, img.ContentType), img.Data); err != nil {
		return err
	}
	return s.Links.SetImageMD5(ctx, link.ID, meta.MD5)
}

// DriftOutcome is what a drift check found.
type DriftOutcome struct {
	// Changed is true when HubRise has a catalog the shop has not imported.
	Changed bool
	// Imported is true when the check imported it (automatic import).
	Imported bool
	// PendingReview is true when the automatic import stopped at the mass-retire threshold.
	PendingReview bool
}

// CheckDrift is the scheduled check: it fetches the catalog and compares its hash with the one last
// imported. With automatic import (and catalog sync on) a change is imported; otherwise it is only
// recorded, for the dashboard's "update available" banner. Both obey the preview and mass-retire
// rules, and a mass retirement is never applied by this path.
func (s *CatalogService) CheckDrift(ctx context.Context) (*DriftOutcome, error) {
	conn, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	if !conn.Active() {
		return &DriftOutcome{}, nil
	}
	if conn.CatalogDirection() == domain.DirectionPublish {
		// The dashboard owns the menu: nothing is imported, and an edit made at HubRise is only a warning.
		if s.Publish == nil {
			return &DriftOutcome{}, nil
		}
		return &DriftOutcome{}, s.Publish.CheckRemoteDrift(ctx)
	}
	_, cat, err := s.fetch(ctx, conn, true)
	if err != nil {
		return nil, err
	}
	hash := cat.Hash()
	if conn.CatalogHash != nil && *conn.CatalogHash == hash {
		if conn.CatalogUpdateAvailableAt != nil {
			return &DriftOutcome{}, s.Connections.SetCatalogUpdateAvailable(ctx, nil)
		}
		return &DriftOutcome{}, nil
	}
	out := &DriftOutcome{Changed: true}
	if !conn.CatalogAutoImport || !conn.SyncCatalog {
		if conn.CatalogUpdateAvailableAt == nil {
			now := s.now()
			return out, s.Connections.SetCatalogUpdateAvailable(ctx, &now)
		}
		return out, nil
	}
	res, err := s.Sync(ctx, SyncRequest{Mode: domain.SyncFull, Scheduled: true})
	if err != nil {
		return out, err
	}
	out.Imported, out.PendingReview = res.Applied, res.PendingReview
	return out, nil
}

// DriftChecker adapts CheckDrift to the CatalogDriftChecker port of the drift job, which only wants
// to know whether the check ran.
func (s *CatalogService) DriftChecker() CatalogDriftChecker { return driftChecker{s} }

type driftChecker struct{ s *CatalogService }

func (d driftChecker) CheckDrift(ctx context.Context) error {
	_, err := d.s.CheckDrift(ctx)
	return err
}

// CheckEnableSyncCatalog says whether catalog sync may be switched on: the connection must not wait
// for a relink, and a dry run of the CURRENT catalog, less than a day old, must have succeeded.
func (s *CatalogService) CheckEnableSyncCatalog(ctx context.Context) error {
	conn, err := s.connection(ctx)
	if err != nil {
		return err
	}
	if err := s.checkRelink(ctx, conn); err != nil {
		return err
	}
	if conn.CatalogPreviewHash == nil || conn.CatalogPreviewAt == nil || s.now().Sub(*conn.CatalogPreviewAt) > previewMaxAge {
		return domain.ErrPreviewRequired
	}
	_, cat, err := s.fetch(ctx, conn, false)
	if err != nil {
		return err
	}
	if cat.Hash() != *conn.CatalogPreviewHash {
		return domain.ErrPreviewRequired
	}
	return nil
}

// CheckEnable says whether the integration may be switched on: not while a reconnect to another
// location or catalog waits for the old links to be forgotten.
func (s *CatalogService) CheckEnable(ctx context.Context) error {
	conn, err := s.connection(ctx)
	if err != nil {
		return err
	}
	return s.checkRelink(ctx, conn)
}

func (s *CatalogService) checkRelink(ctx context.Context, conn *domain.Connection) error {
	if conn.RelinkRequiredAt == nil {
		return nil
	}
	n, err := s.Links.Count(ctx, true)
	if err != nil {
		return err
	}
	if n == 0 { // the links are gone, the flag is stale
		return s.Connections.SetRelinkRequired(ctx, nil)
	}
	return domain.ErrRelinkRequired
}

// AfterConnect must be called after the OAuth callback stored a connection. previous is the row as it
// was before (nil when there was none). When the new connection names another HubRise location or
// catalog than the one the existing links were made for, those links would send stale refs and a
// whole menu would be retired against a foreign catalog: the integration is switched off, catalog
// sync and inventory sync with it, and the connection waits for the links to be forgotten. It
// reports whether that happened.
func (s *CatalogService) AfterConnect(ctx context.Context, previous *domain.Connection) (relinkRequired bool, err error) {
	if previous == nil {
		return false, nil
	}
	current, err := s.Connections.Get(ctx)
	if err != nil {
		return false, err
	}
	changed, err := s.RelinkNeeded(ctx, previous, deref2(current.LocationID), deref2(current.CatalogID))
	if err != nil {
		return false, err
	}
	if !changed {
		return current.RelinkRequiredAt != nil, nil
	}
	off := false
	if _, err := s.Connections.UpdateSettings(ctx, domain.SettingsPatch{Enabled: &off, SyncCatalog: &off, SyncInventory: &off}); err != nil {
		return false, err
	}
	now := s.now()
	return true, s.Connections.SetRelinkRequired(ctx, &now)
}

// RelinkNeeded reports whether connecting to the given HubRise location and catalog would leave
// links that were made for another one (previous is the row as it is now, nil when there is none).
// The OAuth callback asks it before storing the connection and hands the answer to
// ConnectionRepository.Connect, so the integration is already off when the new ids land.
func (s *CatalogService) RelinkNeeded(ctx context.Context, previous *domain.Connection, locationID, catalogID string) (bool, error) {
	if previous == nil {
		return false, nil
	}
	if sameValue(previous.LocationID, &locationID) && sameValue(previous.CatalogID, &catalogID) {
		return false, nil
	}
	n, err := s.Links.Count(ctx, true)
	return n > 0, err
}

func sameValue(a, b *string) bool {
	return strings.TrimSpace(deref2(a)) == strings.TrimSpace(deref2(b))
}

func deref2(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ForgetLinks removes every catalog link (the products stay, as plain local products) and lifts the
// relink requirement. It returns how many links were removed.
func (s *CatalogService) ForgetLinks(ctx context.Context) (int64, error) {
	n, err := s.Links.DeleteAll(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.forgetPublished(ctx); err != nil {
		return n, err
	}
	if _, err := s.Connections.Get(ctx); err == nil {
		if err := s.Connections.SetRelinkRequired(ctx, nil); err != nil {
			return n, err
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return n, err
	}
	return n, nil
}

// forgetPublished forgets what was published: without its links the shop no longer knows which
// HubRise items it owns, so the next publish is a first one again (preview and confirmation).
func (s *CatalogService) forgetPublished(ctx context.Context) error {
	if s.PublishItems != nil {
		if err := s.PublishItems.Clear(ctx); err != nil {
			return err
		}
	}
	if s.PublishState == nil {
		return nil
	}
	conn, err := s.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && conn.CatalogDirection() != domain.DirectionPublish) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.PublishState.Reset(ctx, s.now())
}

// LinkProduct links a local product to a SKU by hand (the explicit link adoption starts from), or,
// with a nil skuRef, removes the link of the product. A SKU already linked to another product is
// refused.
func (s *CatalogService) LinkProduct(ctx context.Context, productID uuid.UUID, skuRef *string) error {
	existing, err := s.Links.FindByLocal(ctx, domain.EntityProduct, productID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if skuRef == nil || strings.TrimSpace(*skuRef) == "" {
		if existing == nil {
			return nil
		}
		return s.Links.Delete(ctx, existing.ID)
	}
	ref := strings.TrimSpace(*skuRef)
	if other, err := s.Links.FindByKey(ctx, domain.EntityProduct, ref); err == nil {
		if other.LocalID() != productID {
			return fmt.Errorf("%w: %q", domain.ErrSKULinked, ref)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if existing != nil && existing.HubriseKey != ref { // re-pointing the product: drop its old link
		if err := s.Links.Delete(ctx, existing.ID); err != nil {
			return err
		}
	}
	link := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: ref, HubriseRef: ref, Origin: domain.OriginManual}
	link.SetLocalID(productID)
	return s.Links.Upsert(ctx, link)
}
