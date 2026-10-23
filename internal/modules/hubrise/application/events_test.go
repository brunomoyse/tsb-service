package application_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// postRaw sends a callback request as HubRise would and returns the status.
func (s *syncEnv) postRaw(body []byte, signature string) int {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.t.Context(), http.MethodPost, s.server.URL+"/hubrise/callback", bytes.NewReader(body))
	require.NoError(s.t, err)
	if signature != "" {
		req.Header.Set(pkghubrise.SignatureHeader, signature)
	}
	resp, err := s.server.Client().Do(req)
	require.NoError(s.t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// post signs the event with the client secret and sends it.
func (s *syncEnv) post(ev pkghubrise.Event) int {
	s.t.Helper()
	body, err := json.Marshal(ev)
	require.NoError(s.t, err)
	return s.postRaw(body, pkghubrise.Sign(hubrisetest.ClientSecret, body))
}

// queued returns the events the mock holds for the system under test.
func (s *syncEnv) queued() []pkghubrise.Event { return s.srv.Events(hubrisetest.Token) }

func (s *syncEnv) event(id string) domain.Event {
	s.t.Helper()
	var e domain.Event
	require.NoError(s.t, s.db.GetContext(s.t.Context(), &e, `
		SELECT id, resource_type, event_type, payload, received_at, run_after, state, attempts, next_attempt_at,
		       processed_at, last_error FROM hubrise_events WHERE id = $1`, id))
	return e
}

func (s *syncEnv) attention(id uuid.UUID) string {
	s.t.Helper()
	l := s.link(id)
	if l.NeedsAttention == nil {
		return ""
	}
	return *l.NeedsAttention
}

// ---- the main path ----

func TestARemoteAcceptanceConfirmsTheOrder(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})

	s.epos(id, pkghubrise.StatusAccepted)

	require.Len(t, s.tr.all(), 1)
	assert.Equal(t, transition{id, application.OrderChange{Status: domain.LocalConfirmed}}, s.tr.all()[0])
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
	link := s.link(id)
	assert.Equal(t, "accepted", *link.LastRemoteStatus)
	require.NotNil(t, link.LastRemoteEventAt, "set from the remote event, never from our clock")
	assert.Empty(t, s.gets(), "the event carries the state: no read needed")
	assert.Equal(t, map[string]int{"done": 1}, s.eventStates())
	assert.Empty(t, s.patches(), "no echo back to HubRise")
	assert.Equal(t, 0, s.count(`SELECT count(*) FROM hubrise_outbox WHERE order_id = $1 AND kind = 'order_status'`, id))
}

func TestTheWholeLifeOfAPickupOrderFollowsHubRise(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	want := map[pkghubrise.OrderStatus]string{
		pkghubrise.StatusReceived:           "PENDING",
		pkghubrise.StatusAccepted:           "CONFIRMED",
		pkghubrise.StatusInPreparation:      "PREPARING",
		pkghubrise.StatusAwaitingCollection: "AWAITING_PICK_UP",
		pkghubrise.StatusAwaitingShipment:   "AWAITING_PICK_UP",
		pkghubrise.StatusCompleted:          "PICKED_UP",
	}
	for _, status := range []pkghubrise.OrderStatus{
		pkghubrise.StatusReceived, pkghubrise.StatusAccepted, pkghubrise.StatusInPreparation,
		pkghubrise.StatusAwaitingCollection, pkghubrise.StatusCompleted,
	} {
		s.epos(id, status)
		assert.Equal(t, want[status], s.localStatus(id), string(status))
		assert.Equal(t, string(status), *s.link(id).LastRemoteStatus)
	}
	assert.Len(t, s.tr.all(), 4, "received changes nothing")
	assert.Empty(t, s.gets())
	assert.Equal(t, map[string]int{"done": 5}, s.eventStates())
}

func TestTheWholeLifeOfADeliveryOrderFollowsHubRise(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{orderType: "DELIVERY"})

	s.epos(id, pkghubrise.StatusAccepted)
	s.epos(id, pkghubrise.StatusInPreparation)
	s.epos(id, pkghubrise.StatusAwaitingCollection)
	assert.Equal(t, "PREPARING", s.localStatus(id), "ready for collection means nothing for a delivery")
	s.epos(id, pkghubrise.StatusInDelivery)
	assert.Equal(t, "OUT_FOR_DELIVERY", s.localStatus(id))
	s.epos(id, pkghubrise.StatusCompleted)
	assert.Equal(t, "DELIVERED", s.localStatus(id))
}

func TestARemoteStatusThatMeansNothingLocallyIsNotMirroredBack(t *testing.T) {
	// awaiting_collection on a delivery order: the shop keeps PREPARING, and the reconciler must not
	// read the difference as a missed local change and PATCH the old status over the EPOS's.
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{orderType: "DELIVERY"})
	s.epos(id, pkghubrise.StatusInPreparation)
	s.epos(id, pkghubrise.StatusAwaitingCollection)

	require.NoError(t, s.push.Reconciler.RunOnce(t.Context()))
	s.drainAll()
	assert.Empty(t, s.patches())
	assert.Equal(t, 0, s.count(`SELECT count(*) FROM hubrise_outbox WHERE order_id = $1 AND kind = 'order_status'`, id))
}

