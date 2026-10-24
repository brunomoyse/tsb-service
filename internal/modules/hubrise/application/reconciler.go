package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// LeaseReconciler is the name of the lease that makes the reconciler a singleton across replicas.
const LeaseReconciler = "reconciler"

// Schedule tells whether the ordering hours of the shop covered an instant. The composition root
// adapts the restaurant service to it.
type Schedule interface {
	OrderingOpenAt(ctx context.Context, at time.Time) (bool, error)
}

// Alerter tells the owner about a problem that needs a human (an email). It may be nil.
type Alerter interface {
	Alert(ctx context.Context, subject, body string) error
}

// CustomerEraser queues the erasure of the deleted accounts the deletion hook missed. The customer
// sync satisfies it.
type CustomerEraser interface {
	EraseMissed(ctx context.Context, limit int) error
}

// RemoteOrderHandler applies the state of an order read from HubRise to the local order. The status
// sync of the callback task implements it; until it does, the reconciler only refreshes totals.
type RemoteOrderHandler interface {
	ApplyRemote(ctx context.Context, orderID uuid.UUID, remote *hubrise.Order) error
}

// ReconcilerConfig tunes the reconciler; zero values take the defaults of the plan.
type ReconcilerConfig struct {
	// Holder identifies this process in the lease table.
	Holder string
	// Interval is the cadence of the run (2 minutes).
	Interval time.Duration
	// Grace is how far before push_orders_since an order may have been created and still be pushed:
	// it covers the orders in flight when pushing was switched on (1 hour).
	Grace time.Duration
	// RefreshPerRun is how many open orders are read back from HubRise per run (5).
	RefreshPerRun int
	// NewAlertAfter is how long an order may stay "new" at HubRise before staff are told (5 minutes).
	NewAlertAfter time.Duration
	// Batch caps the orders handled per step and run (50).
	Batch int
	// Retention is how long finished events, finished outbox rows and foreign order marks are kept (7 days).
	Retention time.Duration
}

func (c ReconcilerConfig) withDefaults() ReconcilerConfig {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Interval, 2*time.Minute)
	def(&c.Grace, time.Hour)
	def(&c.NewAlertAfter, 5*time.Minute)
	def(&c.Retention, 7*24*time.Hour)
	if c.RefreshPerRun <= 0 {
		c.RefreshPerRun = 5
	}
	if c.Batch <= 0 {
		c.Batch = 50
	}
	if c.Holder == "" {
		c.Holder = uuid.NewString()
	}
	return c
}

// ReconcilerDeps are the collaborators of a Reconciler.
type ReconcilerDeps struct {
	Connections domain.ConnectionRepository
	Orders      domain.OrderLinkRepository
	Source      domain.OrderSource
	Leases      domain.LeaseRepository
	Events      domain.EventRepository
	Outbox      domain.OutboxRepository
	Foreign     domain.ForeignOrderRepository
	OAuthStates domain.OAuthStateRepository
	Clients     ClientProvider
	Pusher      *OrderPusher
	Clock       domain.Clock
	Config      ReconcilerConfig

	// Schedule, Alerter, Remote and Customers are optional. Without a Schedule the shop counts as
	// always open.
	Schedule  Schedule
	Alerter   Alerter
	Remote    RemoteOrderHandler
	Customers CustomerEraser
}

// Reconciler is the safety net of the order push, run every two minutes by whichever replica holds
// its lease. It catches what the hooks could miss (a crash between the order commit and the enqueue,
// a status change made by a writer that bypassed the observer, an account erasure the deletion hook
// failed to queue), reads a few open orders back from HubRise, and tells staff about orders HubRise
// does not accept.
type Reconciler struct {
	d   ReconcilerDeps
	cfg ReconcilerConfig
}

// NewReconciler returns the reconciler.
func NewReconciler(d ReconcilerDeps) *Reconciler {
	return &Reconciler{d: d, cfg: d.Config.withDefaults()}
}

// SetRemote installs the handler that applies the state read back from HubRise to the local order.
// The status sync is built after the reconciler (both hang on the same repositories), so the
// composition root plugs it in here. It must be called before Run.
func (r *Reconciler) SetRemote(h RemoteOrderHandler) { r.d.Remote = h }

