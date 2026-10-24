package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// AuditPublish is the audit action of a catalog publish. Its details carry counts only.
const AuditPublish = "hubrise.catalog.publish"

// publishTick is how often the publisher looks for a due run. A tick is one indexed read.
const publishTick = 5 * time.Second

// publishScanJob names the lease of the local-hash scan.
const publishScanJob = "catalog_publish_scan"

// PublishAPI is the part of the HubRise API the catalog publisher uses.
type PublishAPI interface {
	GetCatalogRaw(ctx context.Context, id string) (*hubrise.RawCatalog, error)
	ReplaceCatalog(ctx context.Context, id string, data json.RawMessage) (*hubrise.RawCatalog, error)
	GetLocation(ctx context.Context) (*hubrise.Location, error)
}

// CatalogPublisher publishes the local menu to the HubRise catalog (amendment A15). The dashboard
// owns the menu; every edit marks the singleton publisher row dirty, and this service, on whichever
// replica takes the lease, reads the HubRise catalog, merges the menu into it and replaces the
// catalog. It never imports.
type CatalogPublisher struct {
	Connections domain.ConnectionRepository
	Links       domain.CatalogLinkRepository
	State       domain.PublishStateRepository
	Items       domain.PublishedItemRepository
	Local       domain.LocalCatalogRepository
	Clock       domain.Clock
	// Leases arbitrates the periodic scan between replicas. Optional: without it the scan is off.
	Leases domain.LeaseRepository
	// NewClient builds the API client of the connection.
	NewClient func(*domain.Connection) (PublishAPI, error)
	// Notifier is told which products a publish touched. Optional.
	Notifier CatalogNotifier
	// Audit records the publishes. Optional.
	Audit AuditFunc
	// Holder identifies this instance in the lease.
	Holder string
}

// PublishRequest is an interactive publish.
type PublishRequest struct {
	// PlanHash is the plan the admin saw; required for the first publish and for removals above the
	// threshold, and refused when it no longer matches.
	PlanHash string
	// ConfirmRemovals accepts a removal above the mass-removal threshold.
	ConfirmRemovals bool
	// RemoveForeign also removes the items only HubRise has.
	RemoveForeign bool
}

// PublishOutcome is what an interactive publish did.
type PublishOutcome struct {
	// Published says the catalog was written (or found identical) by this call.
	Published bool
	// Queued says another replica holds the lease: the work was queued for the debounced run.
	Queued bool
	Plan   *PublishPlan
}

// PublishPreview is the plan an admin reviews before confirming.
type PublishPreview struct {
	Plan         *PublishPlan
	FirstPublish bool
	GeneratedAt  time.Time
}

func (p *CatalogPublisher) now() time.Time { return p.Clock.Now() }

// ready checks that the connection may publish at all.
func (p *CatalogPublisher) ready(conn *domain.Connection) error {
	switch {
	case !conn.Connected():
		return domain.ErrNotConnected
	case conn.CatalogDirection() != domain.DirectionPublish:
		return domain.ErrNotPublishMode
	case !conn.Enabled:
		return domain.ErrNotEnabled
	case conn.RelinkRequiredAt != nil:
		return domain.ErrRelinkRequired
	case conn.CatalogID == nil || *conn.CatalogID == "":
		return fmt.Errorf("hubrise: the location has no catalog: %w", domain.ErrNotConnected)
	case !conn.HasWriteScope():
		return domain.ErrReconnectRequired
	case conn.AuthErrorAt != nil:
		return &hubrise.Error{Kind: hubrise.KindUnauthorized, Op: "publish", Message: "HubRise refuses the token, reconnect"}
	}
	return nil
}

func (p *CatalogPublisher) connection(ctx context.Context) (*domain.Connection, error) {
	conn, err := p.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.ErrNotConnected
	}
	return conn, err
}