func TestARemoteConfirmedTimeUpdatesTheEstimatedReadyTime(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remoteID := s.remote(id).ID
	patch := func(p pkghubrise.OrderPatch) {
		t.Helper()
		s.clock.Advance(10 * time.Second)
		_, err := s.srv.PatchOrderAs(hubrisetest.EPOSToken, remoteID, p)
		require.NoError(t, err)
		s.deliver()
		s.process()
	}

	ready := s.clock.Now().Add(25 * time.Minute).Truncate(time.Second)
	patch(pkghubrise.OrderPatch{Status: pkghubrise.StatusAccepted, ConfirmedTime: &ready})
	calls := s.tr.all()
	require.Len(t, calls, 1)
	assert.Equal(t, domain.LocalConfirmed, calls[0].change.Status)
	require.NotNil(t, calls[0].change.EstimatedReadyTime)
	assert.True(t, ready.Equal(*calls[0].change.EstimatedReadyTime))

	later := ready.Add(10 * time.Minute)
	patch(pkghubrise.OrderPatch{ConfirmedTime: &later})
	calls = s.tr.all()
	require.Len(t, calls, 2)
	assert.Empty(t, calls[1].change.Status, "only the time changed")
	assert.True(t, later.Equal(*calls[1].change.EstimatedReadyTime))

	patch(pkghubrise.OrderPatch{ConfirmedTime: &later}) // the same time again
	assert.Len(t, s.tr.all(), 2, "nothing to change")

	t.Run("not on an order that ends", func(t *testing.T) {
		end := later.Add(time.Hour)
		patch(pkghubrise.OrderPatch{Status: pkghubrise.StatusCompleted, ConfirmedTime: &end})
		last := s.tr.all()[2]
		assert.Equal(t, domain.LocalPickedUp, last.change.Status)
		assert.Nil(t, last.change.EstimatedReadyTime)
	})
}

func TestALocalReadyTimeEditSurvivesLaterRemoteEvents(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remoteID := s.remote(id).ID
	patch := func(p pkghubrise.OrderPatch) {
		t.Helper()
		s.clock.Advance(10 * time.Second)
		_, err := s.srv.PatchOrderAs(hubrisetest.EPOSToken, remoteID, p)
		require.NoError(t, err)
		s.deliver()
		s.process()
	}

	ready := s.clock.Now().Add(25 * time.Minute).Truncate(time.Second)
	patch(pkghubrise.OrderPatch{Status: pkghubrise.StatusAccepted, ConfirmedTime: &ready})
	require.Len(t, s.tr.all(), 1)
	require.NotNil(t, s.link(id).LastRemoteConfirmedTime)
	assert.True(t, ready.Equal(*s.link(id).LastRemoteConfirmedTime))

	// Staff push the ready time back in the dashboard; nothing sends that to HubRise.
	edited := ready.Add(30 * time.Minute)
	s.exec(`UPDATE orders SET estimated_ready_time = $2 WHERE id = $1`, id, edited)

	// The EPOS moves on without touching the time: the local edit is not reset.
	patch(pkghubrise.OrderPatch{Status: pkghubrise.StatusInPreparation})
	calls := s.tr.all()
	require.Len(t, calls, 2)
	assert.Equal(t, domain.LocalPreparing, calls[1].change.Status)
	assert.Nil(t, calls[1].change.EstimatedReadyTime, "the confirmed time did not change at HubRise")

	// A new confirmed time at HubRise does win.
	newer := edited.Add(15 * time.Minute)
	patch(pkghubrise.OrderPatch{ConfirmedTime: &newer})
	calls = s.tr.all()
	require.Len(t, calls, 3)
	require.NotNil(t, calls[2].change.EstimatedReadyTime)
	assert.True(t, newer.Equal(*calls[2].change.EstimatedReadyTime))
}

func TestARemoteStatusWaitsForALocalStatusThatIsStillQueued(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remoteID := s.remote(id).ID

	// The shop prepares the order but the PATCH has not left yet (queued, or backing off).
	s.setStatus(id, "PREPARING")
	require.NotEmpty(t, s.outboxRows(id, domain.OutboxOrderStatus))

	// A late HubRise event still carries the older status.
	s.clock.Advance(time.Minute)
	_, err := s.srv.PatchOrderAs(hubrisetest.EPOSToken, remoteID, pkghubrise.OrderPatch{Status: pkghubrise.StatusAccepted})
	require.NoError(t, err)
	s.deliver()
	s.process()
	assert.Empty(t, s.tr.all(), "the local order is not moved back")
	assert.Equal(t, "PREPARING", s.localStatus(id))
	assert.Equal(t, map[string]int{"pending": 1}, s.eventStates(), "the event waits")
	assert.NotEqual(t, "accepted", *s.link(id).LastRemoteStatus, "and nothing is recorded from it")

	// The reconciler gives way too.
	require.NoError(t, s.sync.Status.ApplyRemote(t.Context(), id, s.remote2(remoteID)))
	assert.Empty(t, s.tr.all())

	// Once the PATCH has gone out the event is looked at again, next to our PATCH, and HubRise wins.
	s.drainAll()
	s.clock.Advance(time.Minute)
	s.process()
	assert.Equal(t, "PREPARING", s.localStatus(id))
	assert.Empty(t, s.tr.all())
	assert.Equal(t, map[string]int{"done": 1}, s.eventStates())
	assert.Equal(t, "in_preparation", *s.link(id).LastRemoteStatus)
}

