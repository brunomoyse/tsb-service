package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// Names of the leases that make the inventory jobs singletons across replicas.
const (
	// LeaseInventorySync serializes every change the inventory makes to availability (a sync, the
	// expiry sweeper), so two replicas never apply the same state twice at once.
	LeaseInventorySync = "inventory-sync"
	// LeaseInventoryPoll and LeaseInventorySweeper are the cadence leases of the two periodic jobs.
	LeaseInventoryPoll    = "inventory-poll"
	LeaseInventorySweeper = "inventory-sweeper"
)

// ErrInventoryBusy means another replica is applying the inventory right now. The caller tries again
// shortly (an event is deferred, a poll waits for its next tick).
var ErrInventoryBusy = errors.New("hubrise: the inventory is being synchronized by another process")

// ProductNotifier tells subscribers (open dashboards, menus) that a product changed without a
// mutation. The resolver publishes it on the productUpdated topic. It may be nil.
type ProductNotifier interface {
	PublishProductUpdated(ctx context.Context, productID uuid.UUID)
}

// InventoryConfig tunes the inventory service; zero values take the defaults of the plan.
type InventoryConfig struct {
	// Holder identifies this process in the lease table.
	Holder string
	// CallTimeout bounds the HubRise read (30 s).
	CallTimeout time.Duration
	// SyncLease is how long a sync keeps the singleton lease (2 minutes). A sync releases it when it
	// ends; the TTL only matters when the process dies in the middle of one.
	SyncLease time.Duration
}

func (c InventoryConfig) withDefaults() InventoryConfig {
	if c.CallTimeout <= 0 {
		c.CallTimeout = 30 * time.Second
	}
	if c.SyncLease <= 0 {
		c.SyncLease = 2 * time.Minute
	}
	if c.Holder == "" {
		c.Holder = uuid.NewString()
	}
	return c
}

// InventoryDeps are the collaborators of an InventoryService.
type InventoryDeps struct {
	Connections domain.ConnectionRepository
	Links       domain.CatalogLinkRepository
	Outbox      domain.OutboxRepository
	Leases      domain.LeaseRepository
	Store       domain.AvailabilityStore
	Clients     ClientProvider
	Clock       domain.Clock
	Config      InventoryConfig
	// Notifier is optional.
	Notifier ProductNotifier
	// Wake, optional, asks the outbox worker to drain now.
	Wake func()
}

// InventoryService keeps product availability and HubRise inventory in line (section 8 of the plan).
//
// Inbound, a sync reads GET /location/inventory and applies it to the linked products and choices
// under the provenance rule: the system takes availability away only when the row is available (or
// HubRise already disabled it), remembers that on the link (inventory_sold_out), and gives back only
// what it took. Hand-set unavailability is never flipped back. Outbound, a staff toggle clears that
// provenance and queues a PATCH whose content is decided at send time. Inbound changes are written
// directly to the tables and never go through the hooks, so nothing echoes back to HubRise.
type InventoryService struct {
	d   InventoryDeps
	cfg InventoryConfig
}

// NewInventoryService returns the service.
func NewInventoryService(d InventoryDeps) *InventoryService {
	return &InventoryService{d: d, cfg: d.Config.withDefaults()}
}

// InventoryResult counts what a sync did.
type InventoryResult struct {
	// SoldOut: links newly sold out by inventory (the local row became unavailable, or was already
	// disabled by HubRise).
	SoldOut int
	// Released: links given back (the local row became available again).
	Released int
	// Recorded: links whose stock was stored without any change of availability.
	Recorded int
	// BaselinePushed: unavailable products pushed to HubRise by the first-enable baseline.
	BaselinePushed int
}

// active returns the connection when the inventory may be synchronized now: connected, switched on,
// no authorization error, no relink pending, and the inventory toggle on. A nil connection with a nil
// error means "do nothing".
func (s *InventoryService) active(ctx context.Context) (*domain.Connection, error) {
	conn, err := s.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !conn.Active() || !conn.SyncInventory {
		return nil, nil
	}
	return conn, nil
}

