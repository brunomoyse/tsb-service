package application_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func (e *env) ordersPath() string { return "/v1/location/orders" }

func TestCashOrderIsPushedWithItsRefsAndTheTotalIsChecked(t *testing.T) {
	e := newEnv(t)
	salmon := e.fx.SalmonSushi.ID
	group := testhelpers.SeedChoiceGroup(t, e.db, salmon, 0, 2, map[string]string{"fr": "Sauce"})
	choice := testhelpers.SeedChoice(t, e.db, salmon, group, "0.50", map[string]string{"fr": "Soja"})
	e.linkProduct(salmon, "SUSHI-SALMON")
	e.linkChoice(choice, "SUSHI-SALMON|sauce|SOJA", "SOJA")
	channel := "app"
	id := e.order(orderSpec{
		channel: &channel, total: "26.00", note: "Sonnez deux fois",
		lines: []lineSpec{{product: salmon, qty: 2, total: "26.00", selections: []selSpec{{group, choice, 2}}}},
	})

	e.ready(id)
	rows := e.outboxRows(id, domain.OutboxOrderCreate)
	require.Len(t, rows, 1)
	assert.Equal(t, domain.OutboxPending, rows[0].State)
	assert.Equal(t, domain.PushPending, e.link(id).PushState)
	assert.Empty(t, e.posts(), "queueing sends nothing: the order is not waiting for HubRise")

	assert.Equal(t, 1, e.drain())

	posts := e.posts()
	require.Len(t, posts, 1)
	remote := e.remote(id)
	assert.Equal(t, id.String(), remote.PrivateRef)
	assert.Equal(t, "app", remote.Channel)
	assert.Equal(t, pkghubrise.StatusNew, remote.Status)
	assert.Equal(t, pkghubrise.ServiceCollection, remote.ServiceType)
	assert.Equal(t, "Sonnez deux fois", remote.CustomerNotes)
	require.Len(t, remote.Items, 1)
	item := remote.Items[0]
	assert.Equal(t, "SUSHI-SALMON", item.SkuRef)
	assert.Equal(t, "Sushi au Saumon", item.ProductName)
	assert.Equal(t, "12.50 EUR", string(item.Price), "the base price: (26.00 paid - 2 x 0.50) / 2")
	require.Len(t, item.Options, 1)
	assert.Equal(t, "SOJA", item.Options[0].Ref)
	assert.Equal(t, "Sauce", item.Options[0].OptionListName)
	require.Len(t, remote.Payments, 1)
	assert.Equal(t, "CASH", remote.Payments[0].Ref)

	link := e.link(id)
	assert.Equal(t, domain.PushSent, link.PushState)
	require.NotNil(t, link.HubriseOrderID)
	assert.Equal(t, remote.ID, *link.HubriseOrderID)
	require.NotNil(t, link.LastRemoteStatus)
	assert.Equal(t, "new", *link.LastRemoteStatus)
	require.NotNil(t, link.RemoteTotal)
	assert.Equal(t, "26", link.RemoteTotal.String())
	assert.Nil(t, link.NeedsAttention)
	assert.NotNil(t, link.PushedAt)

	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxSent, row.State)
	assert.Equal(t, 1, row.Attempts)
	assert.Equal(t, 0, e.drain(), "nothing is left to send")
}

func TestOnlineOrderCarriesTheMolliePayment(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{online: true, paid: true, total: "12.80"})
	e.ready(id)
	e.drainAll()
	remote := e.remote(id)
	require.Len(t, remote.Payments, 1)
	assert.Equal(t, "MOLLIE", remote.Payments[0].Ref)
	assert.Equal(t, "12.80 EUR", string(remote.Payments[0].Amount))
	assert.Contains(t, string(remote.Payments[0].Info), "tr_")
	require.Len(t, remote.Charges, 1)
	assert.Equal(t, "PAYMENT_FEE", remote.Charges[0].Ref)
	assert.Equal(t, "12.80", mustDecimal(t, remote.Total).StringFixed(2))
	assert.Equal(t, domain.PushSent, e.link(id).PushState)
}

func TestUnlinkedItemsAreSentAndFlagged(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.drainAll()
	remote := e.remote(id)
	assert.Empty(t, remote.Items[0].SkuRef)
	link := e.link(id)
	assert.Equal(t, domain.PushSent, link.PushState)
	require.NotNil(t, link.NeedsAttention)
	assert.Equal(t, domain.AttentionUnlinkedItem, *link.NeedsAttention)
}