// ---- cancellations ----

func TestARemoteCancelCancelsAnUnpaidOrder(t *testing.T) {
	for _, status := range []pkghubrise.OrderStatus{pkghubrise.StatusCancelled, pkghubrise.StatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			s := newSyncEnv(t)
			id := s.pushed(orderSpec{})
			s.epos(id, status)
			require.Len(t, s.tr.all(), 1)
			assert.Equal(t, application.OrderChange{Status: domain.LocalCancelled}, s.tr.all()[0].change)
			assert.Equal(t, "CANCELLED", s.localStatus(id))
			assert.Empty(t, s.attention(id))
		})
	}
}

func TestARemoteCancelOfAnOnlinePaidOrderWaitsForStaffByDefault(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{online: true, paid: true, total: "12.80"})
	s.epos(id, pkghubrise.StatusCancelled)

	assert.Empty(t, s.tr.all(), "an EPOS mis-tap must never refund")
	assert.Equal(t, "PENDING", s.localStatus(id))
	assert.Equal(t, domain.AttentionRemoteCancelledPaid, s.attention(id))
	assert.Equal(t, "cancelled", *s.link(id).LastRemoteStatus)
	assert.Equal(t, map[string]int{"done": 1}, s.eventStates())

	// Staff confirm by cancelling in the dashboard: the matching PATCH is skipped, HubRise says so already.
	s.setStatus(id, "CANCELLED")
	s.drainAll()
	assert.Empty(t, s.patches())
	rows := s.outboxRows(id, domain.OutboxOrderStatus)
	require.Len(t, rows, 1)
	assert.Equal(t, domain.OutboxSkipped, rows[0].State)
}

func TestARemoteCancelOfAnOnlinePaidOrderRefundsWhenTheConnectionSaysSo(t *testing.T) {
	s := newSyncEnv(t)
	s.exec(`UPDATE hubrise_connection SET auto_refund_on_cancel = TRUE`)
	id := s.pushed(orderSpec{online: true, paid: true, total: "12.80"})
	s.epos(id, pkghubrise.StatusRejected)

	require.Len(t, s.tr.all(), 1)
	assert.Equal(t, application.OrderChange{Status: domain.LocalCancelled}, s.tr.all()[0].change)
	assert.Equal(t, "CANCELLED", s.localStatus(id))
	assert.Empty(t, s.attention(id))
}

func TestARemoteCancelThatCannotBeRefundedLeavesTheOrderAndFlagsIt(t *testing.T) {
	s := newSyncEnv(t)
	s.exec(`UPDATE hubrise_connection SET auto_refund_on_cancel = TRUE`)
	id := s.pushed(orderSpec{online: true, paid: true, total: "12.80"})
	s.tr.setErr(fmt.Errorf("%w: voucher payment", application.ErrNotRefundable))
	s.eposQuiet(id, pkghubrise.StatusCancelled)
	s.deliver()
	evID := s.queuedIDs()[0]

	s.process()

	assert.Equal(t, "PENDING", s.localStatus(id), "untouched")
	assert.Equal(t, domain.AttentionRemoteCancelFailed, s.attention(id))
	ev := s.event(evID)
	assert.Equal(t, domain.EventPending, ev.State, "retried with backoff")
	assert.Equal(t, 1, ev.Attempts)
	require.NotNil(t, ev.NextAttemptAt)
	assert.True(t, ev.NextAttemptAt.After(s.clock.Now()))
	require.NotNil(t, ev.LastError)
	assert.Contains(t, *ev.LastError, "cannot be refunded")
	assert.Equal(t, 0, s.process(), "backing off: not due yet")

	// The attempts run out and the event fails for good.
	for range 8 {
		s.clock.Advance(31 * time.Minute)
		s.process()
	}
	ev = s.event(evID)
	assert.Equal(t, domain.EventFailed, ev.State)
	assert.Equal(t, 8, ev.Attempts)
	assert.Len(t, s.tr.all(), 8)
	assert.Equal(t, domain.AttentionRemoteCancelFailed, s.attention(id))
}

func TestARemoteCancelThatKeepsFailingFlagsTheOrderWhenItGivesUp(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.tr.setErr(errors.New("database is down"))
	s.eposQuiet(id, pkghubrise.StatusCancelled)
	s.deliver()
	s.process()
	assert.Empty(t, s.attention(id), "a transient failure is not flagged yet")
	for range 8 {
		s.clock.Advance(31 * time.Minute)
		s.process()
	}
	assert.Equal(t, map[string]int{"failed": 1}, s.eventStates())
	assert.Equal(t, domain.AttentionRemoteCancelFailed, s.attention(id))
}

