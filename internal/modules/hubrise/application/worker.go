package application

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// WorkerConfig tunes the outbox worker; zero values take the defaults of the plan.
type WorkerConfig struct {
	// BatchSize is how many rows one claim leases (5).
	BatchSize int
	// Lease is how long a claimed row stays ours (5 minutes). The worker renews it before each row of
	// a batch, so it only has to outlast the calls of one row: a row makes at most three HubRise
	// calls (a 404 on a status PATCH, the private_ref lookup, the repeated PATCH), each bounded by
	// CallTimeout. A smaller value is raised to four times CallTimeout, or another replica could
	// reclaim a row that is still being sent and POST it a second time.
	Lease time.Duration
	// Tick is the poll interval; the wake-up channel makes a new row go out sooner (5 seconds).
	Tick time.Duration
	// CallTimeout bounds the HubRise calls made for one row, rate limiter waits included (60 s).
	CallTimeout time.Duration
	// BackoffBase and BackoffMax are the retry delay of a failed send: it starts at BackoffBase and
	// doubles up to BackoffMax (30 s and 30 minutes).
	BackoffBase, BackoffMax time.Duration
	// CreateMaxAge, StatusMaxAge: older rows are not sent (48 h parks a create, 6 h skips a status).
	CreateMaxAge, StatusMaxAge time.Duration
	// CustomerMaxAge: a profile update older than this is skipped (24 h): the customer is brought up
	// to date by the next order anyway. An anonymisation never ages out.
	CustomerMaxAge time.Duration
	// CustomerBusyDelay is how long a row waits when another process is writing the same customer (5 s).
	CustomerBusyDelay time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 5
	}
	def(&c.Lease, 5*time.Minute)
	def(&c.Tick, 5*time.Second)
	def(&c.CallTimeout, time.Minute)
	c.Lease = max(c.Lease, 4*c.CallTimeout)
	def(&c.BackoffBase, 30*time.Second)
	def(&c.BackoffMax, 30*time.Minute)
	def(&c.CreateMaxAge, 48*time.Hour)
	def(&c.StatusMaxAge, 6*time.Hour)
	def(&c.CustomerMaxAge, 24*time.Hour)
	def(&c.CustomerBusyDelay, 5*time.Second)
	return c
}

// WorkerDeps are the collaborators of an OutboxWorker.
type WorkerDeps struct {
	Connections domain.ConnectionRepository
	Outbox      domain.OutboxRepository
	Orders      domain.OrderLinkRepository
	Links       domain.CatalogLinkRepository
	// Availability reads the product state an inventory row is built from; without it inventory rows
	// are not claimed.
	Availability domain.AvailabilityStore
	// Customers keeps the HubRise customer list; without it customer rows are not claimed and an order
	// always carries a guest customer.
	Customers *CustomerSync
	Source    domain.OrderSource
	Clients   ClientProvider
	Clock     domain.Clock
	Config    WorkerConfig
}

// OutboxWorker drains the order_create, order_status, inventory, customer and customer_anonymise rows of
// the outbox to HubRise.
//
// Rows are claimed in one statement that leases them and counts the attempt before the HTTP call, so
// a crash between the send and the acknowledgement is recognised on the next claim (attempts > 1) and
// answered with a private_ref lookup instead of a second POST. Two replicas can run it side by side:
// the claim is FOR UPDATE SKIP LOCKED and only the first row of an order is claimable.
type OutboxWorker struct {
	d    WorkerDeps
	cfg  WorkerConfig
	wake chan struct{}
	// kinds are the outbox kinds this worker handles; a worker must never lease the others.
	kinds []domain.OutboxKind
}

// NewOutboxWorker returns the worker.
func NewOutboxWorker(d WorkerDeps) *OutboxWorker {
	w := &OutboxWorker{d: d, cfg: d.Config.withDefaults(), wake: make(chan struct{}, 1)}
	w.kinds = []domain.OutboxKind{domain.OutboxOrderCreate, domain.OutboxOrderStatus}
	if d.Availability != nil {
		w.kinds = append(w.kinds, domain.OutboxInventory)
	}
	if d.Customers != nil {
		w.kinds = append(w.kinds, domain.OutboxCustomer, domain.OutboxCustomerAnonymise)
	}
	return w
}

