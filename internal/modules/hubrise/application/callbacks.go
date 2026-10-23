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

// Why a callback request is refused. The HTTP handler maps them to status codes.
var (
	// ErrBadSignature: the signature is missing or does not match the body.
	ErrBadSignature = errors.New("hubrise: invalid callback signature")
	// ErrBadEvent: the body is not an event.
	ErrBadEvent = errors.New("hubrise: malformed callback event")
	// ErrNotConnected: this installation has no HubRise connection to receive for.
	ErrNotConnected = errors.New("hubrise: no connection for this callback")
	// ErrWrongLocation: the event is about another HubRise location than ours.
	ErrWrongLocation = errors.New("hubrise: the event is about another location")
)

// ReceiveOutcome tells what became of an accepted callback event.
type ReceiveOutcome int

const (
	// EventStored: new event, queued for the processor.
	EventStored ReceiveOutcome = iota
	// EventDuplicate: HubRise sent it before (a retry after a slow answer).
	EventDuplicate
	// EventDropped: acknowledged but not kept: the integration is paused, or the event is of a kind
	// we never registered for.
	EventDropped
)

// CallbackReceiver is the application side of POST /hubrise/callback: it authenticates the request,
// checks it is about our location, and stores the event. Nothing else happens in the request;
// processing is asynchronous, and the answer must come well within HubRise's 20 seconds.
type CallbackReceiver struct {
	secret string
	conns  domain.ConnectionRepository
	events domain.EventRepository
	clock  domain.Clock
	wake   []func()
}

// NewCallbackReceiver returns the receiver. secret is the HubRise client secret the signature is
// keyed with; wake are called after a new event was stored (to wake the processors).
func NewCallbackReceiver(secret string, conns domain.ConnectionRepository, events domain.EventRepository, clock domain.Clock, wake ...func()) *CallbackReceiver {
	return &CallbackReceiver{secret: secret, conns: conns, events: events, clock: clock, wake: wake}
}

// AddWake registers another function to call after a new event was stored (the inventory processor
// is built after the receiver). It must be called before the receiver serves requests.
func (r *CallbackReceiver) AddWake(w ...func()) { r.wake = append(r.wake, w...) }

// Receive handles the raw body of a callback request and the value of its signature header.
func (r *CallbackReceiver) Receive(ctx context.Context, body []byte, signature string) (ReceiveOutcome, error) {
	if !hubrise.VerifySignature(r.secret, body, signature) {
		return EventDropped, ErrBadSignature
	}
	ev, err := hubrise.ParseEvent(body)
	if err != nil {
		return EventDropped, fmt.Errorf("%w: %w", ErrBadEvent, err)
	}
	conn, err := r.conns.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && !conn.Connected()) {
		return EventDropped, ErrNotConnected
	}
	if err != nil {
		return EventDropped, err
	}
	if conn.LocationID == nil || ev.LocationID != *conn.LocationID {
		return EventDropped, ErrWrongLocation
	}
	if !conn.Active() || !hubrise.ValidCallbackEvent(ev.ResourceType, ev.EventType) {
		// Paused: the reconciler and the drift checks bring the shop up to date when it resumes.
		// Acknowledged, or HubRise would retry for half an hour.
		return EventDropped, nil
	}
	stored, err := r.store(ctx, ev, body)
	if err != nil {
		return EventDropped, err
	}
	if !stored {
		return EventDuplicate, nil
	}
	if err := r.conns.TouchLastEvent(ctx, r.clock.Now()); err != nil {
		logging.FromContext(ctx).Warn("hubrise: cannot stamp the last event", zap.Error(err))
	}
	for _, w := range r.wake {
		w()
	}
	return EventStored, nil
}

func (r *CallbackReceiver) store(ctx context.Context, ev *hubrise.Event, body []byte) (bool, error) {
	return r.events.Insert(ctx, domain.Event{
		ID: ev.ID, ResourceType: ev.ResourceType, EventType: ev.EventType,
		Payload: json.RawMessage(body), ReceivedAt: r.clock.Now(),
	})
}

// LeaseCallbackPoller is the name of the lease that makes the callback poller a singleton.
const LeaseCallbackPoller = "callback-poller"

// CallbackPollerConfig tunes the poller; zero values take the defaults of the plan.
type CallbackPollerConfig struct {
	Holder string
	// Interval is the cadence of the poll (10 minutes; HubRise allows one every 30 seconds).
	Interval time.Duration
	// PageSize is the page size of the event list (100) and MaxPages caps how many pages one poll
	// reads (10).
	PageSize, MaxPages int
	// CallTimeout bounds one HubRise call (30 s).
	CallTimeout time.Duration
}