// tracking is active for the shop's own decisions: it also answers while an authorization error
// pauses the pipeline. What staff change in that time is queued like any other change (the outbox is
// not claimed while the pause lasts) and goes out once the connection works again; dropping it would
// leave HubRise and the shop apart until the next toggle.
func (s *InventoryService) tracking(ctx context.Context) (*domain.Connection, error) {
	conn, err := s.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !conn.Connected() || !conn.Enabled || !conn.SyncInventory || conn.RelinkRequiredAt != nil {
		return nil, nil
	}
	return conn, nil
}

func (s *InventoryService) lock(ctx context.Context) (release func(), err error) {
	// The holder is unique per call: the lease treats its own holder as a renewal, so a shared one
	// would let two syncs of this process run at once.
	holder := s.cfg.Holder + ":" + uuid.NewString()
	ok, err := s.d.Leases.Acquire(ctx, LeaseInventorySync, holder, s.d.Clock.Now(), s.cfg.SyncLease)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInventoryBusy
	}
	return func() {
		if err := s.d.Leases.Release(context.WithoutCancel(ctx), LeaseInventorySync, holder); err != nil {
			logging.FromContext(ctx).Warn("hubrise: cannot release the inventory lease", zap.Error(err))
		}
	}, nil
}

func (s *InventoryService) notify(ctx context.Context, productID uuid.UUID) {
	if s.d.Notifier != nil && productID != uuid.Nil {
		s.d.Notifier.PublishProductUpdated(ctx, productID)
	}
}

// ---- inbound ----

