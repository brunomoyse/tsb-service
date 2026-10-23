package application

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/logging"
)

// InventoryEventsConfig tunes the inventory event processor; zero values take the defaults.
type InventoryEventsConfig struct {
	// BatchSize is how many events one claim leases (50): they all collapse into one sync.
	BatchSize int
	// Lease is how long a claimed event stays ours (2 minutes).
	Lease time.Duration
	// Tick is the poll interval; the wake-up channel makes a new event go out sooner (5 seconds).
	Tick time.Duration
	// BusyDelay is how long events wait when another replica is syncing (5 seconds).
	BusyDelay time.Duration
	// BackoffBase and BackoffMax are the retry delay of a failed sync (30 s doubling to 30 min).
	BackoffBase, BackoffMax time.Duration
	// MaxAttempts is how many times an event is tried before it is marked failed (8).
	MaxAttempts int
}

func (c InventoryEventsConfig) withDefaults() InventoryEventsConfig {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Lease, 2*time.Minute)
	def(&c.Tick, 5*time.Second)
	def(&c.BusyDelay, 5*time.Second)
	def(&c.BackoffBase, 30*time.Second)
	def(&c.BackoffMax, 30*time.Minute)
	if c.BatchSize <= 0 {
		c.BatchSize = 50
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 8
	}
	return c
}

// InventoryEventsDeps are the collaborators of an InventoryEvents processor.
type InventoryEventsDeps struct {
	Connections domain.ConnectionRepository
	Events      domain.EventRepository
	Inventory   *InventoryService
	Clock       domain.Clock
	Config      InventoryEventsConfig
}

// InventoryEvents applies the inventory callback events. Their payload is not trusted for the stock
// (the shape of an inventory.patch state_change is not documented in detail): every event means "read
// GET /location/inventory again", so a batch of events costs one read and one sync, whatever it holds.
type InventoryEvents struct {
	d    InventoryEventsDeps
	cfg  InventoryEventsConfig
	wake chan struct{}
}

// NewInventoryEvents returns the processor.
func NewInventoryEvents(d InventoryEventsDeps) *InventoryEvents {
	return &InventoryEvents{d: d, cfg: d.Config.withDefaults(), wake: make(chan struct{}, 1)}
}

// Wake asks the processor to run now. It never blocks and a pending wake-up absorbs the next ones.
func (p *InventoryEvents) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run processes events until ctx ends: on every wake-up and every tick.
func (p *InventoryEvents) Run(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.Tick)
	defer ticker.Stop()
	for {
		p.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-ticker.C:
		}
	}
}

func (p *InventoryEvents) drain(ctx context.Context) {
	for range 100 {
		if ctx.Err() != nil {
			return
		}
		n, err := p.ProcessOnce(ctx)
		if err != nil {
			logging.FromContext(ctx).Error("hubrise: inventory event processing failed", zap.Error(err))
			return
		}
		if n < p.cfg.BatchSize {
			return
		}
	}
}

// ProcessOnce claims one batch of inventory events and applies them with a single sync. It returns
// how many events it claimed. While the integration is paused nothing is claimed (the events wait);
// with inventory sync switched off the events are ignored.
func (p *InventoryEvents) ProcessOnce(ctx context.Context) (int, error) {
	conn, err := p.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !conn.Active() {
		return 0, nil
	}
	now := p.d.Clock.Now()
	events, err := p.d.Events.Claim(ctx, now, p.cfg.Lease, p.cfg.BatchSize, domain.InventoryEventResource)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	each := func(what string, f func(domain.Event) error) {
		for _, ev := range events {
			if err := f(ev); err != nil {
				logging.FromContext(ctx).Error("hubrise: "+what+" failed", zap.String("event_id", ev.ID), zap.Error(err))
			}
		}
	}
	if !conn.SyncInventory {
		each("ignore an inventory event", func(ev domain.Event) error {
			return p.d.Events.MarkIgnored(ctx, ev.ID, now, "inventory sync is off")
		})
		return len(events), nil
	}

	_, err = p.d.Inventory.Sync(ctx, false)
	switch {
	case err == nil:
		each("mark an inventory event done", func(ev domain.Event) error { return p.d.Events.MarkDone(ctx, ev.ID, p.d.Clock.Now()) })
	case errors.Is(err, ErrInventoryBusy):
		each("defer an inventory event", func(ev domain.Event) error {
			return p.d.Events.Defer(ctx, ev.ID, p.d.Clock.Now().Add(p.cfg.BusyDelay))
		})
	default:
		each("retry an inventory event", func(ev domain.Event) error { p.retry(ctx, ev, err); return nil })
	}
	return len(events), nil
}

// retry schedules another attempt with backoff, or fails the event for good when it has used all of
// them.
func (p *InventoryEvents) retry(ctx context.Context, ev domain.Event, err error) {
	reason := truncate(err.Error(), 500)
	if ev.Attempts >= p.cfg.MaxAttempts {
		logging.FromContext(ctx).Error("hubrise: inventory event failed", zap.String("event_id", ev.ID), zap.String("reason", reason))
		if merr := p.d.Events.MarkFailed(ctx, ev.ID, p.d.Clock.Now(), "giving up: "+reason); merr != nil {
			logging.FromContext(ctx).Error("hubrise: mark event failed failed", zap.Error(merr))
		}
		return
	}
	d := p.cfg.BackoffBase
	for i := 1; i < ev.Attempts && d < p.cfg.BackoffMax; i++ {
		d *= 2
	}
	d = min(d, p.cfg.BackoffMax)
	logging.FromContext(ctx).Warn("hubrise: inventory event will be retried",
		zap.String("event_id", ev.ID), zap.Int("attempt", ev.Attempts), zap.Duration("in", d), zap.Error(err))
	if rerr := p.d.Events.RetryLater(ctx, ev.ID, p.d.Clock.Now().Add(d), reason); rerr != nil {
		logging.FromContext(ctx).Error("hubrise: retry event failed", zap.Error(rerr))
	}
}