func TestATotalHubRiseCalculatesDifferentlyIsFlagged(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	// Corrupt the frozen total so the local figure disagrees with what HubRise will add up.
	e.exec(`UPDATE hubrise_outbox SET payload = jsonb_set(payload, '{total}', '"99.00"') WHERE order_id = $1`, id)
	e.drainAll()
	link := e.link(id)
	require.NotNil(t, link.NeedsAttention)
	assert.Equal(t, domain.AttentionTotalMismatch, *link.NeedsAttention)
	require.NotNil(t, link.RemoteTotal)
	assert.Equal(t, "12.5", link.RemoteTotal.String())
}

func TestARetryAfterALostResponseDoesNotCreateASecondOrder(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)

	// The order reaches HubRise, the answer is lost.
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Times: 1, DropAfter: true})
	e.drain()
	require.Len(t, e.srv.Orders(), 1)
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.Equal(t, 1, row.Attempts, "the attempt is durable")
	assert.Equal(t, domain.PushPending, e.link(id).PushState)

	assert.Equal(t, 0, e.drain(), "backing off: not due yet")
	e.clock.Advance(31 * time.Second)
	e.drain()

	assert.Len(t, e.posts(), 1, "a second claim looks the order up by private_ref instead of posting again")
	assert.Len(t, e.srv.Orders(), 1)
	link := e.link(id)
	assert.Equal(t, domain.PushSent, link.PushState)
	assert.Equal(t, e.srv.Orders()[0].ID, *link.HubriseOrderID)
	assert.Equal(t, domain.OutboxSent, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
	assert.Equal(t, 2, e.outboxRows(id, domain.OutboxOrderCreate)[0].Attempts)
}

func TestACrashBetweenTheSendAndTheMarkIsRecoveredAfterTheLease(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)

	// A worker claims the row, sends the order, and dies before it records anything.
	items, err := e.mod.Outbox.Claim(t.Context(), e.clock.Now(), 2*time.Minute, 5, domain.OutboxOrderCreate)
	require.NoError(t, err)
	require.Len(t, items, 1)
	order, _ := buildForTest(t, e, id)
	_, err = e.srv.NewClient(t).CreateOrder(t.Context(), order)
	require.NoError(t, err)

	assert.Equal(t, 0, e.drain(), "the lease is still running")
	e.clock.Advance(3 * time.Minute)
	assert.Equal(t, 1, e.drain())

	assert.Len(t, e.posts(), 1, "recovered by the lookup, no second POST")
	require.Len(t, e.srv.Orders(), 1)
	assert.Equal(t, domain.PushSent, e.link(id).PushState)
	assert.Equal(t, 2, e.outboxRows(id, domain.OutboxOrderCreate)[0].Attempts)
}