// Wake asks the worker to drain now. It never blocks and a pending wake-up absorbs the next ones.
func (w *OutboxWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run drains the outbox until ctx ends: on every wake-up and every tick.
func (w *OutboxWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Tick)
	defer ticker.Stop()
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

// drain claims batches until one comes back short, with a cap so a flood cannot starve the loop's
// shutdown check.
func (w *OutboxWorker) drain(ctx context.Context) {
	for range 100 {
		if ctx.Err() != nil {
			return
		}
		n, err := w.DrainOnce(ctx)
		if err != nil {
			logging.FromContext(ctx).Error("hubrise: outbox drain failed", zap.Error(err))
			return
		}
		if n < w.cfg.BatchSize {
			return
		}
	}
}

// batch is the state shared by the rows of one claim.
type batch struct {
	client *hubrise.Client
	conn   *domain.Connection
	// paused is set when HubRise refused the connection: the rest of the batch is handed back.
	paused bool
}

// DrainOnce claims one batch and processes it. It returns how many rows it claimed.
func (w *OutboxWorker) DrainOnce(ctx context.Context) (int, error) {
	now := w.d.Clock.Now()
	items, err := w.d.Outbox.Claim(ctx, now, w.cfg.Lease, w.cfg.BatchSize, w.kinds...)
	if err != nil || len(items) == 0 {
		return 0, err
	}
	b := &batch{}
	if b.conn, err = w.d.Connections.Get(ctx); err == nil {
		b.client, err = w.d.Clients.Client(ctx, b.conn)
	}
	if err != nil {
		// The connection vanished between the claim and now, or its token cannot be opened. Give the
		// rows back without burning an attempt.
		logging.FromContext(ctx).Error("hubrise: cannot build the API client", zap.Error(err))
		for _, it := range items {
			w.release(ctx, it, w.backoff(it, nil), "cannot build the API client", true)
		}
		return len(items), nil
	}
	for i, it := range items {
		if b.paused || ctx.Err() != nil {
			w.release(ctx, it, w.d.Clock.Now(), "", true)
			continue
		}
		if i > 0 && !w.renew(ctx, it) {
			continue
		}
		w.process(ctx, b, it)
	}
	return len(items), nil
}

// renew gives a row of the batch a full lease again just before it is processed, so the time spent
// on the rows before it never eats into it. It returns false when the row must not be processed: its
// lease had already run out (another replica may hold it by now, and a send from here would race the
// idempotency lookup that its claim runs). The row is then left to whoever claims it next.
func (w *OutboxWorker) renew(ctx context.Context, item domain.OutboxItem) bool {
	now := w.d.Clock.Now()
	ok, err := w.d.Outbox.Extend(context.WithoutCancel(ctx), item.ID, now, now.Add(w.cfg.Lease))
	if err != nil {
		logging.FromContext(ctx).Error("hubrise: cannot renew an outbox lease", zap.Int64("outbox_id", item.ID), zap.Error(err))
		return false // the lease runs out and the row is claimed again
	}
	if !ok {
		logging.FromContext(ctx).Warn("hubrise: outbox lease lost before the row was processed", zap.Int64("outbox_id", item.ID))
	}
	return ok
}

func (w *OutboxWorker) process(ctx context.Context, b *batch, item domain.OutboxItem) {
	switch item.Kind {
	case domain.OutboxOrderCreate:
		w.sendCreate(ctx, b, item)
	case domain.OutboxOrderStatus:
		w.sendStatus(ctx, b, item)
	case domain.OutboxInventory:
		w.sendInventory(ctx, b, item)
	case domain.OutboxCustomer:
		w.sendCustomer(ctx, b, item)
	case domain.OutboxCustomerAnonymise:
		w.sendCustomerAnonymise(ctx, b, item)
	}
}

// call bounds one HubRise request (the rate limiter wait included), so a stalled call cannot hold
// the lease of the rest of the batch. The bookkeeping that follows is not bound by it.
func call[T any](ctx context.Context, w *OutboxWorker, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.CallTimeout)
	defer cancel()
	return f(ctx)
}

// ---- outcomes ----

// backoff is the time of the next attempt of a row that just failed: 30 s, doubling up to 30
// minutes, and not before HubRise's Retry-After.
func (w *OutboxWorker) backoff(item domain.OutboxItem, err error) time.Time {
	d := w.cfg.BackoffBase
	for i := 1; i < item.Attempts && d < w.cfg.BackoffMax; i++ {
		d *= 2
	}
	d = min(d, w.cfg.BackoffMax)
	var he *hubrise.Error
	if errors.As(err, &he) && he.RetryAfter > d {
		d = he.RetryAfter
	}
	return w.d.Clock.Now().Add(d)
}

