package application

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// LeaseCatalogDrift is the cadence lease of the 6 hourly check.
const LeaseCatalogDrift = "catalog-drift"

// CatalogDriftChecker is the catalog service seen by the drift job: it fetches the HubRise catalog,
// compares its hash with the one last imported and then either imports it (automatic import on, and
// never a mass retire) or only records that an update is available. The job here only decides when
// to ask. The call is a background one: ctx carries hubrise.Background.
type CatalogDriftChecker interface {
	CheckDrift(ctx context.Context) error
}

// CatalogDriftConfig tunes the drift job; zero values take the defaults of the plan.
type CatalogDriftConfig struct {
	// Holder identifies this process in the lease table.
	Holder string
	// Interval is the cadence of the unconditional check (6 hours).
	Interval time.Duration
	// Tick is how often the job looks for catalog events to act on (30 seconds).
	Tick time.Duration
	// Debounce is how old the oldest pending catalog event must be before they all collapse into
	// one check (30 seconds): a burst of edits in the HubRise back office costs one import.
	Debounce time.Duration
}

func (c CatalogDriftConfig) withDefaults() CatalogDriftConfig {
	if c.Interval <= 0 {
		c.Interval = 6 * time.Hour
	}
	if c.Tick <= 0 {
		c.Tick = 30 * time.Second
	}
	if c.Debounce <= 0 {
		c.Debounce = 30 * time.Second
	}
	if c.Holder == "" {
		c.Holder = uuid.NewString()
	}
	return c
}

// CatalogDriftDeps are the collaborators of a CatalogDrift job.
type CatalogDriftDeps struct {
	Connections domain.ConnectionRepository
	Events      domain.EventRepository
	Leases      domain.LeaseRepository
	Checker     CatalogDriftChecker
	// Publish, optional, takes over while the dashboard owns the menu: catalog events then only
	// record that HubRise was edited elsewhere.
	Publish PublishWatcher
	Clock   domain.Clock
	Config  CatalogDriftConfig
}

// CatalogDrift notices that the HubRise catalog changed: every 6 hours, and 30 seconds after the first
// of a burst of catalog callback events. The debounce is backed by the database, not by memory: the
// events carry their arrival time, whichever replica looks first collapses all the due ones into one
// check, and the other finds nothing left. Events that arrive while the integration is paused are
// dropped at the door; the 6 hourly check covers them.
type CatalogDrift struct {
	d      CatalogDriftDeps
	cfg    CatalogDriftConfig
	hourly periodicJob
}

// NewCatalogDrift returns the job.
func NewCatalogDrift(d CatalogDriftDeps) *CatalogDrift {
	cfg := d.Config.withDefaults()
	c := &CatalogDrift{d: d, cfg: cfg}
	c.hourly = periodicJob{
		name: LeaseCatalogDrift, holder: cfg.Holder, interval: cfg.Interval, leases: d.Leases, clock: d.Clock,
		run: c.check,
	}
	return c
}

// active reports whether the integration may talk to HubRise now, and whether the dashboard owns the
// menu.
func (c *CatalogDrift) active(ctx context.Context) (active, publishing bool, err error) {
	conn, err := c.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return conn.Active(), conn.CatalogDirection() == domain.DirectionPublish, nil
}

// check asks the catalog service for a drift check, as background work. The check runs whether
// automatic import is on or not: with it off the service only records that an update is available.
func (c *CatalogDrift) check(ctx context.Context) error {
	ok, _, err := c.active(ctx)
	if err != nil || !ok {
		return err
	}
	return c.d.Checker.CheckDrift(hubrise.Background(ctx))
}

// CheckScheduled runs the unconditional check if this process can take the cadence lease.
func (c *CatalogDrift) CheckScheduled(ctx context.Context) (bool, error) { return c.hourly.tick(ctx) }

// CheckEvents collapses the catalog events that are old enough into one check. It returns whether a
// check ran. The check is not repeated for the events it consumed if it fails: the 6 hourly check is
// the backstop.
func (c *CatalogDrift) CheckEvents(ctx context.Context) (bool, error) {
	ok, publishing, err := c.active(ctx)
	if err != nil || !ok {
		return false, err
	}
	oldest, err := c.d.Events.OldestPendingReceivedAt(ctx, domain.CatalogEventResource)
	if err != nil || oldest == nil {
		return false, err
	}
	now := c.d.Clock.Now()
	if now.Sub(*oldest) < c.cfg.Debounce {
		return false, nil
	}
	if publishing {
		return c.noteExternal(ctx, *oldest, now)
	}
	// One statement marks the due events done and counts them: of two replicas looking at once, one
	// gets the events and the other finds none.
	n, err := c.d.Events.CollapsePending(ctx, domain.CatalogEventResource, now.Add(-c.cfg.Debounce), now)
	if err != nil || n == 0 {
		return false, err
	}
	logging.FromContext(ctx).Info("hubrise: catalog changed, checking for drift", zap.Int64("events", n))
	return true, c.check(ctx)
}

// noteExternal consumes the due catalog events of a published catalog: they cost no HubRise call,
// they only say somebody edited the catalog (or deleted it).
func (c *CatalogDrift) noteExternal(ctx context.Context, oldest, now time.Time) (bool, error) {
	cutoff := now.Add(-c.cfg.Debounce)
	types, err := c.d.Events.PendingEventTypes(ctx, domain.CatalogEventResource, cutoff)
	if err != nil {
		return false, err
	}
	n, err := c.d.Events.CollapsePending(ctx, domain.CatalogEventResource, cutoff, now)
	if err != nil || n == 0 {
		return false, err
	}
	if c.d.Publish == nil {
		return true, nil
	}
	return true, c.d.Publish.NoteExternalChange(ctx, slices.Contains(types, "delete"), oldest)
}

// Run checks until ctx ends: once at start (the 6 hourly job), on its cadence, and every tick for
// catalog events.
func (c *CatalogDrift) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.hourly.loop(ctx)
	}()
	ticker := time.NewTicker(c.cfg.Tick)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
		}
		if _, err := c.CheckEvents(ctx); err != nil {
			logging.FromContext(ctx).Error("hubrise: catalog event check failed", zap.Error(err))
		}
	}
	wg.Wait()
}
