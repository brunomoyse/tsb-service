package application

import (
	"context"
	"time"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// externalEchoWindow is how long after our own publish a catalog event is taken for the echo of that
// publish rather than for somebody else's edit.
const externalEchoWindow = 2 * time.Minute

// PublishStatus is what the dashboard shows about the catalog publisher.
type PublishStatus struct {
	// Enabled says the direction is PUBLISH.
	Enabled bool
	// Active says a publish may run right now.
	Active                  bool
	Pending                 bool
	PendingSince            *time.Time
	NextPublishAt           *time.Time
	Publishing              bool
	LastPublishedAt         *time.Time
	FirstPublishConfirmedAt *time.Time
	AwaitingFirstPublish    bool
	LastError               *string
	LastErrorCode           domain.PublishErrorCode
	LastErrorAt             *time.Time
	ExternalChangeAt        *time.Time
	// NeedsReconnectForWrite says the stored scope has no catalog.write, or HubRise refused the write
	// for lack of it.
	NeedsReconnectForWrite bool
	AccountCatalogDetected bool
	PublishImages          bool
	PublishedProducts      int
	// PendingProducts counts the products edited since the last publish (computed only while edits
	// wait).
	PendingProducts int
}

// Status reads the state of the publisher for conn (nil when the installation never connected).
func (p *CatalogPublisher) Status(ctx context.Context, conn *domain.Connection) (*PublishStatus, error) {
	out := &PublishStatus{}
	if conn == nil {
		return out, nil
	}
	state, err := p.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	now := p.now()
	out.Enabled = conn.CatalogDirection() == domain.DirectionPublish
	out.NeedsReconnectForWrite = conn.Connected() && (!conn.HasWriteScope() || state.ErrorCode() == domain.PublishWriteScopeMissing)
	out.PublishImages = conn.PublishImages
	out.AccountCatalogDetected = state.ErrorCode() == domain.PublishAccountCatalog
	out.FirstPublishConfirmedAt = state.FirstPublishConfirmedAt
	out.AwaitingFirstPublish = out.Enabled && state.FirstPublishConfirmedAt == nil
	out.LastPublishedAt = state.LastPublishedAt
	out.ExternalChangeAt = state.ExternalChangeAt
	out.Publishing = state.Leased(now)
	if state.LastErrorCode != nil || state.LastError != nil {
		out.LastError, out.LastErrorCode, out.LastErrorAt = state.LastError, state.ErrorCode(), state.LastErrorAt
	}
	out.Active = out.Enabled && conn.Connected() && conn.Enabled && conn.AuthErrorAt == nil && conn.RelinkRequiredAt == nil &&
		conn.HasWriteScope() && state.FirstPublishConfirmedAt != nil && state.BlockedAt == nil
	if out.Enabled {
		out.Pending = state.Dirty()
		out.PendingSince = state.DirtySince
		if out.Pending && out.Active {
			out.NextPublishAt = state.NextAttemptAt
		}
		if out.PublishedProducts, err = p.Items.Count(ctx, domain.EntityProduct); err != nil {
			return nil, err
		}
		if out.Pending {
			infos, err := p.productInfos(ctx, nil)
			if err != nil {
				return nil, err
			}
			for _, info := range infos {
				if info.State != ProductPublished {
					out.PendingProducts++
				}
			}
		}
	}
	return out, nil
}

// ProductPublishState is where a product stands against the last publish.
type ProductPublishState string

const (
	// ProductPublished: HubRise has the current version.
	ProductPublished ProductPublishState = "PUBLISHED"
	// ProductPending: edited since the last publish.
	ProductPending ProductPublishState = "PENDING"
	// ProductNotPublished: never published.
	ProductNotPublished ProductPublishState = "NOT_PUBLISHED"
)

// ProductPublishInfo is what the dashboard shows about one product in PUBLISH mode.
type ProductPublishInfo struct {
	State       ProductPublishState
	PublishedAt *time.Time
	// Ref is the SKU ref the product has (or will have) at HubRise; empty before its first publish.
	Ref string
}

// ProductInfos returns the publish info of the given products (every product when ids is empty). It
// returns an empty map when the direction is not PUBLISH.
func (p *CatalogPublisher) ProductInfos(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ProductPublishInfo, error) {
	conn, err := p.connection(ctx)
	if err != nil || conn.CatalogDirection() != domain.DirectionPublish {
		return map[uuid.UUID]ProductPublishInfo{}, nil //nolint:nilerr // not connected or not publishing: nothing to show
	}
	return p.productInfos(ctx, ids)
}

func (p *CatalogPublisher) productInfos(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ProductPublishInfo, error) {
	snap, links, err := p.Local.SnapshotWithLinks(ctx)
	if err != nil {
		return nil, err
	}
	items, err := p.Items.List(ctx)
	if err != nil {
		return nil, err
	}
	published := map[string]domain.PublishedItem{}
	for _, it := range items {
		if it.EntityType == domain.EntityProduct {
			published[it.HubriseKey] = it
		}
	}
	linkOf := map[uuid.UUID]domain.CatalogLink{}
	for i := range links {
		if links[i].EntityType == domain.EntityProduct && links[i].RetiredAt == nil {
			linkOf[links[i].LocalID()] = links[i]
		}
	}
	want := map[uuid.UUID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[uuid.UUID]ProductPublishInfo{}
	for i := range snap.Products {
		lp := &snap.Products[i]
		if len(want) > 0 && !want[lp.ID] {
			continue
		}
		info := ProductPublishInfo{State: ProductNotPublished}
		if l, ok := linkOf[lp.ID]; ok {
			info.Ref = l.HubriseRef
			if it, ok := published[l.HubriseKey]; ok {
				at := it.PublishedAt
				info.PublishedAt = &at
				info.State = ProductPending
				if it.ContentHash == productItemHash(lp) {
					info.State = ProductPublished
				}
			}
		}
		out[lp.ID] = info
	}
	return out, nil
}

// NoteExternalChange records that somebody edited the HubRise catalog: a warning, never an import.
// deleted says the catalog itself was deleted, which stops publishing until the admin reconnects.
// Events that arrive right after our own publish are its echo and are ignored.
func (p *CatalogPublisher) NoteExternalChange(ctx context.Context, deleted bool, firstEventAt time.Time) error {
	state, err := p.State.Get(ctx)
	if err != nil {
		return err
	}
	if deleted {
		return p.State.Block(ctx, "", domain.PublishCatalogNotFound, "the HubRise catalog was deleted", p.now())
	}
	if state.LastPublishedAt != nil && !firstEventAt.After(state.LastPublishedAt.Add(externalEchoWindow)) {
		return nil
	}
	now := p.now()
	return p.State.SetExternalChange(ctx, &now)
}

// CheckRemoteDrift is the 6 hourly check of a published catalog: it reads the HubRise catalog and
// compares its content with what the last publish left there. A difference is only a warning; the
// next publish merges around it.
func (p *CatalogPublisher) CheckRemoteDrift(ctx context.Context) error {
	conn, err := p.connection(ctx)
	if err != nil {
		return err
	}
	if conn.CatalogDirection() != domain.DirectionPublish || !conn.Active() || conn.CatalogID == nil {
		return nil
	}
	state, err := p.State.Get(ctx)
	if err != nil {
		return err
	}
	if state.FirstPublishConfirmedAt == nil || state.LastPublishedRemoteHash == nil || *state.LastPublishedRemoteHash == "" || state.Leased(p.now()) {
		return nil
	}
	api, err := p.NewClient(conn)
	if err != nil {
		return err
	}
	raw, err := api.GetCatalogRaw(hubrise.Background(ctx), *conn.CatalogID)
	if err != nil {
		p.noteAuthError(ctx, err)
		return err
	}
	if RemoteCatalogHash(raw.Data) == *state.LastPublishedRemoteHash {
		return nil
	}
	now := p.now()
	return p.State.SetExternalChange(ctx, &now)
}

// AcknowledgeExternalChange clears the "edited elsewhere" warning.
func (p *CatalogPublisher) AcknowledgeExternalChange(ctx context.Context) error {
	return p.State.SetExternalChange(ctx, nil)
}