func (w *OutboxWorker) release(ctx context.Context, item domain.OutboxItem, next time.Time, reason string, revert bool) {
	if _, err := w.d.Outbox.Release(context.WithoutCancel(ctx), item.ID, next, reason, revert); err != nil {
		logging.FromContext(ctx).Error("hubrise: cannot release an outbox row", zap.Int64("outbox_id", item.ID), zap.Error(err))
	}
}

func (w *OutboxWorker) markSent(ctx context.Context, item domain.OutboxItem) {
	if _, err := w.d.Outbox.MarkSent(ctx, item.ID, w.d.Clock.Now()); err != nil {
		// The send happened. The lease runs out, and the next claim recognises the row as already
		// attempted and looks the order up before sending again.
		logging.FromContext(ctx).Error("hubrise: cannot mark an outbox row sent", zap.Int64("outbox_id", item.ID), zap.Error(err))
	}
}

func (w *OutboxWorker) skip(ctx context.Context, item domain.OutboxItem, reason string) {
	logging.FromContext(ctx).Info("hubrise: outbox row skipped", zap.Int64("outbox_id", item.ID), zap.String("reason", reason))
	if _, err := w.d.Outbox.MarkSkipped(ctx, item.ID, reason); err != nil {
		logging.FromContext(ctx).Error("hubrise: cannot skip an outbox row", zap.Int64("outbox_id", item.ID), zap.Error(err))
	}
	w.skipStatusRowsOf(ctx, item)
}

// skipStatusRowsOf drops the queued status rows of an order whose create just ended without reaching
// HubRise (parked or skipped): they cannot be sent for an order HubRise does not have, and a create
// that is retried later is built with the then current status anyway.
func (w *OutboxWorker) skipStatusRowsOf(ctx context.Context, item domain.OutboxItem) {
	if item.Kind != domain.OutboxOrderCreate || item.OrderID == nil {
		return
	}
	if _, err := w.d.Outbox.SkipPendingUpTo(ctx, *item.OrderID, domain.OutboxOrderStatus, math.MaxInt64, "the order was not pushed"); err != nil {
		logging.FromContext(ctx).Error("hubrise: cannot skip the status rows of an unpushed order", zap.Error(err))
	}
}

// park gives up on a row until a human retries it, and flags the order.
func (w *OutboxWorker) park(ctx context.Context, item domain.OutboxItem, reason string) {
	reason = truncate(reason, 500)
	logging.FromContext(ctx).Error("hubrise: outbox row parked",
		zap.Int64("outbox_id", item.ID), zap.String("kind", string(item.Kind)), zap.String("reason", reason))
	if _, err := w.d.Outbox.MarkParked(ctx, item.ID, w.d.Clock.Now(), reason); err != nil {
		logging.FromContext(ctx).Error("hubrise: cannot park an outbox row", zap.Int64("outbox_id", item.ID), zap.Error(err))
	}
	if item.OrderID != nil {
		if item.Kind == domain.OutboxOrderCreate {
			w.logIf(ctx, "set push state", w.d.Orders.SetPushState(ctx, *item.OrderID, domain.PushParked))
		}
		w.logIf(ctx, "flag order", w.d.Orders.FlagAttention(ctx, *item.OrderID, domain.AttentionPushParked))
	}
	w.skipStatusRowsOf(ctx, item)
}

func (w *OutboxWorker) logIf(ctx context.Context, what string, err error) {
	if err != nil {
		logging.FromContext(ctx).Error("hubrise: "+what+" failed", zap.Error(err))
	}
}