func TestARemoteFailureOfAnotherKindDoesNotFlagTheOrder(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.tr.setErr(errors.New("database is down"))
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.deliver()
	for range 9 {
		s.process()
		s.clock.Advance(31 * time.Minute)
	}
	assert.Equal(t, map[string]int{"failed": 1}, s.eventStates())
	assert.Empty(t, s.attention(id), "a failed confirmation is not a cancel problem")

	// The error is transient: the same order is read by the reconciler and caught up.
	s.tr.setErr(nil)
	require.NoError(t, s.push.Reconciler.RunOnce(t.Context()))
}

func TestADeliveryFailureOnlyFlagsTheOrder(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{orderType: "DELIVERY"})
	s.epos(id, pkghubrise.StatusInDelivery)
	s.epos(id, pkghubrise.StatusDeliveryFailed)
	assert.Equal(t, "OUT_FOR_DELIVERY", s.localStatus(id))
	assert.Equal(t, domain.AttentionDeliveryFailed, s.attention(id))
	assert.Len(t, s.tr.all(), 1)
}

// ---- what is never moved back ----

func TestAFinishedOrderIsNeverMovedBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		local  string
		remote pkghubrise.OrderStatus
	}{
		{"delivered then preparation", "DELIVERED", pkghubrise.StatusInPreparation},
		{"picked up then cancelled", "PICKED_UP", pkghubrise.StatusCancelled},
		{"cancelled then accepted", "CANCELLED", pkghubrise.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSyncEnv(t)
			id := s.pushed(orderSpec{})
			s.exec(`UPDATE orders SET order_status = $2 WHERE id = $1`, id, tc.local)
			s.epos(id, tc.remote)
			assert.Empty(t, s.tr.all())
			assert.Equal(t, tc.local, s.localStatus(id))
			assert.Equal(t, domain.AttentionStatusConflict, s.attention(id))
			assert.Equal(t, string(tc.remote), *s.link(id).LastRemoteStatus, "what HubRise says is still recorded")
			assert.Equal(t, map[string]int{"done": 1}, s.eventStates())
		})
	}
}

func TestAnEqualStatusChangesNothing(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.exec(`UPDATE orders SET order_status = 'CONFIRMED' WHERE id = $1`, id)
	s.epos(id, pkghubrise.StatusAccepted)
	assert.Empty(t, s.tr.all())
	assert.Empty(t, s.attention(id))
	assert.Equal(t, "accepted", *s.link(id).LastRemoteStatus)

	// And a remote cancel of an order already cancelled here.
	s.exec(`UPDATE orders SET order_status = 'CANCELLED' WHERE id = $1`, id)
	s.epos(id, pkghubrise.StatusCancelled)
	assert.Empty(t, s.tr.all())
	assert.Empty(t, s.attention(id))
}

func TestACancelledOrderThatIsGoneIsIgnoredQuietly(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.deliver()
	s.exec(`DELETE FROM orders WHERE id = $1`, id) // the link goes with it
	s.process()
	assert.Equal(t, map[string]int{"ignored": 1}, s.eventStates())
}

func (s *syncEnv) remote2(remoteID string) *pkghubrise.Order {
	s.t.Helper()
	o, ok := s.srv.Order(remoteID)
	require.True(s.t, ok)
	return &o
}

func (s *syncEnv) queuedIDs() []string {
	var ids []string
	for _, e := range s.queued() {
		ids = append(ids, e.ID)
	}
	if len(ids) == 0 {
		var stored []string
		require.NoError(s.t, s.db.SelectContext(s.t.Context(), &stored, `SELECT id FROM hubrise_events ORDER BY received_at, id`))
		return stored
	}
	return ids
}

// ---- orders we do not know ----

func TestOrdersOfOtherChannelsAreReadOnceAndThenIgnored(t *testing.T) {
	s := newSyncEnv(t)
	foreign, err := s.srv.InjectOrder(pkghubrise.Order{Status: pkghubrise.StatusNew, ServiceType: pkghubrise.ServiceCollection, PrivateRef: "their-ref"})
	require.NoError(t, err)

	s.clock.Advance(time.Minute)
	require.NoError(t, s.srv.SetOrderStatus(foreign.ID, pkghubrise.StatusAccepted))
	s.deliver()
	s.process()
	assert.Len(t, s.gets(), 1, "read once to see whose it is")
	assert.Equal(t, map[string]int{"ignored": 1}, s.eventStates())

	s.clock.Advance(time.Minute)
	require.NoError(t, s.srv.SetOrderStatus(foreign.ID, pkghubrise.StatusInPreparation))
	s.deliver()
	s.process()
	assert.Len(t, s.gets(), 1, "the cache answers: no second read")
	assert.Equal(t, map[string]int{"ignored": 2}, s.eventStates())
	assert.Empty(t, s.tr.all())
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_foreign_orders WHERE hubrise_order_id = $1`, foreign.ID))
}

func TestAnOrderWhosePrivateRefIsNotOursIsForeign(t *testing.T) {
	s := newSyncEnv(t)
	pushed := s.pushed(orderSpec{})
	client := s.srv.NewClient(t)
	create := func(ref string) pkghubrise.Order {
		o, err := client.CreateOrder(t.Context(), &pkghubrise.Order{Status: pkghubrise.StatusNew, ServiceType: pkghubrise.ServiceCollection, PrivateRef: ref})
		require.NoError(t, err)
		return *o
	}
	for name, ref := range map[string]string{
		"not a uuid":                          "hello",
		"no such order":                       uuid.NewString(),
		"the private_ref of an order we have": pushed.String(),
	} {
		t.Run(name, func(t *testing.T) {
			o := create(ref)
			s.clock.Advance(time.Minute)
			require.NoError(t, s.srv.SetOrderStatus(o.ID, pkghubrise.StatusAccepted))
			s.deliver()
			s.process()
			assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_foreign_orders WHERE hubrise_order_id = $1`, o.ID))
			assert.Empty(t, s.tr.all())
		})
	}
	assert.Equal(t, domain.PushSent, s.link(pushed).PushState, "the real order keeps its link")
}