func TestAnOrderAlreadyFinishedBeforeItsFirstSendIsNeverPushed(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.setStatus(id, "CANCELLED")
	e.drainAll()
	assert.Empty(t, e.srv.Requests(), "HubRise never sees an order the shop already finished")
	assert.Equal(t, domain.PushSkipped, e.link(id).PushState)
	assert.Equal(t, domain.OutboxSkipped, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
	for _, r := range e.outboxRows(id, domain.OutboxOrderStatus) {
		assert.Equal(t, domain.OutboxSkipped, r.State, "no PATCH for an order HubRise does not have")
	}
}

func TestACreateReclaimedAfterACrashAdoptsTheOrderEvenIfItEnded(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	_, err := e.mod.Outbox.Claim(t.Context(), e.clock.Now(), time.Minute, 5, domain.OutboxOrderCreate)
	require.NoError(t, err)
	order, _ := buildForTest(t, e, id)
	_, err = e.srv.NewClient(t).CreateOrder(t.Context(), order)
	require.NoError(t, err)
	e.setStatus(id, "CANCELLED")

	e.clock.Advance(2 * time.Minute)
	e.drainAll()

	assert.Len(t, e.posts(), 1)
	assert.Equal(t, domain.PushSent, e.link(id).PushState)
	remote := e.remote(id)
	assert.Equal(t, pkghubrise.StatusCancelled, remote.Status, "the cancellation queued behind the create reaches HubRise")
}

func TestTheCreateUsesTheCurrentStatusAndSkipsTheRowsItCovers(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	e.setStatus(id, "CONFIRMED")
	e.setStatus(id, "PREPARING")

	e.drainAll()

	assert.Equal(t, pkghubrise.StatusInPreparation, e.remote(id).Status, "created in the state staff already moved it to")
	assert.Len(t, e.posts(), 1)
	assert.Empty(t, e.patches(), "the queued transitions are covered by the create")
	for _, r := range e.outboxRows(id, domain.OutboxOrderStatus) {
		assert.Equal(t, domain.OutboxSkipped, r.State)
	}
	assert.Equal(t, "in_preparation", *e.link(id).LastRemoteStatus)
}

func TestAStatusChangeEnqueuedDuringTheSendIsNotLost(t *testing.T) {
	var e *env
	var once sync.Once
	var id uuid.UUID
	e = newEnv(t, withClients(func(e *env) application.ClientProvider {
		hooked := &http.Client{Transport: hookTransport{next: e.srv.HTTPClient().Transport, before: func(r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/location/orders") {
				once.Do(func() { e.setStatus(id, "CONFIRMED") })
			}
		}}}
		return fixedClients{c: e.srv.NewClient(t, pkghubrise.WithHTTPClient(hooked))}
	}))
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id = e.order(orderSpec{})
	e.ready(id)

	e.drain()
	assert.Equal(t, pkghubrise.StatusNew, e.remote(id).Status, "the create was built before the change")
	rows := e.outboxRows(id, domain.OutboxOrderStatus)
	require.Len(t, rows, 1)
	assert.Equal(t, domain.OutboxPending, rows[0].State, "a row queued after the status was read stays queued")

	e.drainAll()
	assert.Equal(t, pkghubrise.StatusAccepted, e.remote(id).Status)
	require.Len(t, e.patches(), 1)
}

func TestOutageWithThreeStatusChangesThenRecovery(t *testing.T) {
	t.Run("the create is the one failing: it goes out with the final status and nothing else", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.order(orderSpec{})
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 503})
		e.ready(id)
		e.drain()
		e.setStatus(id, "CONFIRMED")
		e.setStatus(id, "PREPARING")
		e.setStatus(id, "AWAITING_PICK_UP")
		e.clock.Advance(time.Minute)
		e.drain()
		assert.Empty(t, e.srv.Orders())

		e.srv.ClearFaults()
		e.clock.Advance(time.Hour)
		e.drainAll()
		require.Len(t, e.srv.Orders(), 1)
		assert.Equal(t, pkghubrise.StatusAwaitingCollection, e.remote(id).Status)
		assert.Empty(t, e.patches(), "no backwards or redundant PATCH")
	})
	t.Run("the PATCHes are the ones failing: they follow in order and end on the final status", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.order(orderSpec{})
		e.ready(id)
		e.drainAll()
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: e.ordersPath(), Status: 503})
		e.setStatus(id, "CONFIRMED")
		e.setStatus(id, "PREPARING")
		e.setStatus(id, "AWAITING_PICK_UP")
		e.drain()
		assert.Equal(t, pkghubrise.StatusNew, e.remote(id).Status)

		e.srv.ClearFaults()
		e.srv.ClearRequests()
		e.clock.Advance(time.Hour)
		e.drainAll()
		var sent []string
		for _, p := range e.patches() {
			sent = append(sent, string(p.Body))
		}
		require.Len(t, sent, 3)
		assert.Contains(t, sent[0], "accepted")
		assert.Contains(t, sent[1], "in_preparation")
		assert.Contains(t, sent[2], "awaiting_collection")
		assert.Equal(t, pkghubrise.StatusAwaitingCollection, e.remote(id).Status)
		assert.Equal(t, "awaiting_collection", *e.link(id).LastRemoteStatus)
	})
}

func TestAStatusGoingBackAndForthIsSentAsThreePatches(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	e.drainAll()
	e.setStatus(id, "CONFIRMED")
	e.setStatus(id, "PREPARING")
	e.setStatus(id, "CONFIRMED")
	e.drainAll()
	require.Len(t, e.patches(), 3, "no collapsing dedupe on status rows")
	assert.Equal(t, pkghubrise.StatusAccepted, e.remote(id).Status)
}

func TestAStatusEqualToTheRemoteOneIsSkipped(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	e.drainAll()
	e.setStatus(id, "CONFIRMED")
	e.drainAll()
	e.srv.ClearRequests()
	e.setStatus(id, "CONFIRMED") // the same target again
	e.drainAll()
	assert.Empty(t, e.srv.Requests())
	rows := e.outboxRows(id, domain.OutboxOrderStatus)
	assert.Equal(t, domain.OutboxSkipped, rows[len(rows)-1].State)
}