// fail classifies the error of a HubRise call and settles the row accordingly (the outcome table of
// section 6.1 of the plan).
func (w *OutboxWorker) fail(ctx context.Context, b *batch, item domain.OutboxItem, err error) {
	var he *hubrise.Error
	if !errors.As(err, &he) {
		// Not an API answer (the database, a codec): nothing says retrying cannot work.
		w.release(ctx, item, w.backoff(item, nil), truncate(err.Error(), 500), false)
		return
	}
	switch {
	case he.ConnectionLevel():
		// 401 or 403 concern the whole connection: stop the pipeline instead of failing the row. The
		// claim's attempt is given back and nothing is parked; reconnecting resumes.
		msg := truncate(he.Error(), 500)
		logging.FromContext(ctx).Error("hubrise: connection refused, pipeline paused", zap.Error(he))
		w.logIf(ctx, "record auth error", w.d.Connections.RecordAuthError(context.WithoutCancel(ctx), w.d.Clock.Now(), msg))
		w.release(ctx, item, w.d.Clock.Now(), msg, true)
		b.paused = true
	case he.Kind == hubrise.KindInvalid, he.Kind == hubrise.KindNotFound:
		w.park(ctx, item, he.Error())
	case he.Kind == hubrise.KindCanceled:
		w.release(ctx, item, w.d.Clock.Now(), "interrupted", false)
	default: // 429, 5xx, timeout, network, protocol: the same request may work later
		w.release(ctx, item, w.backoff(item, he), truncate(he.Error(), 500), false)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- order_create ----

func (w *OutboxWorker) sendCreate(ctx context.Context, b *batch, item domain.OutboxItem) {
	if item.OrderID == nil {
		w.skip(ctx, item, "order create without an order")
		return
	}
	orderID := *item.OrderID
	link, err := w.d.Orders.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		w.skip(ctx, item, "no order link")
		return
	}
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	if link.HubriseOrderID != nil && link.PushState == domain.PushSent {
		w.markSent(ctx, item) // adopted earlier, only the row was left
		return
	}
	if w.d.Clock.Now().Sub(item.CreatedAt) > w.cfg.CreateMaxAge {
		w.park(ctx, item, "the order is too old to be pushed")
		return
	}
	if link.PushState == domain.PushParked {
		// A manual retry reactivated the chain: the order is being pushed again.
		w.logIf(ctx, "reset push state", w.d.Orders.SetPushState(ctx, orderID, domain.PushPending))
	}

	var payload domain.CreatePayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil || payload.Version != domain.PayloadVersion {
		w.park(ctx, item, "unreadable order payload")
		return
	}

	// A row claimed before may have reached HubRise whose answer was lost: look the order up by
	// private_ref and adopt it instead of creating a second one.
	if item.Attempts > 1 {
		found, err := call(ctx, w, func(c context.Context) (*hubrise.Order, error) {
			return b.client.FindOrderByPrivateRef(c, orderID.String())
		})
		if err != nil {
			w.fail(ctx, b, item, err)
			return
		}
		if found != nil {
			logging.FromContext(ctx).Info("hubrise: order already at HubRise, adopted",
				zap.String("order_id", orderID.String()), zap.String("hubrise_order_id", found.ID))
			w.afterCreate(ctx, item, orderID, found, payload.Total, false)
			return
		}
	}

	// Read, in this order, the newest queued status row and then the current status: every row up to
	// the first is covered by the status read after it, and is skipped once the create has gone out.
	coverID, err := w.d.Outbox.MaxPendingID(ctx, orderID, domain.OutboxOrderStatus)
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	state, err := w.d.Source.State(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		w.skip(ctx, item, "the order no longer exists")
		return
	}
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	status := initialRemoteStatus(state.Status)
	if status == "" || domain.IsTerminalLocal(state.Status) {
		// The shop already finished the order and HubRise never saw it: nothing is reprinted.
		w.logIf(ctx, "set push state", w.d.Orders.SetPushState(ctx, orderID, domain.PushSkipped))
		w.skip(ctx, item, "the order ended before it was pushed")
		return
	}

	env, err := w.sendEnv(ctx, b.conn, orderID, status, payload)
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	if env.CustomerID, err = w.customerFor(ctx, b, item, payload); err != nil {
		if errors.Is(err, ErrCustomerBusy) {
			w.release(ctx, item, w.d.Clock.Now().Add(w.cfg.CustomerBusyDelay), "the customer is being written elsewhere", true)
			return
		}
		w.fail(ctx, b, item, err)
		return
	}
	order, unlinked := BuildOrder(payload, env)
	created, err := call(ctx, w, func(c context.Context) (*hubrise.Order, error) { return b.client.CreateOrder(c, order) })
	if err != nil {
		w.fail(ctx, b, item, err)
		return
	}

	w.afterCreate(ctx, item, orderID, created, payload.Total, unlinked)
	if n, err := w.d.Outbox.SkipPendingUpTo(ctx, orderID, domain.OutboxOrderStatus, coverID, "covered by the order create"); err != nil {
		w.logIf(ctx, "skip covered status rows", err)
	} else if n > 0 {
		logging.FromContext(ctx).Info("hubrise: status rows covered by the create", zap.String("order_id", orderID.String()), zap.Int64("rows", n))
	}
}

// afterCreate records the order HubRise now has, created by us or adopted: the link, the remote total
// and its comparison with the local one, the flags, and the completed row. A failure here is logged
// and leaves the row to its lease: the next claim finds the order through private_ref.
func (w *OutboxWorker) afterCreate(ctx context.Context, item domain.OutboxItem, orderID uuid.UUID, remote *hubrise.Order, localTotal decimal.Decimal, unlinked bool) {
	now := w.d.Clock.Now()
	if err := w.d.Orders.MarkSent(ctx, orderID, remote.ID, string(remote.Status.Canonical()), now); err != nil {
		logging.FromContext(ctx).Error("hubrise: cannot record the pushed order", zap.String("order_id", orderID.String()), zap.Error(err))
		return
	}
	w.markSent(ctx, item)
	w.logIf(ctx, "clear flag", w.d.Orders.ClearAttention(ctx, orderID, domain.AttentionPushParked))
	if unlinked {
		w.logIf(ctx, "flag order", w.d.Orders.FlagAttention(ctx, orderID, domain.AttentionUnlinkedItem))
	}
	w.checkTotal(ctx, orderID, remote, localTotal)
}

// checkTotal stores the total HubRise calculated and flags a difference with the local total.
func (w *OutboxWorker) checkTotal(ctx context.Context, orderID uuid.UUID, remote *hubrise.Order, localTotal decimal.Decimal) {
	if remote.Total == "" {
		return
	}
	total, err := remote.Total.Decimal()
	if err != nil {
		logging.FromContext(ctx).Warn("hubrise: unreadable remote total", zap.String("order_id", orderID.String()), zap.Error(err))
		return
	}
	w.logIf(ctx, "store remote total", w.d.Orders.SetRemoteTotal(ctx, orderID, total))
	if !total.Equal(localTotal) {
		logging.FromContext(ctx).Error("hubrise: remote total differs from the local total",
			zap.String("order_id", orderID.String()), zap.String("local", localTotal.String()), zap.String("remote", total.String()))
		w.logIf(ctx, "flag order", w.d.Orders.FlagAttention(ctx, orderID, domain.AttentionTotalMismatch))
	}
}

// sendEnv resolves what must be current at send time: the refs of the link table and the settings.
func (w *OutboxWorker) sendEnv(ctx context.Context, conn *domain.Connection, orderID uuid.UUID, status hubrise.OrderStatus, p domain.CreatePayload) (SendEnv, error) {
	env := SendEnv{
		OrderID: orderID, Now: w.d.Clock.Now(), Status: status,
		DeliveryRef: conn.DeliveryChargeRefOrDefault(), PaymentFeeRef: conn.PaymentFeeChargeRefOrDefault(),
	}
	if conn.ChannelName != nil {
		env.Channel = *conn.ChannelName
	}
	products := map[uuid.UUID]string{}
	choices := map[uuid.UUID]string{}
	lookup := func(t domain.EntityType, id uuid.UUID, into map[uuid.UUID]string) error {
		if _, done := into[id]; done {
			return nil
		}
		link, err := w.d.Links.FindByLocal(ctx, t, id)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			into[id] = ""
		case err != nil:
			return err
		default:
			into[id] = link.HubriseRef
		}
		return nil
	}
	for _, it := range p.Items {
		if err := lookup(domain.EntityProduct, it.ProductID, products); err != nil {
			return env, err
		}
		for _, o := range it.Options {
			if err := lookup(domain.EntityChoice, o.ChoiceID, choices); err != nil {
				return env, err
			}
		}
	}
	env.SkuRef = func(id uuid.UUID) string { return products[id] }
	env.OptionRef = func(id uuid.UUID) string { return choices[id] }
	return env, nil
}