func TestAnEventForAnOrderThatIsGoneAtHubRiseIsIgnored(t *testing.T) {
	s := newSyncEnv(t)
	foreign, err := s.srv.InjectOrder(pkghubrise.Order{Status: pkghubrise.StatusNew, ServiceType: pkghubrise.ServiceCollection})
	require.NoError(t, err)
	s.clock.Advance(time.Minute)
	require.NoError(t, s.srv.SetOrderStatus(foreign.ID, pkghubrise.StatusAccepted))
	s.deliver()
	s.srv.DeleteOrder(foreign.ID)
	s.process()
	assert.Equal(t, map[string]int{"ignored": 1}, s.eventStates())
	assert.Zero(t, s.count(`SELECT count(*) FROM hubrise_foreign_orders`), "gone is not foreign")
}

func TestOrderCreateEventsAreStoredAndIgnoredWithoutAnyCall(t *testing.T) {
	s := newSyncEnv(t)
	_, err := s.srv.NewClient(t).SetCallback(t.Context(), pkghubrise.Callback{
		URL: s.server.URL + "/hubrise/callback", Events: map[string][]string{"order": {"create", "update"}},
	})
	require.NoError(t, err)
	_, err = s.srv.InjectOrder(pkghubrise.Order{Status: pkghubrise.StatusNew, ServiceType: pkghubrise.ServiceCollection})
	require.NoError(t, err)
	s.srv.ClearRequests()

	s.deliver()
	assert.Equal(t, 1, s.process())
	assert.Equal(t, map[string]int{"ignored": 1}, s.eventStates())
	assert.Empty(t, s.srv.Requests(), "no API call")
}

// The answer to our POST was lost, and the EPOS has already accepted the order: the event arrives for
// an id we have not recorded. It must wait for the push to settle, not be taken for a foreign order.
func TestAnEventForAnOrderWhosePushAnswerWasLostWaitsForThePush(t *testing.T) {
	setup := func(t *testing.T) (*syncEnv, uuid.UUID, string) {
		s := newSyncEnv(t)
		s.linkProduct(s.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := s.order(orderSpec{})
		s.ready(id)
		s.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: "/v1/location/orders", Times: 1, DropAfter: true})
		s.drain()
		require.Len(t, s.srv.Orders(), 1)
		remoteID := s.srv.Orders()[0].ID
		assert.Nil(t, s.link(id).HubriseOrderID, "we never learned the id")
		s.srv.ClearRequests()
		s.clock.Advance(time.Second)
		require.NoError(t, s.srv.SetOrderStatus(remoteID, pkghubrise.StatusAccepted))
		s.deliver()
		return s, id, remoteID
	}

	t.Run("the retry of the push recognises the order, then the event is applied", func(t *testing.T) {
		s, id, _ := setup(t)
		evID := s.queuedIDs()[0]
		s.process()
		ev := s.event(evID)
		assert.Equal(t, domain.EventPending, ev.State, "deferred, not ignored")
		assert.Equal(t, 0, ev.Attempts, "waiting is not failing")
		require.NotNil(t, ev.NextAttemptAt)
		assert.True(t, ev.NextAttemptAt.After(s.clock.Now()))
		assert.Empty(t, s.gets(), "no call while the push is unsettled")
		assert.Empty(t, s.tr.all())

		s.clock.Advance(31 * time.Second) // the push backs off, then adopts the order through private_ref
		s.drainAll()
		assert.Equal(t, domain.PushSent, s.link(id).PushState)
		assert.Len(t, s.posts(), 0, "no second POST")
		s.clock.Advance(time.Minute)
		s.process()
		assert.Equal(t, "CONFIRMED", s.localStatus(id))
		assert.Equal(t, map[string]int{"done": 1}, s.eventStates())
	})

	t.Run("after the defer window the order is matched through its private_ref", func(t *testing.T) {
		s, id, remoteID := setup(t)
		s.process()
		s.clock.Advance(11 * time.Minute) // the push is still backing off (we never drain)
		s.process()

		assert.Len(t, s.gets(), 1, "one read of the order")
		link := s.link(id)
		require.NotNil(t, link.HubriseOrderID)
		assert.Equal(t, remoteID, *link.HubriseOrderID, "backfilled")
		assert.Equal(t, domain.PushSent, link.PushState)
		assert.Equal(t, "CONFIRMED", s.localStatus(id))
		assert.Equal(t, 0, s.count(`SELECT count(*) FROM hubrise_foreign_orders`))

		// The create that was still queued finds the order adopted and only closes its row.
		s.srv.ClearRequests()
		s.clock.Advance(time.Hour)
		s.drainAll()
		assert.Empty(t, s.posts())
		assert.Equal(t, domain.OutboxSent, s.outboxRows(id, domain.OutboxOrderCreate)[0].State)
	})
}