// Run ticks until ctx ends.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		if _, err := r.Tick(ctx); err != nil {
			logging.FromContext(ctx).Error("hubrise: reconciler failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs the reconciler once if this process can take the lease. The lease is kept (not released)
// for just under an interval, so the replicas share the cadence instead of each running every tick.
func (r *Reconciler) Tick(ctx context.Context) (ran bool, err error) {
	ok, err := r.d.Leases.Acquire(ctx, LeaseReconciler, r.cfg.Holder, r.d.Clock.Now(), r.cfg.Interval-r.cfg.Interval/10)
	if err != nil || !ok {
		return false, err
	}
	return true, r.RunOnce(ctx)
}

// RunOnce runs every step; a failing step is reported at the end and does not stop the others.
func (r *Reconciler) RunOnce(ctx context.Context) error {
	conn, err := r.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !conn.Active() {
		return nil // paused or disconnected: nothing leaves, nothing is read
	}
	var errs []error
	if conn.PushOrders {
		errs = append(errs, r.enqueueMissing(ctx, conn))
	}
	if conn.SyncStatus {
		errs = append(errs, r.statusDiff(ctx))
		errs = append(errs, r.refresh(ctx, conn))
		// Only while the status is followed: without it last_remote_status stays "new" for every
		// pushed order, whatever the EPOS did.
		errs = append(errs, r.flagStuck(ctx))
	}
	if r.d.Customers != nil {
		errs = append(errs, r.d.Customers.EraseMissed(ctx, r.cfg.Batch))
	}
	errs = append(errs, r.purge(ctx))
	return errors.Join(errs...)
}

// enqueueMissing queues the push of open orders that became actionable but have no link: the hook
// did not run (a crash right after the commit). Only orders created after push_orders_since less the
// grace are considered, never terminal ones, so switching the integration on cannot replay history.
func (r *Reconciler) enqueueMissing(ctx context.Context, conn *domain.Connection) error {
	if conn.PushOrdersSince == nil {
		return nil
	}
	ids, err := r.d.Source.OpenUnpushed(ctx, conn.PushOrdersSince.Add(-r.cfg.Grace), r.cfg.Batch)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		queued, err := r.d.Pusher.enqueueCreate(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("order %s: %w", id, err))
			continue
		}
		if queued {
			logging.FromContext(ctx).Warn("hubrise: reconciler queued an order the hook missed", zap.String("order_id", id.String()))
		}
	}
	return errors.Join(errs...)
}

// statusDiff queues the PATCH of a pushed order whose local status is not the one HubRise last had
// and that has nothing queued: a missed or aged out status row.
func (r *Reconciler) statusDiff(ctx context.Context) error {
	diffs, err := r.d.Source.StatusMismatches(ctx, StatusPairs(), r.cfg.Batch)
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range diffs {
		key := fmt.Sprintf("order:%s:reconcile:%s", l.OrderID, uuid.NewString())
		if err := r.d.Pusher.enqueueStatus(ctx, l.OrderID, l.LocalStatus, key); err != nil {
			errs = append(errs, fmt.Errorf("order %s: %w", l.OrderID, err))
			continue
		}
		logging.FromContext(ctx).Warn("hubrise: reconciler queued a missing status change",
			zap.String("order_id", l.OrderID.String()), zap.String("local", l.LocalStatus), zap.String("remote", l.LastRemoteStatus))
	}
	return errors.Join(errs...)
}

// refresh reads at most RefreshPerRun open orders back from HubRise, the ones read longest ago first:
// it stores the remote total (flagging a difference), clears the "not accepted" flag once HubRise has
// moved on, and hands a changed status to the status sync when there is one. These are background
// calls: they never eat the budget the order pushes need.
func (r *Reconciler) refresh(ctx context.Context, conn *domain.Connection) error {
	links, err := r.d.Source.RefreshCandidates(ctx, r.cfg.RefreshPerRun)
	if err != nil || len(links) == 0 {
		return err
	}
	client, err := r.d.Clients.Client(ctx, conn)
	if err != nil {
		return err
	}
	bg := hubrise.Background(ctx)
	var errs []error
	for _, l := range links {
		remote, err := client.GetOrder(bg, l.HubriseOrderID)
		var he *hubrise.Error
		switch {
		case errors.As(err, &he) && he.ConnectionLevel():
			msg := truncate(he.Error(), 500)
			logging.FromContext(ctx).Error("hubrise: connection refused, pipeline paused", zap.Error(he))
			errs = append(errs, r.d.Connections.RecordAuthError(ctx, r.d.Clock.Now(), msg))
			return errors.Join(errs...)
		case hubrise.KindOf(err) == hubrise.KindNotFound:
			errs = append(errs, r.d.Orders.FlagAttention(ctx, l.OrderID, domain.AttentionRemoteMissing))
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("order %s: %w", l.OrderID, err))
			continue
		}
		errs = append(errs, r.afterRefresh(ctx, l, remote))
	}
	return errors.Join(errs...)
}

