package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// ResourceOrder is the resource type of the events this processor handles.
const ResourceOrder = "order"

// EventProcessorConfig tunes the event processor; zero values take the defaults of the plan.
type EventProcessorConfig struct {
	// BatchSize is how many events one claim leases (10).
	BatchSize int
	// Lease is how long a claimed event stays ours (2 minutes).
	Lease time.Duration
	// Tick is the poll interval; the wake-up channel makes a new event go out sooner (5 seconds).
	Tick time.Duration
	// CallTimeout bounds one HubRise read (30 s).
	CallTimeout time.Duration
	// DeferWindow is how long an event for an unknown order may wait for our own push to be
	// recorded before the order is looked up at HubRise instead (10 minutes).
	DeferWindow time.Duration
	// ReadsPerMinute caps the order reads the processor makes; further events are deferred (60).
	ReadsPerMinute int
	// StaleMargin and PatchWindow are the tolerances of the ordering guard (5 seconds each).
	StaleMargin, PatchWindow time.Duration
	// BackoffBase and BackoffMax are the retry delay of a failed event (30 s doubling to 30 min).
	BackoffBase, BackoffMax time.Duration
	// MaxAttempts is how many times an event is tried before it is marked failed (8).
	MaxAttempts int
}

func (c EventProcessorConfig) withDefaults() EventProcessorConfig {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Lease, 2*time.Minute)
	def(&c.Tick, 5*time.Second)
	def(&c.CallTimeout, 30*time.Second)
	def(&c.DeferWindow, 10*time.Minute)
	def(&c.StaleMargin, 5*time.Second)
	def(&c.PatchWindow, 5*time.Second)
	def(&c.BackoffBase, 30*time.Second)
	def(&c.BackoffMax, 30*time.Minute)
	if c.BatchSize <= 0 {
		c.BatchSize = 10
	}
	if c.ReadsPerMinute <= 0 {
		c.ReadsPerMinute = 60
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 8
	}
	return c
}

// EventProcessorDeps are the collaborators of an EventProcessor.
type EventProcessorDeps struct {
	Connections domain.ConnectionRepository
	Orders      domain.OrderLinkRepository
	Events      domain.EventRepository
	Foreign     domain.ForeignOrderRepository
	Clients     ClientProvider
	Sync        *StatusSync
	Clock       domain.Clock
	Config      EventProcessorConfig
}

// EventProcessor applies the order events HubRise sent to the local orders. The callback handler only
// stores them; this does the work, one event at a time, and can run on both replicas at once (the
// claim is FOR UPDATE SKIP LOCKED and the change of an order is serialized by a lease).
type EventProcessor struct {
	d    EventProcessorDeps
	cfg  EventProcessorConfig
	wake chan struct{}

	readsMu sync.Mutex
	reads   []time.Time
}

// NewEventProcessor returns the processor.
func NewEventProcessor(d EventProcessorDeps) *EventProcessor {
	return &EventProcessor{d: d, cfg: d.Config.withDefaults(), wake: make(chan struct{}, 1)}
}