func settingsOf(conn *domain.Connection) PublishSettings {
	s := PublishSettings{Language: conn.CatalogLanguage, Images: conn.PublishImages}
	if conn.VariantRef != nil {
		s.VariantRef = *conn.VariantRef
	}
	return s
}

// Preview plans a publish against the current HubRise catalog and writes nothing. It costs one read
// of the catalog (the 10 a minute bucket).
func (p *CatalogPublisher) Preview(ctx context.Context, removeForeign bool) (*PublishPreview, error) {
	conn, err := p.connection(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.ready(conn); err != nil {
		return nil, err
	}
	snap, links, err := p.Local.SnapshotWithLinks(ctx)
	if err != nil {
		return nil, err
	}
	published, err := p.Items.List(ctx)
	if err != nil {
		return nil, err
	}
	state, err := p.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	api, err := p.NewClient(conn)
	if err != nil {
		return nil, err
	}
	raw, err := api.GetCatalogRaw(ctx, *conn.CatalogID)
	if err != nil {
		p.noteAuthError(ctx, err)
		return nil, err
	}
	if raw.LocationID == "" && raw.AccountID != "" {
		return nil, domain.ErrAccountCatalog
	}
	plan, err := PlanPublish(PublishInput{Local: snap, Links: links, Published: published, Settings: settingsOf(conn),
		Base: raw.Data, RemoveForeign: removeForeign})
	if err != nil {
		return nil, err
	}
	return &PublishPreview{Plan: plan, FirstPublish: state.FirstPublishConfirmedAt == nil, GeneratedAt: p.now()}, nil
}

// PublishNow publishes at once, skipping the debounce. The first publish and removals above the
// threshold need the confirmation (see PublishRequest). When another replica holds the lease the
// work is queued for the debounced run instead.
func (p *CatalogPublisher) PublishNow(ctx context.Context, req PublishRequest) (*PublishOutcome, error) {
	conn, err := p.connection(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.ready(conn); err != nil {
		return nil, err
	}
	holder := p.Holder + ":" + uuid.NewString()
	seq, ok, err := p.State.ClaimNow(ctx, holder, p.now())
	if err != nil {
		return nil, err
	}
	if !ok {
		state, err := p.State.Get(ctx)
		if err != nil {
			return nil, err
		}
		if state.FirstPublishConfirmedAt == nil {
			return nil, domain.ErrPublishBusy
		}
		if _, err := p.State.MarkDirty(ctx, p.now()); err != nil {
			return nil, err
		}
		return &PublishOutcome{Queued: true}, nil
	}
	return p.execute(ctx, holder, seq, conn, &req)
}

// Tick takes the lease when a background publish is due and runs it. ran says whether it did.
func (p *CatalogPublisher) Tick(ctx context.Context) (ran bool, err error) {
	holder := p.Holder + ":" + uuid.NewString()
	seq, ok, err := p.State.Claim(ctx, holder, p.now())
	if err != nil || !ok {
		return false, err
	}
	conn, err := p.connection(ctx)
	if err != nil {
		_ = p.State.Release(context.WithoutCancel(ctx), holder)
		return true, err
	}
	_, err = p.execute(hubrise.Background(ctx), holder, seq, conn, nil)
	return true, err
}

// Run ticks until ctx ends, and keeps the local-hash scan going.
func (p *CatalogPublisher) Run(ctx context.Context) {
	if p.Leases != nil {
		scan := periodicJob{name: publishScanJob, holder: p.Holder, interval: domain.PublishScanInterval,
			leases: p.Leases, clock: p.Clock, run: p.Scan}
		go scan.loop(ctx)
	}
	ticker := time.NewTicker(publishTick)
	defer ticker.Stop()
	for {
		if _, err := p.Tick(ctx); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).Error("hubrise: catalog publish tick failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Scan is the safety net for menu writers that bypass the change hook (a script, a migration, another
// service): it compares the hash of the local menu with the one of the last publish and marks the
// publisher dirty when they differ.
func (p *CatalogPublisher) Scan(ctx context.Context) error {
	conn, err := p.connection(ctx)
	if err != nil || conn.CatalogDirection() != domain.DirectionPublish {
		return nil //nolint:nilerr // no connection or not publishing: nothing to scan
	}
	state, err := p.State.Get(ctx)
	if err != nil {
		return err
	}
	if state.FirstPublishConfirmedAt == nil || state.LastPublishedLocalHash == nil || state.Dirty() {
		return nil
	}
	snap, err := p.Local.Snapshot(ctx)
	if err != nil {
		return err
	}
	if LocalMenuHash(snap, settingsOf(conn)) == *state.LastPublishedLocalHash {
		return nil
	}
	_, err = p.State.MarkDirty(ctx, p.now())
	return err
}

// execute runs one publish with the lease held. Every way out either completes the run, records
// why it did not publish, or releases the lease. req is nil for a background run.
func (p *CatalogPublisher) execute(ctx context.Context, holder string, seq int64, conn *domain.Connection, req *PublishRequest) (out *PublishOutcome, err error) {
	interactive := req != nil
	settled := false
	defer func() {
		if !settled {
			_ = p.State.Release(context.WithoutCancel(ctx), holder)
		}
	}()
	bg := context.WithoutCancel(ctx)
	// extend renews the lease; a lost one is not ours to release.
	extend := func() error {
		ok, err := p.State.Extend(ctx, holder, p.now())
		if err != nil {
			return err
		}
		if !ok {
			settled = true
			return domain.ErrPublishBusy
		}
		return nil
	}
	// fail records a failure (the lease is released with it) and returns the error to an interactive
	// caller; a background run swallows it, the state row is the report.
	fail := func(f domain.PublishFailure, block bool, cause error) (*PublishOutcome, error) {
		settled = true
		var rerr error
		if block {
			rerr = p.State.Block(bg, holder, f.Code, f.Message, p.now())
		} else {
			rerr = p.State.RecordError(bg, holder, f, p.now())
		}
		p.audit(ctx, interactive, false, map[string]any{"code": string(f.Code)})
		if rerr != nil {
			return nil, errors.Join(cause, rerr)
		}
		if interactive {
			return nil, cause
		}
		return nil, nil
	}

	if err := p.ready(conn); err != nil {
		if !interactive && errors.Is(err, domain.ErrReconnectRequired) {
			return fail(domain.PublishFailure{Code: domain.PublishWriteScopeMissing, Message: err.Error()}, true, err)
		}
		return nil, err
	}
	if err := extend(); err != nil {
		return nil, err
	}

	snap, links, err := p.Local.SnapshotWithLinks(ctx)
	if err != nil {
		return nil, err
	}
	published, err := p.Items.List(ctx)
	if err != nil {
		return nil, err
	}
	state, err := p.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	settings := settingsOf(conn)
	localHash := LocalMenuHash(snap, settings)
	if !interactive && state.LastPublishedLocalHash != nil && *state.LastPublishedLocalHash == localHash {
		// The edits that woke us (an availability toggle, a no-op save) change nothing HubRise
		// holds: no read, no write, the 10 a minute bucket stays free.
		remote := ""
		if state.LastPublishedRemoteHash != nil {
			remote = *state.LastPublishedRemoteHash
		}
		settled = true
		return nil, p.State.Complete(bg, holder, seq, domain.PublishResult{At: p.now(), LocalHash: localHash, RemoteHash: remote}, published, nil)
	}

	api, err := p.NewClient(conn)
	if err != nil {
		return nil, err
	}
	if err := extend(); err != nil {
		return nil, err
	}
	raw, err := api.GetCatalogRaw(ctx, *conn.CatalogID)
	if err != nil {
		return p.failFromRead(ctx, state, err, fail)
	}
	if raw.LocationID == "" && raw.AccountID != "" {
		return fail(domain.PublishFailure{Code: domain.PublishAccountCatalog, Message: domain.ErrAccountCatalog.Error()}, true, domain.ErrAccountCatalog)
	}

	in := PublishInput{Local: snap, Links: links, Published: published, Settings: settings, Base: raw.Data}
	if interactive {
		in.RemoveForeign = req.RemoveForeign
	}
	plan, err := PlanPublish(in)
	if errors.Is(err, domain.ErrEmptyMenu) {
		return fail(domain.PublishFailure{Code: domain.PublishInvalidMenu, Message: err.Error()}, true, err)
	}
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) > 0 {
		blocked := &domain.PublishBlockedError{Blockers: plan.Blockers}
		return fail(domain.PublishFailure{Code: domain.PublishInvalidMenu, Message: truncate(blocked.Error(), 500)}, true, blocked)
	}

	if interactive {
		if err := p.confirmed(state, plan, req); err != nil {
			return nil, err
		}
	} else if plan.RemovalsNeedConfirm {
		msg := fmt.Sprintf("this publish removes %d of %d products from HubRise, confirm it in the dashboard", plan.RemovedSkus, plan.PublishedSkus)
		return fail(domain.PublishFailure{Code: domain.PublishRemovalsNeedConfirm, Message: msg}, true, domain.ErrPublishConfirmRequired)
	}

	if err := p.writeLinks(ctx, plan); err != nil {
		return fail(domain.PublishFailure{Code: domain.PublishUnavailable, Message: truncate(err.Error(), 500), RetryAt: p.retryAt(state), CountAttempt: true}, false, err)
	}
	if interactive && state.FirstPublishConfirmedAt == nil {
		now := p.now()
		if err := p.State.SetFirstPublishConfirmed(ctx, &now); err != nil {
			return nil, err
		}
	}

	var ids []domain.LinkHubriseID
	if plan.NeedsPut {
		if err := extend(); err != nil {
			return nil, err
		}
		put, err := api.ReplaceCatalog(ctx, *conn.CatalogID, plan.Data)
		if err != nil {
			return p.failFromWrite(ctx, api, state, err, fail)
		}
		ids = linkIDsOf(put)
	}

	items := make([]domain.PublishedItem, 0, len(plan.Desired))
	for _, d := range plan.Desired {
		items = append(items, domain.PublishedItem{EntityType: d.Entity, HubriseKey: d.Key, HubriseRef: d.Ref, ContentHash: d.Hash})
	}
	settled = true
	if err := p.State.Complete(bg, holder, seq, domain.PublishResult{At: p.now(), LocalHash: plan.LocalMenuHash, RemoteHash: RemoteCatalogHash(plan.Data)}, items, ids); err != nil {
		return nil, err
	}
	p.audit(ctx, interactive, true, map[string]any{
		"added": plan.Count(ActionPublishAdd), "updated": plan.Count(ActionPublishUpdate), "removed": plan.Count(ActionPublishRemove),
		"wrote": plan.NeedsPut,
	})
	p.notify(ctx, plan)
	return &PublishOutcome{Published: true, Plan: plan}, nil
}

// confirmed applies the confirmation rules of an interactive publish.
func (p *CatalogPublisher) confirmed(state *domain.PublishState, plan *PublishPlan, req *PublishRequest) error {
	first := state.FirstPublishConfirmedAt == nil
	if req.PlanHash != "" && req.PlanHash != plan.PlanHash {
		return domain.ErrPreviewStale
	}
	if first && req.PlanHash == "" {
		return domain.ErrPublishConfirmRequired
	}
	if plan.RemovalsNeedConfirm && (!req.ConfirmRemovals || req.PlanHash == "") {
		return domain.ErrPublishConfirmRequired
	}
	return nil
}

// writeLinks stores the refs the plan generated, the splits and the revivals, under the importer's
// advisory lock, before the catalog is written: a PUT that succeeds always finds its links.
func (p *CatalogPublisher) writeLinks(ctx context.Context, plan *PublishPlan) error {
	if len(plan.NewLinks) == 0 && len(plan.Rekeys) == 0 && len(plan.Unretire) == 0 {
		return nil
	}
	return p.Local.InBatch(ctx, func(ctx context.Context, tx domain.CatalogTx) error {
		links := tx.Links()
		for _, id := range plan.Unretire {
			if err := links.SetRetired(ctx, id, nil); err != nil {
				return err
			}
		}
		for _, rk := range plan.Rekeys {
			if err := links.Rekey(ctx, rk.LinkID, rk.NewKey, rk.NewRef); err != nil {
				return err
			}
		}
		for _, nl := range plan.NewLinks {
			l := &domain.CatalogLink{EntityType: nl.Entity, HubriseKey: nl.Key, HubriseRef: nl.Ref, Origin: domain.OriginPublished}
			l.SetLocalID(nl.LocalID)
			if err := links.Upsert(ctx, l); err != nil {
				return err
			}
		}
		return nil
	})
}

// linkIDsOf reads the ids HubRise gave the elements of the catalog it just wrote.
func linkIDsOf(put *hubrise.RawCatalog) []domain.LinkHubriseID {
	if put == nil || len(put.Data) == 0 {
		return nil
	}
	var data hubrise.CatalogData
	if err := json.Unmarshal(put.Data, &data); err != nil {
		return nil
	}
	neutral := MapCatalog(&hubrise.Catalog{Data: &data}, MapOptions{})
	var ids []domain.LinkHubriseID
	add := func(t domain.EntityType, key, id string) {
		if id != "" {
			ids = append(ids, domain.LinkHubriseID{EntityType: t, Key: key, HubriseID: id})
		}
	}
	for _, c := range neutral.Categories {
		add(domain.EntityCategory, c.Key, c.HubriseID)
	}
	for _, pr := range neutral.Products {
		add(domain.EntityProduct, pr.Key, pr.HubriseID)
		for _, g := range pr.Groups {
			add(domain.EntityChoiceGroup, g.Key, g.HubriseID)
			for _, c := range g.Choices {
				add(domain.EntityChoice, c.Key, c.HubriseID)
			}
		}
	}
	return ids
}

// backoff is the retry delay after the attempts-th consecutive failure: 30 s, doubling, 15 min at most.
func backoff(attempts int) time.Duration {
	d := domain.PublishBackoffMin
	for i := 1; i < attempts && d < domain.PublishBackoffMax; i++ {
		d *= 2
	}
	return min(d, domain.PublishBackoffMax)
}

func (p *CatalogPublisher) retryAt(state *domain.PublishState) *time.Time {
	at := p.now().Add(backoff(state.Attempts + 1))
	return &at
}

type failFunc func(f domain.PublishFailure, block bool, cause error) (*PublishOutcome, error)

// failFromRead classifies the failure of the catalog read (A15.6).
func (p *CatalogPublisher) failFromRead(ctx context.Context, state *domain.PublishState, err error, fail failFunc) (*PublishOutcome, error) {
	switch hubrise.KindOf(err) {
	case hubrise.KindUnauthorized, hubrise.KindForbidden:
		p.noteAuthError(ctx, err)
		return fail(domain.PublishFailure{Code: domain.PublishAuth, Message: truncate(err.Error(), 500)}, false, err)
	case hubrise.KindNotFound:
		return fail(domain.PublishFailure{Code: domain.PublishCatalogNotFound, Message: truncate(err.Error(), 500)}, true, err)
	case hubrise.KindCanceled:
		return fail(domain.PublishFailure{Code: domain.PublishUnavailable, Message: "canceled"}, false, err)
	}
	return p.failTransient(state, err, fail)
}

// failFromWrite classifies the failure of the catalog replacement (A15.6). A 403 is about this
// catalog write only and pauses nothing else; a 401 is either a refused token or an account level
// catalog, and a probe of the location tells which.
func (p *CatalogPublisher) failFromWrite(ctx context.Context, api PublishAPI, state *domain.PublishState, err error, fail failFunc) (*PublishOutcome, error) {
	switch hubrise.KindOf(err) {
	case hubrise.KindForbidden:
		return fail(domain.PublishFailure{Code: domain.PublishWriteScopeMissing, Message: truncate(err.Error(), 500)}, true, err)
	case hubrise.KindUnauthorized:
		if _, perr := api.GetLocation(ctx); perr != nil && (hubrise.KindOf(perr) == hubrise.KindUnauthorized || hubrise.KindOf(perr) == hubrise.KindForbidden) {
			p.noteAuthError(ctx, perr)
			return fail(domain.PublishFailure{Code: domain.PublishAuth, Message: truncate(perr.Error(), 500)}, false, err)
		}
		return fail(domain.PublishFailure{Code: domain.PublishAccountCatalog, Message: domain.ErrAccountCatalog.Error()}, true, domain.ErrAccountCatalog)
	case hubrise.KindNotFound:
		return fail(domain.PublishFailure{Code: domain.PublishCatalogNotFound, Message: truncate(err.Error(), 500)}, true, err)
	case hubrise.KindInvalid:
		return fail(domain.PublishFailure{Code: domain.PublishRejected, Message: truncate(err.Error(), 500)}, true, err)
	case hubrise.KindCanceled:
		return fail(domain.PublishFailure{Code: domain.PublishUnavailable, Message: "canceled"}, false, err)
	}
	return p.failTransient(state, err, fail)
}

// failTransient is a rate limit (rescheduled without burning an attempt) or an outage (backoff).
func (p *CatalogPublisher) failTransient(state *domain.PublishState, err error, fail failFunc) (*PublishOutcome, error) {
	var he *hubrise.Error
	if errors.As(err, &he) && he.Kind == hubrise.KindRateLimited {
		wait := max(he.RetryAfter, time.Second)
		at := p.now().Add(wait)
		return fail(domain.PublishFailure{Code: domain.PublishUnavailable, Message: truncate(err.Error(), 500), RetryAt: &at}, false, err)
	}
	return fail(domain.PublishFailure{Code: domain.PublishUnavailable, Message: truncate(err.Error(), 500), RetryAt: p.retryAt(state), CountAttempt: true}, false, err)
}

// noteAuthError pauses the pipeline on a revoked or forbidden token, like every other HubRise call.
func (p *CatalogPublisher) noteAuthError(ctx context.Context, err error) {
	if !errors.Is(err, hubrise.KindUnauthorized) && !errors.Is(err, hubrise.KindForbidden) {
		return
	}
	if rerr := p.Connections.RecordAuthError(context.WithoutCancel(ctx), p.now(), err.Error()); rerr != nil {
		logging.FromContext(ctx).Error("hubrise: record auth error", zap.Error(rerr))
	}
}

func (p *CatalogPublisher) audit(ctx context.Context, interactive, success bool, details map[string]any) {
	if p.Audit == nil {
		return
	}
	if interactive {
		details["trigger"] = "manual"
	} else {
		details["trigger"] = "background"
	}
	p.Audit(context.WithoutCancel(ctx), AuditPublish, success, details)
}

// notify tells the dashboards which products a publish touched.
func (p *CatalogPublisher) notify(ctx context.Context, plan *PublishPlan) {
	if p.Notifier == nil {
		return
	}
	changed := map[string]bool{}
	for _, c := range plan.Changes {
		if c.Entity == domain.EntityProduct {
			changed[c.Key] = true
		}
	}
	var ids []uuid.UUID
	for _, d := range plan.Desired {
		if d.Entity == domain.EntityProduct && changed[d.Key] {
			ids = append(ids, d.LocalID)
		}
	}
	if len(ids) > 0 {
		p.Notifier.ProductsChanged(context.WithoutCancel(ctx), ids)
	}
}