// ---- the ordering guard ----

func TestAnOldEventAfterANewerOneIsDropped(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remoteID := s.remote(id).ID

	s.clock.Advance(time.Minute)
	require.NoError(t, s.srv.SetOrderStatus(remoteID, pkghubrise.StatusAccepted))
	s.clock.Advance(30 * time.Second)
	require.NoError(t, s.srv.SetOrderStatus(remoteID, pkghubrise.StatusInPreparation))
	older, newer := s.queued()[0], s.queued()[1]

	// HubRise retried the first one: the newer event arrives first.
	assert.Equal(t, http.StatusOK, s.post(newer))
	s.process()
	assert.Equal(t, http.StatusOK, s.post(older))
	s.process()

	assert.Equal(t, "PREPARING", s.localStatus(id), "the stale event did not move the order back")
	assert.Len(t, s.tr.all(), 1)
	assert.Empty(t, s.gets(), "clearly older: dropped without a read")
	assert.Equal(t, map[string]int{"done": 1, "ignored": 1}, s.eventStates())
}

func TestAnEventThatIsNotClearlyNewerIsReadAgain(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remoteID := s.remote(id).ID
	s.clock.Advance(time.Minute)

	// Two changes in the same second: HubRise timestamps have a second of precision.
	require.NoError(t, s.srv.SetOrderStatus(remoteID, pkghubrise.StatusAccepted))
	require.NoError(t, s.srv.SetOrderStatus(remoteID, pkghubrise.StatusInPreparation))
	first, second := s.queued()[0], s.queued()[1]
	require.True(t, first.CreatedAt.Equal(second.CreatedAt))

	assert.Equal(t, http.StatusOK, s.post(second))
	s.process()
	assert.Empty(t, s.gets(), "the first one is trusted")
	assert.Equal(t, "PREPARING", s.localStatus(id))

	assert.Equal(t, http.StatusOK, s.post(first))
	s.process()
	assert.Len(t, s.gets(), 1, "the same instant as what we know: the order is read again")
	assert.Equal(t, "PREPARING", s.localStatus(id), "and what HubRise has now wins, not the stale payload")
	assert.Equal(t, "in_preparation", *s.link(id).LastRemoteStatus)
}

func TestAnEventNextToOurOwnPatchIsReadAgain(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})

	// The shop confirms; the PATCH goes out and is stamped.
	s.setStatus(id, "CONFIRMED")
	s.drainAll()
	require.NotNil(t, s.link(id).LastLocalPatchAt)
	assert.Equal(t, "accepted", *s.link(id).LastRemoteStatus)
	s.srv.ClearRequests()

	// Two seconds later the EPOS prepares the order: too close to our PATCH to trust the payload.
	s.clock.Advance(2 * time.Second)
	s.eposQuiet(id, pkghubrise.StatusInPreparation)
	s.deliver()
	s.process()
	assert.Len(t, s.gets(), 1, "read again")
	assert.Equal(t, "PREPARING", s.localStatus(id))

	// Far from any PATCH of ours the payload is trusted.
	s.srv.ClearRequests()
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAwaitingCollection)
	s.deliver()
	s.process()
	assert.Empty(t, s.gets())
	assert.Equal(t, "AWAITING_PICK_UP", s.localStatus(id))
}

func TestAnEventWithoutStateIsReadAgain(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	ev := s.queued()[0]
	ev.NewState, ev.PreviousState = nil, nil

	assert.Equal(t, http.StatusOK, s.post(ev))
	s.process()
	assert.Len(t, s.gets(), 1)
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
	require.NotNil(t, s.link(id).LastRemoteEventAt)
}

func TestAnEventWithAnUnknownStatusIsReadAgain(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	ev := s.queued()[0]
	ev.NewState = json.RawMessage(`{"id":"` + ev.OrderID + `","status":"sideways"}`)
	assert.Equal(t, http.StatusOK, s.post(ev))
	s.process()
	assert.Len(t, s.gets(), 1)
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
}