func TestAnOrderThatIsFinalAtHubRiseIsNotReopened(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	e.drainAll()
	remote := e.remote(id)
	require.NoError(t, e.srv.SetOrderStatus(remote.ID, pkghubrise.StatusCompleted))
	// The shop learns it later: its own record of HubRise says completed already.
	e.exec(`UPDATE hubrise_orders SET last_remote_status = 'completed' WHERE order_id = $1`, id)
	e.setStatus(id, "PREPARING")
	e.srv.ClearRequests()
	e.drainAll()
	assert.Empty(t, e.patches())
	assert.Equal(t, pkghubrise.StatusCompleted, e.remote(id).Status)
}

func TestPatchesTargetTheRemoteIdOfEveryStatus(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	cases := map[string]pkghubrise.OrderStatus{
		"CONFIRMED": pkghubrise.StatusAccepted, "PREPARING": pkghubrise.StatusInPreparation,
		"AWAITING_PICK_UP": pkghubrise.StatusAwaitingCollection, "OUT_FOR_DELIVERY": pkghubrise.StatusInDelivery,
		"PICKED_UP": pkghubrise.StatusCompleted,
	}
	for local, remoteStatus := range cases {
		t.Run(local, func(t *testing.T) {
			id := e.order(orderSpec{})
			e.ready(id)
			e.drainAll()
			e.setStatus(id, local)
			e.drainAll()
			assert.Equal(t, remoteStatus, e.remote(id).Status)
		})
	}
	t.Run("a cancellation", func(t *testing.T) {
		id := e.order(orderSpec{})
		e.ready(id)
		e.drainAll()
		e.setStatus(id, "CANCELLED")
		e.drainAll()
		assert.Equal(t, pkghubrise.StatusCancelled, e.remote(id).Status)
	})
}

func TestTwoWorkersOnOneOutboxSendEachOrderOnce(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	const orders = 12
	for range orders {
		e.ready(e.order(orderSpec{}))
	}
	second := e.rebuild(e.opts)

	var wg sync.WaitGroup
	run := func(drain func() (int, error)) {
		defer wg.Done()
		for {
			n, err := drain()
			if err != nil {
				t.Error(err)
				return
			}
			if n == 0 {
				return
			}
		}
	}
	wg.Add(2)
	go run(func() (int, error) { return e.push.Worker.DrainOnce(t.Context()) })
	go run(func() (int, error) { return second.Worker.DrainOnce(t.Context()) })
	wg.Wait()

	assert.Len(t, e.posts(), orders, "exactly one POST per order")
	refs := map[string]bool{}
	for _, o := range e.srv.Orders() {
		assert.False(t, refs[o.PrivateRef], "duplicate private_ref %s", o.PrivateRef)
		refs[o.PrivateRef] = true
	}
	assert.Equal(t, orders, e.count(`SELECT count(*) FROM hubrise_outbox WHERE state = 'sent'`))
	assert.Equal(t, orders, e.count(`SELECT count(*) FROM hubrise_orders WHERE push_state = 'sent'`))
}

func mustDecimal(t *testing.T, m pkghubrise.Money) interface{ StringFixed(int32) string } {
	t.Helper()
	d, err := m.Decimal()
	require.NoError(t, err)
	return d
}

// leaseSpy wraps the outbox: it records what Extend answered, can make it fail, and runs a hook
// after a row was completed, which is how the tests let time pass in the middle of a batch.
type leaseSpy struct {
	domain.OutboxRepository
	extended  []bool
	extendErr error
	afterSent func()
}

func (s *leaseSpy) Extend(ctx context.Context, id int64, now, until time.Time) (bool, error) {
	if s.extendErr != nil {
		return false, s.extendErr
	}
	ok, err := s.OutboxRepository.Extend(ctx, id, now, until)
	s.extended = append(s.extended, ok)
	return ok, err
}

func (s *leaseSpy) MarkSent(ctx context.Context, id int64, now time.Time) (bool, error) {
	ok, err := s.OutboxRepository.MarkSent(ctx, id, now)
	if s.afterSent != nil {
		s.afterSent()
	}
	return ok, err
}