// ---- order_status ----

func (w *OutboxWorker) sendStatus(ctx context.Context, b *batch, item domain.OutboxItem) {
	if item.OrderID == nil {
		w.skip(ctx, item, "status change without an order")
		return
	}
	orderID := *item.OrderID
	link, err := w.d.Orders.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		w.skip(ctx, item, "no order link")
		return
	}
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	if w.d.Clock.Now().Sub(item.CreatedAt) > w.cfg.StatusMaxAge {
		// The reconciler sends the current status instead, if the order is still open.
		w.skip(ctx, item, "status change too old")
		return
	}
	if link.HubriseOrderID == nil {
		w.skip(ctx, item, "the order never reached HubRise")
		return
	}
	var payload domain.StatusPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		w.park(ctx, item, "unreadable status payload")
		return
	}
	target := RemoteStatusFor(payload.Status)
	if target == "" {
		w.skip(ctx, item, "this status is not pushed")
		return
	}
	last := hubrise.OrderStatus("")
	if link.LastRemoteStatus != nil {
		last = hubrise.OrderStatus(*link.LastRemoteStatus).Canonical()
	}
	switch {
	case target == last:
		w.skip(ctx, item, "HubRise already has this status")
		return
	case last.Final() && !target.Final():
		w.skip(ctx, item, "the HubRise order is already "+string(last))
		return
	}

	remoteID := *link.HubriseOrderID
	_, err = w.patch(ctx, b, remoteID, target)
	if hubrise.KindOf(err) == hubrise.KindNotFound {
		remoteID, err = w.recoverMissing(ctx, b, item, link, target)
		if remoteID == "" {
			return // settled by recoverMissing
		}
	}
	if err != nil {
		w.fail(ctx, b, item, err)
		return
	}
	w.logIf(ctx, "record patch", w.d.Orders.RecordLocalPatch(ctx, orderID, string(target), w.d.Clock.Now()))
	w.markSent(ctx, item)
}