func TestARedeliveredEventIsAppliedOnce(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)

	keep := hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Client: s.server.Client(), Keep: true}
	for range 3 {
		for _, d := range s.srv.Dispatch(t.Context(), keep) {
			assert.Equal(t, http.StatusOK, d.Status)
		}
		s.process()
	}
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`))
	assert.Len(t, s.tr.all(), 1)
}

// ---- the read budget, the order lease and refusals ----

func TestEventsBeyondTheReadBudgetAreDeferred(t *testing.T) {
	s := newSyncEnv(t)
	s.sync = s.rebuildSync(application.EventProcessorConfig{ReadsPerMinute: 1})
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.clock.Advance(10 * time.Second)
	s.eposQuiet(id, pkghubrise.StatusInPreparation)
	for _, ev := range s.queued() { // both without state: each needs a read
		ev.NewState = nil
		assert.Equal(t, http.StatusOK, s.post(ev))
	}
	s.srv.ClearRequests()
	assert.Equal(t, 2, s.process())
	assert.Len(t, s.gets(), 1, "one read this minute")
	states := s.eventStates()
	assert.Equal(t, 1, states["pending"], "the other one waits")
	s.clock.Advance(time.Minute)
	s.process()
	assert.Equal(t, map[string]int{"done": 2}, s.eventStates())
	assert.Equal(t, "PREPARING", s.localStatus(id))
}

func TestAnOrderBeingUpdatedElsewhereIsRetriedSoon(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	ok, err := s.mod.Leases.Acquire(t.Context(), "order-status:"+id.String(), "another replica", s.clock.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.deliver()
	s.process()
	assert.Empty(t, s.tr.all())
	assert.Equal(t, map[string]int{"pending": 1}, s.eventStates())

	s.clock.Advance(2 * time.Minute) // the other replica's lease ran out
	s.process()
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
	holder := s.count(`SELECT count(*) FROM hubrise_job_leases WHERE name = $1`, "order-status:"+id.String())
	assert.Equal(t, 0, holder, "the lease is released")
}

func TestAConnectionRefusalWhileReadingPausesTheProcessor(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.eposQuiet(id, pkghubrise.StatusInPreparation)
	for _, ev := range s.queued() {
		ev.NewState = nil
		s.post(ev)
	}
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/orders/", Status: http.StatusUnauthorized})

	assert.Equal(t, 2, s.process())
	conn, err := s.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	require.NotNil(t, conn.AuthErrorAt, "the pipeline is paused")
	assert.Equal(t, map[string]int{"pending": 2}, s.eventStates(), "nothing was lost")
	assert.Equal(t, 0, s.process(), "paused: nothing is claimed")
}

func TestATransientReadFailureIsRetried(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	ev := s.queued()[0]
	ev.NewState = nil
	s.post(ev)
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/location/orders/", Times: 1, Status: http.StatusInternalServerError})

	s.process()
	assert.Equal(t, domain.EventPending, s.event(ev.ID).State)
	assert.Equal(t, 1, s.event(ev.ID).Attempts)
	s.clock.Advance(time.Minute)
	s.process()
	assert.Equal(t, map[string]int{"done": 1}, s.eventStates())
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
}

func TestABrokenClientIsRetried(t *testing.T) {
	s := newSyncEnv(t, withClients(func(*env) application.ClientProvider { return brokenClients{} }))
	id := s.order(orderSpec{})
	s.exec(`INSERT INTO hubrise_orders (order_id, hubrise_order_id, push_state, last_remote_status) VALUES ($1, 'ho-x', 'sent', 'new')`, id)
	ev := pkghubrise.Event{
		ID: "e-1", ResourceType: "order", EventType: "update", LocationID: hubrisetest.LocationID, OrderID: "ho-x",
		CreatedAt: s.clock.Now(),
	}
	assert.Equal(t, http.StatusOK, s.post(ev)) // no state: it needs a read, and there is no client
	s.process()
	got := s.event("e-1")
	assert.Equal(t, domain.EventPending, got.State)
	assert.Equal(t, 1, got.Attempts)
	require.NotNil(t, got.LastError)
}

func (s *syncEnv) rebuildSync(cfg application.EventProcessorConfig) *hubrise.Sync {
	return s.mod.NewSync(s.push, hubrise.SyncOptions{Transitioner: s.tr, Holder: "test", Processor: cfg})
}

// ---- paused and standalone ----

func TestNothingIsProcessedWhileTheIntegrationIsOffOrPaused(t *testing.T) {
	for name, apply := range map[string]func(s *syncEnv){
		"disabled":   func(s *syncEnv) { s.exec(`UPDATE hubrise_connection SET enabled = FALSE`) },
		"auth error": func(s *syncEnv) { s.exec(`UPDATE hubrise_connection SET auth_error_at = now()`) },
		"relink":     func(s *syncEnv) { s.exec(`UPDATE hubrise_connection SET relink_required_at = now()`) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newSyncEnv(t)
			id := s.pushed(orderSpec{})
			s.clock.Advance(time.Minute)
			s.eposQuiet(id, pkghubrise.StatusAccepted)
			s.deliver()
			assert.Equal(t, map[string]int{"pending": 1}, s.eventStates())
			apply(s)
			s.srv.ClearRequests()
			assert.Equal(t, 0, s.process())
			assert.Empty(t, s.tr.all())
			assert.Empty(t, s.srv.Requests(), "zero calls")
			assert.Equal(t, map[string]int{"pending": 1}, s.eventStates())
		})
	}
}

func TestEventsAreIgnoredWhileStatusSyncIsOff(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.exec(`UPDATE hubrise_connection SET sync_status = FALSE`)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.deliver()
	s.process()
	assert.Equal(t, map[string]int{"ignored": 1}, s.eventStates())
	assert.Empty(t, s.tr.all())
}

func TestNoConnectionMeansNothingToProcess(t *testing.T) {
	s := newSyncEnv(t, withoutConnection())
	assert.Equal(t, 0, s.process())
	require.NoError(t, s.sync.Status.ApplyRemote(t.Context(), uuid.New(), &pkghubrise.Order{Status: pkghubrise.StatusAccepted}))
}

func TestAnUnreadableOrIrrelevantEventIsSettled(t *testing.T) {
	s := newSyncEnv(t)
	insert := func(id, kind, payload string) {
		s.exec(`INSERT INTO hubrise_events (id, resource_type, event_type, payload, received_at) VALUES ($1, 'order', $2, $3::jsonb, $4)`,
			id, kind, payload, s.clock.Now())
	}
	insert("junk", "update", `{"nothing": true}`)
	insert("orderless", "update", `{"id":"orderless","resource_type":"order","event_type":"update"}`)
	insert("deleted", "delete", `{"id":"deleted","resource_type":"order","event_type":"delete","order_id":"o1"}`)
	s.srv.ClearRequests()
	s.process()
	assert.Equal(t, domain.EventFailed, s.event("junk").State)
	assert.Equal(t, domain.EventIgnored, s.event("orderless").State)
	assert.Equal(t, domain.EventIgnored, s.event("deleted").State)
	assert.Empty(t, s.srv.Requests())
}

func TestAnEventForTheOrderIdOfItsStateIsRecognised(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remote := s.remote(id)
	s.clock.Advance(time.Minute)
	payload := fmt.Sprintf(`{"id":"e-1","resource_type":"order","event_type":"update","location_id":"%s","created_at":"%s","new_state":{"id":"%s","status":"accepted"}}`,
		hubrisetest.LocationID, s.clock.Now().Format(time.RFC3339), remote.ID)
	s.exec(`INSERT INTO hubrise_events (id, resource_type, event_type, payload, received_at) VALUES ('e-1', 'order', 'update', $1::jsonb, $2)`, payload, s.clock.Now())
	s.process()
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
}

// ---- the reconciler uses the same rules ----

func TestTheReconcilerAppliesWhatItReadsBackFromHubRise(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusInPreparation) // the event is lost: it is never delivered

	require.NoError(t, s.push.Reconciler.RunOnce(t.Context()))
	assert.Equal(t, "PREPARING", s.localStatus(id))
	assert.Equal(t, "in_preparation", *s.link(id).LastRemoteStatus)
	assert.Empty(t, s.patches(), "no echo")

	// Nothing more to do on the next run.
	s.srv.ClearRequests()
	require.NoError(t, s.push.Reconciler.RunOnce(t.Context()))
	assert.Len(t, s.tr.all(), 1)
}

func TestTheReconcilerSkipsAnOrderAnotherProcessIsUpdating(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusInPreparation)
	ok, err := s.mod.Leases.Acquire(t.Context(), "order-status:"+id.String(), "another replica", s.clock.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.push.Reconciler.RunOnce(t.Context()))
	assert.Empty(t, s.tr.all())
}

func TestTheReconcilerDoesNothingWhenStatusSyncIsOff(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	remote := s.remote2(s.remote(id).ID)
	s.exec(`UPDATE hubrise_connection SET sync_status = FALSE`)
	require.NoError(t, s.sync.Status.ApplyRemote(t.Context(), id, remote))
	assert.Empty(t, s.tr.all())
	require.NoError(t, s.sync.Status.ApplyRemote(t.Context(), uuid.New(), remote))
}

func TestClearingTheNotAcceptedFlagOnceHubRiseMovesOn(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.exec(`UPDATE hubrise_orders SET needs_attention = 'NOT_ACCEPTED' WHERE order_id = $1`, id)
	s.epos(id, pkghubrise.StatusAccepted)
	assert.Empty(t, s.attention(id))
}

func TestAnEventOlderThanOurLatestPatchIsReadAgainHoweverLongThePatchWaited(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	stale := s.queued()[0]

	// The shop moves the order on, but its PATCH only goes out half a minute later, long after the
	// window around the event.
	s.clock.Advance(30 * time.Second)
	s.setStatus(id, "PREPARING")
	s.drainAll()
	require.NotNil(t, s.link(id).LastLocalPatchAt)
	assert.Equal(t, "in_preparation", *s.link(id).LastRemoteStatus)
	s.srv.ClearRequests()

	// The event of the acceptance is delivered late: its payload must not move the order back.
	assert.Equal(t, http.StatusOK, s.post(stale))
	s.process()
	assert.Len(t, s.gets(), 1, "read again")
	assert.Equal(t, "PREPARING", s.localStatus(id))
	assert.Equal(t, "in_preparation", *s.link(id).LastRemoteStatus)
}