// Sync reads the inventory from HubRise and applies it. background marks the HubRise call as
// background work for the rate limiter (the 30 minute poll); a sync driven by an event is not. It does
// nothing while the integration is paused, disconnected or does not sync inventory.
func (s *InventoryService) Sync(ctx context.Context, background bool) (InventoryResult, error) {
	var res InventoryResult
	conn, err := s.active(ctx)
	if err != nil || conn == nil {
		return res, err
	}
	release, err := s.lock(ctx)
	if err != nil {
		return res, err
	}
	defer release()

	client, err := s.d.Clients.Client(ctx, conn)
	if err != nil {
		return res, err
	}
	rctx := ctx
	if background {
		rctx = hubrise.Background(ctx)
	}
	rctx, cancel := context.WithTimeout(rctx, s.cfg.CallTimeout)
	entries, err := client.GetInventory(rctx)
	cancel()
	if err != nil {
		return res, s.callFailed(ctx, err)
	}

	// The shop's own decisions go first: until the baseline is stamped an absent entry restores
	// nothing, because the shop may have sold things out that HubRise has never heard of.
	restoreAbsent := conn.InventoryBaselineAt != nil
	var errs []error
	if !restoreAbsent {
		n, err := s.baseline(ctx)
		res.BaselinePushed = n
		if err != nil {
			errs = append(errs, err)
		}
		restoreAbsent = err == nil
	}
	if err := s.apply(ctx, entries, restoreAbsent, &res); err != nil {
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}

// callFailed records a connection-level refusal (the pipeline pauses) and returns the error.
func (s *InventoryService) callFailed(ctx context.Context, err error) error {
	var he *hubrise.Error
	if errors.As(err, &he) && he.ConnectionLevel() {
		logging.FromContext(ctx).Error("hubrise: connection refused, pipeline paused", zap.Error(he))
		if rerr := s.d.Connections.RecordAuthError(context.WithoutCancel(ctx), s.d.Clock.Now(), truncate(he.Error(), 500)); rerr != nil {
			return errors.Join(err, rerr)
		}
	}
	return err
}

// stockOf reads the stock of an entry: nil for an entry without stock figure.
func stockOf(e *hubrise.InventoryEntry) (*decimal.Decimal, error) {
	if e == nil || e.Stock == nil {
		return nil, nil
	}
	d, err := hubrise.ParseStock(*e.Stock)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// apply applies the entries to every linked product and choice.
func (s *InventoryService) apply(ctx context.Context, entries []hubrise.InventoryEntry, restoreAbsent bool, res *InventoryResult) error {
	skus := map[string]*hubrise.InventoryEntry{}
	options := map[string]*hubrise.InventoryEntry{}
	for i := range entries {
		switch e := &entries[i]; {
		case e.SkuRef != "":
			skus[e.SkuRef] = e
		case e.OptionRef != "":
			options[e.OptionRef] = e
		}
	}
	// A product whose state the shop changed and whose PATCH is still on its way is the shop's to
	// decide: HubRise has not heard of it yet, so what it says now is stale.
	inflight, err := s.d.Outbox.ActiveDedupeKeys(ctx, domain.OutboxInventory)
	if err != nil {
		return err
	}

	var errs []error
	for _, t := range []struct {
		entity  domain.EntityType
		entries map[string]*hubrise.InventoryEntry
	}{{domain.EntityProduct, skus}, {domain.EntityChoice, options}} {
		links, err := s.d.Links.ListByType(ctx, t.entity, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ids := make([]uuid.UUID, 0, len(links))
		for i := range links {
			ids = append(ids, links[i].LocalID())
		}
		avail, err := s.d.Store.Availability(ctx, t.entity, ids)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for i := range links {
			l := &links[i]
			if t.entity == domain.EntityProduct && inflight[domain.InventoryDedupeKey(l.HubriseRef)] {
				continue
			}
			a, ok := avail[l.LocalID()]
			if !ok {
				continue // the local row vanished; its link goes with it
			}
			if err := s.applyLink(ctx, l, t.entries[l.HubriseRef], a, restoreAbsent, res); err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", l.EntityType, l.HubriseRef, err))
			}
		}
	}
	return errors.Join(errs...)
}

// sameStock reports whether two stock figures are equal (nil is "no figure").
func sameStock(a, b *decimal.Decimal) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// applyLink applies the entry of one link (nil when HubRise has none). The rules, in short:
//   - stock 0: if the link already owns the sold-out state, refresh stock and expiry; if the local row
//     is available (or HubRise itself disabled it) take the state over; otherwise the row is
//     unavailable for a local reason, so only the figures are recorded.
//   - positive stock, or no entry: give back only what inventory took. An absent entry gives back
//     nothing until the baseline is stamped.
func (s *InventoryService) applyLink(ctx context.Context, l *domain.CatalogLink, e *hubrise.InventoryEntry, a domain.Availability, restoreAbsent bool, res *InventoryResult) error {
	stock, err := stockOf(e)
	if err != nil {
		logging.FromContext(ctx).Warn("hubrise: unreadable stock, entry ignored",
			zap.String("ref", l.HubriseRef), zap.Error(err))
		return nil
	}
	st := domain.InventoryState{Stock: stock, SyncedAt: s.d.Clock.Now()}
	soldOut := stock != nil && stock.IsZero()
	if soldOut {
		st.SoldOut, st.SoldOutUntil = true, e.ExpiresAt
	}

	switch {
	case soldOut && l.InventorySoldOut:
		return s.record(ctx, l, st, res)
	case soldOut && (a.Available || l.CatalogDisabled):
		ch, err := s.d.Store.TakeOver(ctx, l, st)
		if err != nil {
			return err
		}
		if ch.Done {
			res.SoldOut++
			if ch.Changed {
				s.notify(ctx, ch.ProductID)
			}
		}
		return nil
	case soldOut:
		st.SoldOut = false // unavailable for a local reason: recorded, not owned
		return s.record(ctx, l, st, res)
	case l.InventorySoldOut && (e != nil || restoreAbsent):
		ch, err := s.d.Store.Release(ctx, l, st)
		if err != nil {
			return err
		}
		if ch.Done {
			res.Released++
			if ch.Changed {
				s.notify(ctx, ch.ProductID)
			}
		}
		return nil
	case l.InventorySoldOut:
		return nil // absent before the baseline: not proof of anything
	}
	return s.record(ctx, l, st, res)
}

// record stores the stock figures of a link when they changed.
func (s *InventoryService) record(ctx context.Context, l *domain.CatalogLink, st domain.InventoryState, res *InventoryResult) error {
	if sameStock(l.Stock, st.Stock) && sameTime(l.SoldOutUntil, st.SoldOutUntil) {
		return nil
	}
	if err := s.d.Links.RecordStock(ctx, l.ID, st.Stock, st.SoldOutUntil, st.SyncedAt); err != nil {
		return err
	}
	res.Recorded++
	return nil
}

// ---- baseline ----

// Baseline pushes the shop's existing sold-out products to HubRise and stamps the baseline, the first
// time inventory sync is effective. It does nothing otherwise.
func (s *InventoryService) Baseline(ctx context.Context) (int, error) {
	conn, err := s.active(ctx)
	if err != nil || conn == nil || conn.InventoryBaselineAt != nil {
		return 0, err
	}
	release, err := s.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	return s.baseline(ctx)
}

// baseline queues stock "0" for every linked product that is unavailable in the shop (and that
// neither inventory nor the HubRise catalog made so), then stamps the baseline. Queuing is idempotent
// (pending rows of a SKU collapse), so a failure before the stamp is simply retried.
func (s *InventoryService) baseline(ctx context.Context) (int, error) {
	links, err := s.d.Links.ListByType(ctx, domain.EntityProduct, false)
	if err != nil {
		return 0, err
	}
	ids := make([]uuid.UUID, 0, len(links))
	for i := range links {
		ids = append(ids, links[i].LocalID())
	}
	avail, err := s.d.Store.Availability(ctx, domain.EntityProduct, ids)
	if err != nil {
		return 0, err
	}
	pushed := 0
	for i := range links {
		l := &links[i]
		if a, ok := avail[l.LocalID()]; !ok || a.Available || l.InventorySoldOut || l.CatalogDisabled {
			continue
		}
		if err := s.enqueueSku(ctx, l); err != nil {
			return pushed, err
		}
		pushed++
	}
	if err := s.d.Connections.SetInventoryBaseline(ctx, s.d.Clock.Now()); err != nil {
		return pushed, err
	}
	if pushed > 0 {
		logging.FromContext(ctx).Info("hubrise: inventory baseline pushed", zap.Int("sold_out_products", pushed))
	}
	return pushed, nil
}

// ---- outbound ----

// enqueueSku queues the PATCH of one SKU. The row says which product and SKU, never the stock: the
// worker reads the product's availability when it sends.
func (s *InventoryService) enqueueSku(ctx context.Context, l *domain.CatalogLink) error {
	payload := domain.InventoryPayload{ProductID: l.LocalID(), SkuRef: l.HubriseRef}.Marshal()
	now := s.d.Clock.Now()
	item := domain.NewOutboxItem(domain.OutboxInventory, nil, domain.InventoryDedupeKey(l.HubriseRef), payload, now)
	item.CreatedAt = now
	if _, _, err := s.d.Outbox.Enqueue(ctx, item); err != nil {
		return err
	}
	if s.d.Wake != nil {
		s.d.Wake()
	}
	return nil
}

// AvailabilityChanged is the hook of a sold-out toggle made by staff. Its signature is the one of
// HubriseHooks.AvailabilityChanged. It queues the PATCH first and then clears the sold-out
// provenance of the link (the shop owns that state from now on), so a failure in between leaves the
// pending PATCH to guard the product against a stale inbound answer. An unlinked product, a retired
// one, or a switched off or disconnected integration changes nothing (switching it on again pushes
// the whole state); a pause by an authorization error only delays the PATCH.
func (s *InventoryService) AvailabilityChanged(ctx context.Context, productID uuid.UUID, _ bool) error {
	conn, err := s.tracking(ctx)
	if err != nil || conn == nil {
		return err
	}
	l, err := s.d.Links.FindByLocal(ctx, domain.EntityProduct, productID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if l.RetiredAt != nil {
		return nil
	}
	if err := s.enqueueSku(ctx, l); err != nil {
		return err
	}
	if !l.InventorySoldOut {
		return nil
	}
	return s.d.Links.ClearSoldOut(ctx, l.ID)
}

// ---- expiry ----

// ReleaseExpired gives back the products and choices that inventory sold out until a time that has
// passed (HubRise announces the expiry with an inventory.patch event too; this is the safety net, run
// every minute). Only links with the provenance flag are touched. It returns how many were released.
func (s *InventoryService) ReleaseExpired(ctx context.Context) (int, error) {
	conn, err := s.active(ctx)
	if err != nil || conn == nil {
		return 0, err
	}
	release, err := s.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	now := s.d.Clock.Now()
	links, err := s.d.Links.ListSoldOutExpired(ctx, now)
	if err != nil {
		return 0, err
	}
	var errs []error
	released := 0
	for i := range links {
		ch, err := s.d.Store.Release(ctx, &links[i], domain.InventoryState{SyncedAt: now})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ch.Done {
			released++
			if ch.Changed {
				s.notify(ctx, ch.ProductID)
			}
		}
	}
	return released, errors.Join(errs...)
}