func (w *OutboxWorker) patch(ctx context.Context, b *batch, remoteID string, target hubrise.OrderStatus) (*hubrise.Order, error) {
	return call(ctx, w, func(c context.Context) (*hubrise.Order, error) {
		return b.client.UpdateOrder(c, remoteID, &hubrise.OrderPatch{Status: target})
	})
}

// recoverMissing handles a 404 on a status PATCH. It looks the order up by private_ref: under another
// id, the id is backfilled and the PATCH repeated (the returned id and error are that retry's);
// unknown, the order is created again, once, with this row and the other queued status rows covered by
// it (an empty id is returned); a second loss, or any other lookup answer, parks the row.
func (w *OutboxWorker) recoverMissing(ctx context.Context, b *batch, item domain.OutboxItem, link *domain.OrderLink, target hubrise.OrderStatus) (string, error) {
	orderID := *item.OrderID
	found, err := call(ctx, w, func(c context.Context) (*hubrise.Order, error) {
		return b.client.FindOrderByPrivateRef(c, orderID.String())
	})
	if err != nil {
		return *link.HubriseOrderID, err
	}
	switch {
	case found != nil && found.ID != *link.HubriseOrderID:
		if err := w.d.Orders.MarkSent(ctx, orderID, found.ID, string(found.Status.Canonical()), w.d.Clock.Now()); err != nil {
			return found.ID, err
		}
		_, err := w.patch(ctx, b, found.ID, target)
		return found.ID, err
	case found != nil:
		w.park(ctx, item, "HubRise answers 404 for an order it lists")
		return "", nil
	}
	creates, err := w.d.Outbox.CountForOrder(ctx, orderID, domain.OutboxOrderCreate)
	if err != nil {
		return *link.HubriseOrderID, err
	}
	if creates > 1 {
		w.logIf(ctx, "flag order", w.d.Orders.FlagAttention(ctx, orderID, domain.AttentionRemoteMissing))
		w.park(ctx, item, "HubRise lost the order again after it was created a second time")
		return "", nil
	}
	snap, err := w.d.Source.Snapshot(ctx, orderID)
	if err != nil {
		return *link.HubriseOrderID, err
	}
	payload, err := BuildCreatePayload(snap).Marshal()
	if err != nil {
		return *link.HubriseOrderID, err
	}
	logging.FromContext(ctx).Warn("hubrise: HubRise lost an order, creating it again", zap.String("order_id", orderID.String()))
	if err := w.d.Orders.Recreate(ctx, orderID, payload, w.d.Clock.Now()); err != nil {
		return *link.HubriseOrderID, err
	}
	return "", nil
}

// ---- inventory ----