func (r *Reconciler) afterRefresh(ctx context.Context, l domain.OpenLink, remote *hubrise.Order) error {
	if err := r.d.Orders.StampRemoteRead(ctx, l.OrderID, r.d.Clock.Now()); err != nil {
		return err
	}
	if remote.Total != "" {
		if total, err := remote.Total.Decimal(); err == nil {
			if err := r.d.Orders.SetRemoteTotal(ctx, l.OrderID, total); err != nil {
				return err
			}
			if !total.Equal(l.LocalTotal) {
				if err := r.d.Orders.FlagAttention(ctx, l.OrderID, domain.AttentionTotalMismatch); err != nil {
					return err
				}
			}
		}
	}
	status := remote.Status.Canonical()
	if status != hubrise.StatusNew && l.NeedsAttention == domain.AttentionNotAccepted {
		if err := r.d.Orders.ClearAttention(ctx, l.OrderID, domain.AttentionNotAccepted); err != nil {
			return err
		}
	}
	if r.d.Remote != nil && string(status) != l.LastRemoteStatus {
		return r.d.Remote.ApplyRemote(ctx, l.OrderID, remote)
	}
	return nil
}

// flagStuck flags the orders that HubRise still has as "new" after NewAlertAfter and, when an alerter
// is set, emails the owner once per order. It only concerns the orders that were pushed while the
// shop was taking orders: one nobody could accept at night is not a problem to wake anyone for,
// while one pushed just before closing is, and a shop that paused ordering since still wants to
// know. The alert is recorded once it went out, so a mail provider that is down is retried.
func (r *Reconciler) flagStuck(ctx context.Context) error {
	stuck, err := r.d.Source.StuckNew(ctx, r.d.Clock.Now().Add(-r.cfg.NewAlertAfter), r.cfg.Batch)
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range stuck {
		if r.d.Schedule != nil {
			at := r.d.Clock.Now()
			if l.PushedAt != nil {
				at = *l.PushedAt
			}
			open, err := r.d.Schedule.OrderingOpenAt(ctx, at)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !open {
				continue
			}
		}
		if err := r.d.Orders.MarkNotAccepted(ctx, l.OrderID); err != nil {
			errs = append(errs, err)
			continue
		}
		logging.FromContext(ctx).Error("hubrise: an order was not accepted at HubRise in time",
			zap.String("order_id", l.OrderID.String()), zap.String("hubrise_order_id", l.HubriseOrderID))
		if r.d.Alerter != nil {
			subject := "HubRise: order " + ShortRef(l.OrderID) + " not accepted"
			body := fmt.Sprintf("Order %s was sent to HubRise (%s) more than %d minutes ago and is still new. Check the EPOS.",
				ShortRef(l.OrderID), l.HubriseOrderID, int(r.cfg.NewAlertAfter.Minutes()))
			if err := r.d.Alerter.Alert(ctx, subject, body); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := r.d.Orders.MarkStuckAlerted(ctx, l.OrderID, r.d.Clock.Now()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// purge forgets old finished events, foreign order marks and expired OAuth states, and applies the
// retention of the outbox (finished rows go, the personal data of sent orders and of erased accounts
// is dropped).
func (r *Reconciler) purge(ctx context.Context) error {
	now := r.d.Clock.Now()
	cutoff := now.Add(-r.cfg.Retention)
	var errs []error
	if _, err := r.d.Events.PurgeFinished(ctx, cutoff); err != nil {
		errs = append(errs, err)
	}
	if _, err := r.d.Outbox.PurgeFinished(ctx, cutoff); err != nil {
		errs = append(errs, err)
	}
	if _, err := r.d.Foreign.Purge(ctx, cutoff); err != nil {
		errs = append(errs, err)
	}
	if _, err := r.d.OAuthStates.PurgeExpired(ctx, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