func (c CallbackPollerConfig) withDefaults() CallbackPollerConfig {
	if c.Interval <= 0 {
		c.Interval = 10 * time.Minute
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = 30 * time.Second
	}
	if c.PageSize <= 0 {
		c.PageSize = 100
	}
	if c.MaxPages <= 0 {
		c.MaxPages = 10
	}
	if c.Holder == "" {
		c.Holder = uuid.NewString()
	}
	return c
}

// CallbackPollerDeps are the collaborators of a CallbackPoller.
type CallbackPollerDeps struct {
	Connections domain.ConnectionRepository
	Events      domain.EventRepository
	Leases      domain.LeaseRepository
	Clients     ClientProvider
	Clock       domain.Clock
	Config      CallbackPollerConfig
	// Wake are called after events were stored.
	Wake []func()
}

// CallbackPoller is the fallback of the callback: every ten minutes, by whichever replica holds its
// lease, it lists the events HubRise could not deliver (or gave up on), stores the ones we do not
// have and acknowledges them. Events listed this way carry no state, so the processor reads the
// order again, which is the authoritative path anyway.
type CallbackPoller struct {
	d   CallbackPollerDeps
	cfg CallbackPollerConfig
}

// NewCallbackPoller returns the poller.
func NewCallbackPoller(d CallbackPollerDeps) *CallbackPoller {
	return &CallbackPoller{d: d, cfg: d.Config.withDefaults()}
}

// AddWake registers another function to call after the poll stored events, with the same rules as
// CallbackReceiver.AddWake.
func (p *CallbackPoller) AddWake(w ...func()) { p.d.Wake = append(p.d.Wake, w...) }

// Run polls until ctx ends.
func (p *CallbackPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()
	for {
		if _, err := p.Tick(ctx); err != nil {
			logging.FromContext(ctx).Error("hubrise: callback poll failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick polls once if this process can take the lease. The lease is kept for just under an interval,
// so the replicas share the cadence.
func (p *CallbackPoller) Tick(ctx context.Context) (ran bool, err error) {
	ok, err := p.d.Leases.Acquire(ctx, LeaseCallbackPoller, p.cfg.Holder, p.d.Clock.Now(), p.cfg.Interval-p.cfg.Interval/10)
	if err != nil || !ok {
		return false, err
	}
	return true, p.PollOnce(ctx)
}

// PollOnce lists, stores and acknowledges the events HubRise holds. These are background calls.
func (p *CallbackPoller) PollOnce(ctx context.Context) error {
	conn, err := p.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !conn.Active() {
		return nil
	}
	client, err := p.d.Clients.Client(ctx, conn)
	if err != nil {
		return err
	}
	bg := hubrise.Background(ctx)

	var listed []hubrise.Event
	cursor := ""
	for range p.cfg.MaxPages {
		cctx, cancel := context.WithTimeout(bg, p.cfg.CallTimeout)
		page, err := client.ListCallbackEvents(cctx, p.cfg.PageSize, cursor)
		cancel()
		if err != nil {
			return p.callFailed(ctx, err)
		}
		listed = append(listed, page.Items...)
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}

	var errs []error
	stored := 0
	for _, ev := range listed {
		keep, err := p.keep(ctx, ev)
		if err != nil {
			errs = append(errs, err)
			continue // not acknowledged: HubRise keeps it for the next poll
		}
		if keep {
			stored++
		}
		cctx, cancel := context.WithTimeout(bg, p.cfg.CallTimeout)
		err = client.AckCallbackEvent(cctx, ev.ID)
		cancel()
		if err != nil {
			errs = append(errs, p.callFailed(ctx, err))
		}
	}
	if stored > 0 {
		logging.FromContext(ctx).Info("hubrise: events recovered by polling", zap.Int("events", stored))
		for _, w := range p.d.Wake {
			w()
		}
	}
	return errors.Join(errs...)
}

// keep stores an event unless it is of a kind we never use; it reports whether a new row was stored.
func (p *CallbackPoller) keep(ctx context.Context, ev hubrise.Event) (bool, error) {
	if !hubrise.ValidCallbackEvent(ev.ResourceType, ev.EventType) {
		return false, nil
	}
	payload, _ := json.Marshal(ev) // an event that was decoded from JSON encodes again
	return p.d.Events.Insert(ctx, domain.Event{
		ID: ev.ID, ResourceType: ev.ResourceType, EventType: ev.EventType,
		Payload: payload, ReceivedAt: p.d.Clock.Now(),
	})
}

// callFailed records a connection-level refusal (the pipeline pauses) and returns the error.
func (p *CallbackPoller) callFailed(ctx context.Context, err error) error {
	var he *hubrise.Error
	if errors.As(err, &he) && he.ConnectionLevel() {
		logging.FromContext(ctx).Error("hubrise: connection refused, pipeline paused", zap.Error(he))
		if rerr := p.d.Connections.RecordAuthError(context.WithoutCancel(ctx), p.d.Clock.Now(), truncate(he.Error(), 500)); rerr != nil {
			return errors.Join(err, rerr)
		}
	}
	return err
}