func (e *env) workerOver(outbox domain.OutboxRepository, cfg application.WorkerConfig) *application.OutboxWorker {
	return application.NewOutboxWorker(application.WorkerDeps{
		Connections: e.mod.Connections, Outbox: outbox, Orders: e.mod.Orders, Links: e.mod.CatalogLinks,
		Source: e.mod.Source, Clients: e.opts.Clients, Clock: e.clock, Config: cfg,
	})
}

// twoQueuedOrders queues two orders that one claim leases together.
func twoQueuedOrders(e *env) (first, second uuid.UUID) {
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	return e.queued(orderSpec{}), e.queued(orderSpec{})
}

func TestTheLeaseIsRenewedBeforeEachRowOfABatch(t *testing.T) {
	e := newEnv(t)
	first, second := twoQueuedOrders(e)
	spy := &leaseSpy{OutboxRepository: e.mod.Outbox}
	spy.afterSent = func() { e.clock.Advance(4 * time.Minute) } // the first row was slow, the lease is 5 minutes
	n, err := e.workerOver(spy, application.WorkerConfig{}).DrainOnce(t.Context())
	require.NoError(t, err)

	assert.Equal(t, 2, n)
	assert.Equal(t, []bool{true}, spy.extended, "the second row got a fresh lease, the first needs none")
	assert.Len(t, e.posts(), 2)
	for _, id := range []uuid.UUID{first, second} {
		assert.Equal(t, domain.OutboxSent, e.state(id, domain.OutboxOrderCreate))
	}
}

func TestARowWhoseLeaseRanOutInTheBatchIsLeftToTheNextClaim(t *testing.T) {
	e := newEnv(t)
	first, second := twoQueuedOrders(e)
	spy := &leaseSpy{OutboxRepository: e.mod.Outbox}
	spy.afterSent = func() { e.clock.Advance(6 * time.Minute) } // longer than the lease: another replica may hold it
	_, err := e.workerOver(spy, application.WorkerConfig{}).DrainOnce(t.Context())
	require.NoError(t, err)

	assert.Equal(t, []bool{false}, spy.extended)
	assert.Len(t, e.posts(), 1, "the late row is not sent from a stale claim")
	assert.Equal(t, domain.OutboxSent, e.state(first, domain.OutboxOrderCreate))
	assert.Equal(t, domain.OutboxSending, e.state(second, domain.OutboxOrderCreate))

	// Whoever claims it next looks the order up first (attempts is 2) and creates it once.
	e.drainAll()
	assert.Len(t, e.posts(), 2)
	assert.Equal(t, domain.OutboxSent, e.state(second, domain.OutboxOrderCreate))
	assert.Equal(t, 2, e.outboxRows(second, domain.OutboxOrderCreate)[0].Attempts)
}

func TestARowWhoseLeaseCannotBeRenewedIsNotSent(t *testing.T) {
	e := newEnv(t)
	_, second := twoQueuedOrders(e)
	spy := &leaseSpy{OutboxRepository: e.mod.Outbox, extendErr: errors.New("store down")}
	_, err := e.workerOver(spy, application.WorkerConfig{}).DrainOnce(t.Context())
	require.NoError(t, err)

	assert.Len(t, e.posts(), 1)
	assert.Equal(t, domain.OutboxSending, e.state(second, domain.OutboxOrderCreate), "it stays leased until the lease runs out")
	e.clock.Advance(6 * time.Minute)
	e.drainAll()
	assert.Len(t, e.posts(), 2)
}

func TestTheLeaseOutlastsTheCallsOfOneRow(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  application.WorkerConfig
		want time.Duration
	}{
		"default":                {application.WorkerConfig{}, 5 * time.Minute},
		"too short for the call": {application.WorkerConfig{Lease: time.Minute}, 4 * time.Minute},
		"short calls":            {application.WorkerConfig{Lease: time.Second, CallTimeout: 10 * time.Second}, 40 * time.Second},
		"generous":               {application.WorkerConfig{Lease: 10 * time.Minute}, 10 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id := e.queued(orderSpec{})
			// The row stays claimed when it cannot be given back, which exposes its lease.
			outbox := faultOutbox{e.mod.Outbox, newFaults("Outbox.Release")}
			e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 503, Times: 1})
			_, err := e.workerOver(outbox, tc.cfg).DrainOnce(t.Context())
			require.NoError(t, err)

			row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
			require.NotNil(t, row.LeasedUntil)
			assert.WithinDuration(t, e.clock.Now().Add(tc.want), *row.LeasedUntil, time.Second)
		})
	}
}