// Wake asks the processor to run now. It never blocks and a pending wake-up absorbs the next ones.
func (p *EventProcessor) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run processes events until ctx ends: on every wake-up and every tick.
func (p *EventProcessor) Run(ctx context.Context) {
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

// drain claims batches until one comes back short, with a cap so a flood cannot starve the loop's
// shutdown check.
func (p *EventProcessor) drain(ctx context.Context) {
	for range 100 {
		if ctx.Err() != nil {
			return
		}
		n, err := p.ProcessOnce(ctx)
		if err != nil {
			logging.FromContext(ctx).Error("hubrise: event processing failed", zap.Error(err))
			return
		}
		if n < p.cfg.BatchSize {
			return
		}
	}
}

// run is the state shared by the events of one claim.
type run struct {
	conn   *domain.Connection
	client *hubrise.Client
	// paused is set when HubRise refused the connection: the rest of the batch is handed back.
	paused bool
}

// ProcessOnce claims one batch of order events and processes it. It returns how many it claimed.
// While the integration is paused, or does not sync statuses, nothing is claimed: the events wait.
func (p *EventProcessor) ProcessOnce(ctx context.Context) (int, error) {
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
	events, err := p.d.Events.Claim(ctx, p.d.Clock.Now(), p.cfg.Lease, p.cfg.BatchSize, ResourceOrder)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	r := &run{conn: conn}
	for _, ev := range events {
		if r.paused || ctx.Err() != nil {
			p.logIf(ctx, "hand an event back", p.d.Events.Defer(context.WithoutCancel(ctx), ev.ID, p.d.Clock.Now()))
			continue
		}
		p.handle(ctx, r, ev)
	}
	return len(events), nil
}

func (p *EventProcessor) logIf(ctx context.Context, what string, err error) {
	if err != nil {
		logging.FromContext(ctx).Error("hubrise: "+what+" failed", zap.Error(err))
	}
}

// ---- settling an event ----

func (p *EventProcessor) done(ctx context.Context, ev domain.Event) {
	p.logIf(ctx, "mark event done", p.d.Events.MarkDone(ctx, ev.ID, p.d.Clock.Now()))
}

func (p *EventProcessor) ignore(ctx context.Context, ev domain.Event, reason string) {
	logging.FromContext(ctx).Info("hubrise: event ignored", zap.String("event_id", ev.ID), zap.String("reason", reason))
	p.logIf(ctx, "mark event ignored", p.d.Events.MarkIgnored(ctx, ev.ID, p.d.Clock.Now(), reason))
}

func (p *EventProcessor) fail(ctx context.Context, ev domain.Event, reason string) {
	logging.FromContext(ctx).Error("hubrise: event failed", zap.String("event_id", ev.ID), zap.String("reason", reason))
	p.logIf(ctx, "mark event failed", p.d.Events.MarkFailed(ctx, ev.ID, p.d.Clock.Now(), truncate(reason, 500)))
}

// deferTo keeps the event pending, due at next, without counting the claim as an attempt.
func (p *EventProcessor) deferTo(ctx context.Context, ev domain.Event, next time.Time) {
	p.logIf(ctx, "defer event", p.d.Events.Defer(ctx, ev.ID, next))
}

// retry schedules another attempt with backoff, or fails the event for good when it has used all of
// them. It reports whether the event was failed for good.
func (p *EventProcessor) retry(ctx context.Context, ev domain.Event, err error) bool {
	reason := truncate(err.Error(), 500)
	if ev.Attempts >= p.cfg.MaxAttempts {
		p.fail(ctx, ev, "giving up: "+reason)
		return true
	}
	d := p.cfg.BackoffBase
	for i := 1; i < ev.Attempts && d < p.cfg.BackoffMax; i++ {
		d *= 2
	}
	d = min(d, p.cfg.BackoffMax)
	logging.FromContext(ctx).Warn("hubrise: event will be retried",
		zap.String("event_id", ev.ID), zap.Int("attempt", ev.Attempts), zap.Duration("in", d), zap.Error(err))
	p.logIf(ctx, "retry event", p.d.Events.RetryLater(ctx, ev.ID, p.d.Clock.Now().Add(d), reason))
	return false
}

// ---- reading orders ----

// takeRead reserves one of the order reads the processor may make this minute.
func (p *EventProcessor) takeRead(now time.Time) bool {
	p.readsMu.Lock()
	defer p.readsMu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := p.reads[:0]
	for _, t := range p.reads {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	p.reads = kept
	if len(p.reads) >= p.cfg.ReadsPerMinute {
		return false
	}
	p.reads = append(p.reads, now)
	return true
}

// readOrder reads the order at HubRise. The returned order is nil when the event was settled instead
// (deferred for the read budget, ignored because the order is gone, retried after an error).
func (p *EventProcessor) readOrder(ctx context.Context, r *run, ev domain.Event, hubriseOrderID string) *hubrise.Order {
	now := p.d.Clock.Now()
	if !p.takeRead(now) {
		p.deferTo(ctx, ev, now.Add(15*time.Second))
		return nil
	}
	if r.client == nil {
		client, err := p.d.Clients.Client(ctx, r.conn)
		if err != nil {
			p.retry(ctx, ev, err)
			return nil
		}
		r.client = client
	}
	rctx, cancel := context.WithTimeout(ctx, p.cfg.CallTimeout)
	defer cancel()
	remote, err := r.client.GetOrder(rctx, hubriseOrderID)
	var he *hubrise.Error
	switch {
	case err == nil:
		return remote
	case errors.As(err, &he) && he.ConnectionLevel():
		msg := truncate(he.Error(), 500)
		logging.FromContext(ctx).Error("hubrise: connection refused, pipeline paused", zap.Error(he))
		p.logIf(ctx, "record auth error", p.d.Connections.RecordAuthError(context.WithoutCancel(ctx), p.d.Clock.Now(), msg))
		r.paused = true
		p.deferTo(ctx, ev, p.d.Clock.Now())
	case hubrise.KindOf(err) == hubrise.KindNotFound:
		p.ignore(ctx, ev, "the order no longer exists at HubRise")
	default:
		p.retry(ctx, ev, err)
	}
	return nil
}

// ---- one event ----

func (p *EventProcessor) handle(ctx context.Context, r *run, ev domain.Event) {
	var parsed hubrise.Event
	if err := json.Unmarshal(ev.Payload, &parsed); err != nil || parsed.ID == "" {
		p.fail(ctx, ev, "unreadable event payload")
		return
	}
	switch {
	case !r.conn.SyncStatus:
		p.ignore(ctx, ev, "status sync is off")
		return
	case !parsed.Is(ResourceOrder, "update"):
		p.ignore(ctx, ev, "this event is not used")
		return
	}
	hubriseOrderID := parsed.OrderID
	if hubriseOrderID == "" {
		if o, err := parsed.NewOrder(); err == nil {
			hubriseOrderID = o.ID
		}
	}
	if hubriseOrderID == "" {
		p.ignore(ctx, ev, "event without an order id")
		return
	}

	link, err := p.d.Orders.FindByHubriseID(ctx, hubriseOrderID)
	var fresh *hubrise.Order
	switch {
	case errors.Is(err, domain.ErrNotFound):
		link, fresh = p.identify(ctx, r, ev, hubriseOrderID)
		if link == nil {
			return // settled by identify
		}
	case err != nil:
		p.retry(ctx, ev, err)
		return
	}

	remote, eventAt := p.guard(ctx, r, ev, parsed, link, fresh, hubriseOrderID)
	if remote == nil {
		return // settled by guard
	}
	err = p.d.Sync.apply(ctx, r.conn, link, remote, eventAt)
	switch {
	case errors.Is(err, errOrderBusy):
		p.deferTo(ctx, ev, p.d.Clock.Now().Add(5*time.Second))
	case errors.Is(err, errStatusQueued):
		p.deferTo(ctx, ev, p.d.Clock.Now().Add(15*time.Second)) // after the queued PATCH went out
	case err != nil:
		if p.retry(ctx, ev, err) && (remote.Status == hubrise.StatusCancelled || remote.Status == hubrise.StatusRejected) {
			p.d.Sync.flagCancelFailed(ctx, link.OrderID, err) // out of attempts: staff must look at it
		}
	default:
		p.done(ctx, ev)
	}
}

// identify finds the local order of an event whose HubRise order id is not recorded. It returns the
// link, and the order as read from HubRise when it had to be read; a nil link means the event was
// settled (ignored, deferred or retried).
func (p *EventProcessor) identify(ctx context.Context, r *run, ev domain.Event, hubriseOrderID string) (*domain.OrderLink, *hubrise.Order) {
	foreign, err := p.d.Foreign.IsForeign(ctx, hubriseOrderID)
	if err != nil {
		p.retry(ctx, ev, err)
		return nil, nil
	}
	if foreign {
		p.ignore(ctx, ev, "the order belongs to another sales channel")
		return nil, nil
	}

	// Race with our own push: the answer to the POST may not be stored yet, or may have been lost.
	// Wait for it rather than treat the order as foreign.
	now := p.d.Clock.Now()
	if age := now.Sub(ev.ReceivedAt); age < p.cfg.DeferWindow {
		busy, err := p.d.Orders.HasUnsettledCreate(ctx)
		if err != nil {
			p.retry(ctx, ev, err)
			return nil, nil
		}
		if busy {
			p.deferTo(ctx, ev, now.Add(max(15*time.Second, min(age/4, 2*time.Minute))))
			return nil, nil
		}
	}

	remote := p.readOrder(ctx, r, ev, hubriseOrderID)
	if remote == nil {
		return nil, nil
	}
	notOurs := func(why string) {
		p.logIf(ctx, "remember foreign order", p.d.Foreign.Mark(ctx, hubriseOrderID, p.d.Clock.Now()))
		p.ignore(ctx, ev, why)
	}
	orderID, err := uuid.Parse(remote.PrivateRef)
	if err != nil {
		notOurs("the order has no private_ref of ours")
		return nil, nil
	}
	link, err := p.d.Orders.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		notOurs("the private_ref names no order we pushed")
		return nil, nil
	}
	if err != nil {
		p.retry(ctx, ev, err)
		return nil, nil
	}
	if link.HubriseOrderID != nil && *link.HubriseOrderID != hubriseOrderID {
		logging.FromContext(ctx).Warn("hubrise: a second HubRise order carries the private_ref of one of ours",
			zap.String("order_id", orderID.String()), zap.String("known", *link.HubriseOrderID), zap.String("other", hubriseOrderID))
		notOurs("another HubRise order with the same private_ref")
		return nil, nil
	}
	if link.HubriseOrderID == nil {
		// Our push reached HubRise and its answer never made it back: adopt the order. A create still
		// queued finds it adopted and only marks its row sent.
		if err := p.d.Orders.MarkSent(ctx, orderID, hubriseOrderID, string(remote.Status.Canonical()), now); err != nil {
			p.retry(ctx, ev, err)
			return nil, nil
		}
		logging.FromContext(ctx).Info("hubrise: order recognised through its private_ref",
			zap.String("order_id", orderID.String()), zap.String("hubrise_order_id", hubriseOrderID))
		if link, err = p.d.Orders.Get(ctx, orderID); err != nil {
			p.retry(ctx, ev, err)
			return nil, nil
		}
	}
	return link, remote
}

// guard decides which state of the order to trust (section 7.2 of the plan). Clocks differ, HubRise
// timestamps have a second of precision and our own PATCHes generate no event for our client, so an
// event that is not clearly newer than what we know, or that predates or falls next to a PATCH of
// ours, or that carries no state, is not trusted: the order is read again and what HubRise has now wins. An event
// clearly older than what we know is dropped. The returned order is nil when the event was settled.
func (p *EventProcessor) guard(ctx context.Context, r *run, ev domain.Event, parsed hubrise.Event, link *domain.OrderLink, fresh *hubrise.Order, hubriseOrderID string) (*hubrise.Order, *time.Time) {
	at := parsed.CreatedAt
	var eventAt *time.Time
	if !at.IsZero() {
		eventAt = &at
	}
	if fresh != nil {
		return fresh, eventAt // read a moment ago to identify the order
	}
	payload, perr := parsed.NewOrder()
	usable := perr == nil && payload.Status.Valid() && !at.IsZero()
	// An event from before our last PATCH is stale however long the PATCH waited in the queue: the
	// payload shows a state we already moved past, and applying it would move the order back.
	nearPatch := link.LastLocalPatchAt != nil && (at.Before(*link.LastLocalPatchAt) || at.Sub(*link.LastLocalPatchAt) <= p.cfg.PatchWindow)
	last := link.LastRemoteEventAt

	switch {
	case !usable || nearPatch:
	case last != nil && at.Before(last.Add(-p.cfg.StaleMargin)):
		p.ignore(ctx, ev, "older than what is already known")
		return nil, nil
	case last != nil && !at.After(*last):
	default:
		return payload, eventAt
	}
	remote := p.readOrder(ctx, r, ev, hubriseOrderID)
	if remote == nil {
		return nil, nil
	}
	return remote, eventAt
}