// sendInventory sends the stock of one SKU. The row only names the product and its SKU: the PATCH is
// built from the product's availability now, so a row that waited out an outage, or was queued before
// a second toggle, still sends the truth. The SKU is sold out ("0") while the product is unavailable
// and removed from the inventory (unlimited stock) when it is available.
func (w *OutboxWorker) sendInventory(ctx context.Context, b *batch, item domain.OutboxItem) {
	var payload domain.InventoryPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil || payload.Version != domain.InventoryPayloadVersion || payload.SkuRef == "" {
		w.park(ctx, item, "unreadable inventory payload")
		return
	}
	if !b.conn.SyncInventory {
		w.skip(ctx, item, "inventory sync is off") // switched off since the toggle queued the row
		return
	}
	link, err := w.d.Links.FindByLocal(ctx, domain.EntityProduct, payload.ProductID)
	if errors.Is(err, domain.ErrNotFound) {
		w.skip(ctx, item, "the product is no longer linked")
		return
	}
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	if link.RetiredAt != nil || link.HubriseRef != payload.SkuRef {
		w.skip(ctx, item, "the link of the product changed")
		return
	}
	states, err := w.d.Availability.Availability(ctx, domain.EntityProduct, []uuid.UUID{payload.ProductID})
	if err != nil {
		w.release(ctx, item, w.backoff(item, nil), err.Error(), false)
		return
	}
	state, ok := states[payload.ProductID]
	if !ok {
		w.skip(ctx, item, "the product no longer exists")
		return
	}
	entry := hubrise.AvailableSku(payload.SkuRef)
	if !state.Available {
		entry = hubrise.SoldOutSku(payload.SkuRef, nil)
	}
	if _, err := call(ctx, w, func(c context.Context) ([]hubrise.InventoryEntry, error) {
		return b.client.PatchInventory(c, []hubrise.InventoryEntry{entry})
	}); err != nil {
		w.fail(ctx, b, item, err)
		return
	}
	w.markSent(ctx, item)
}

// ---- customer ----

// customerFor returns the HubRise customer id to send with an order, after creating or updating the
// customer; "" sends the guest customer object (the customer sync is off or refused the customer).
func (w *OutboxWorker) customerFor(ctx context.Context, b *batch, item domain.OutboxItem, p domain.CreatePayload) (string, error) {
	if w.d.Customers == nil {
		return "", nil
	}
	return w.d.Customers.ForOrder(ctx, b.client, b.conn, p, item.Attempts)
}

// sendCustomer sends the current profile of a user to its HubRise customer.
func (w *OutboxWorker) sendCustomer(ctx context.Context, b *batch, item domain.OutboxItem) {
	var payload domain.CustomerPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil || payload.Version != domain.CustomerPayloadVersion || payload.UserID == uuid.Nil {
		w.park(ctx, item, "unreadable customer payload")
		return
	}
	if w.d.Clock.Now().Sub(item.CreatedAt) > w.cfg.CustomerMaxAge {
		w.skip(ctx, item, "customer update too old") // the next order brings the customer up to date
		return
	}
	skip, err := w.d.Customers.SendProfile(ctx, b.client, b.conn, payload.UserID)
	w.finishCustomer(ctx, b, item, skip, err)
}

// sendCustomerAnonymise anonymises the HubRise customer of a deleted account.
func (w *OutboxWorker) sendCustomerAnonymise(ctx context.Context, b *batch, item domain.OutboxItem) {
	var payload domain.CustomerAnonymisePayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil || payload.Version != domain.CustomerPayloadVersion || payload.UserID == uuid.Nil {
		w.park(ctx, item, "unreadable customer payload")
		return
	}
	skip, err := w.d.Customers.SendAnonymise(ctx, b.client, b.conn, payload)
	w.finishCustomer(ctx, b, item, skip, err)
}

// finishCustomer settles a customer row after its send.
func (w *OutboxWorker) finishCustomer(ctx context.Context, b *batch, item domain.OutboxItem, skip string, err error) {
	switch {
	case errors.Is(err, ErrCustomerBusy):
		w.release(ctx, item, w.d.Clock.Now().Add(w.cfg.CustomerBusyDelay), "the customer is being written elsewhere", true)
	case err != nil:
		w.fail(ctx, b, item, err)
	case skip != "":
		w.skip(ctx, item, skip)
	default:
		w.markSent(ctx, item)
	}
}
