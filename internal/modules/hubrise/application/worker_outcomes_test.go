package application_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func TestRetryableFailuresBackOffFrom30SecondsAndDouble(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 500})

	start := e.clock.Now()
	wants := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, want := range wants {
		// A failing attempt: a lookup first when the row was claimed before (it finds nothing).
		e.clock.Advance(time.Duration(i+1) * 10 * time.Minute)
		now := e.clock.Now()
		require.Equal(t, 1, e.drain())
		row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
		assert.Equal(t, domain.OutboxPending, row.State, "attempt %d", i+1)
		assert.Equal(t, i+1, row.Attempts)
		assert.WithinDuration(t, now.Add(want), row.NextAttemptAt, time.Second, "attempt %d backs off %s", i+1, want)
		require.NotNil(t, row.LastError)
		assert.Contains(t, *row.LastError, "500")
	}
	assert.True(t, e.clock.Now().After(start))
	assert.Empty(t, e.srv.Orders())
	// The order stays flagged for nobody: a plain outage is not a problem of the order.
	assert.Nil(t, e.link(id).NeedsAttention)
}

func TestARateLimitedCallWaitsForRetryAfter(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 429, RetryAfter: 10 * time.Minute, Times: 1})
	now := e.clock.Now()
	e.drain()
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.WithinDuration(t, now.Add(10*time.Minute), row.NextAttemptAt, time.Second, "Retry-After beats the 30 s backoff")
}

func TestTheClaimHandsOverToTheNextAttemptOnlyWhenDue(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 503, Times: 1})
	e.drain()
	assert.Equal(t, 0, e.drain())
	e.clock.Advance(29 * time.Second)
	assert.Equal(t, 0, e.drain())
	e.clock.Advance(2 * time.Second)
	assert.Equal(t, 1, e.drain())
	assert.Equal(t, domain.PushSent, e.link(id).PushState)
}

func TestARefusedConnectionPausesThePipelineWithoutParkingAnything(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			e := newEnv(t)
			first, second := e.order(orderSpec{}), e.order(orderSpec{})
			e.ready(first)
			e.ready(second)
			e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: status})

			assert.Equal(t, 2, e.drain(), "both rows were claimed in one batch")
			conn, err := e.mod.Connections.Get(t.Context())
			require.NoError(t, err)
			require.NotNil(t, conn.AuthErrorAt, "the pipeline is paused")
			require.NotNil(t, conn.LastError)
			assert.NotContains(t, *conn.LastError, hubrisetest.Token, "the token never appears in a stored error")
			for _, oid := range []string{first.String(), second.String()} {
				var state string
				var attempts int
				require.NoError(t, e.db.QueryRowxContext(t.Context(),
					`SELECT state, attempts FROM hubrise_outbox WHERE order_id = $1`, oid).Scan(&state, &attempts))
				assert.Equal(t, "pending", state, "nothing is parked")
				assert.Zero(t, attempts, "the claim's attempt is given back")
			}
			assert.Len(t, e.posts(), 1, "the second row of the batch was not even tried")

			// While paused nothing is claimed; a reconnect resumes.
			assert.Equal(t, 0, e.drain())
			e.srv.ClearFaults()
			require.NoError(t, e.mod.Connections.ClearAuthError(t.Context()))
			e.drainAll()
			assert.Len(t, e.srv.Orders(), 2)
		})
	}
}

func TestInvalidOrdersAreParkedAndAManualRetryResumes(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	id := e.order(orderSpec{})
	e.ready(id)
	e.setStatus(id, "CONFIRMED")
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 422, Times: 1})

	e.drainAll()
	create := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxParked, create.State)
	require.NotNil(t, create.LastError)
	assert.Contains(t, *create.LastError, "422")
	link := e.link(id)
	assert.Equal(t, domain.PushParked, link.PushState)
	require.NotNil(t, link.NeedsAttention)
	assert.Equal(t, domain.AttentionPushParked, *link.NeedsAttention)
	for _, r := range e.outboxRows(id, domain.OutboxOrderStatus) {
		assert.Equal(t, domain.OutboxSkipped, r.State, "no PATCH for an order HubRise never received")
	}
	assert.Empty(t, e.srv.Orders())
	e.setStatus(id, "PREPARING")
	assert.Empty(t, e.outboxRows(id, domain.OutboxOrderStatus)[1:], "a parked order queues no more status rows")

	n, err := e.mod.Outbox.RetryParked(t.Context(), create.ID, e.clock.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	e.drainAll()
	require.Len(t, e.srv.Orders(), 1)
	assert.Equal(t, pkghubrise.StatusInPreparation, e.remote(id).Status, "the retry is built with the current status")
	link = e.link(id)
	assert.Equal(t, domain.PushSent, link.PushState)
	assert.Nil(t, link.NeedsAttention, "the parked flag is lifted once the order is sent")
}

func TestAnUnknownErrorStatusIsRetried(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Status: 418, Times: 1})
	e.drain()
	assert.Equal(t, domain.OutboxPending, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
}

func TestTimeoutsAreRetried(t *testing.T) {
	e := newEnv(t, withPush(hubriseWorker(80*time.Millisecond)))
	id := e.order(orderSpec{})
	e.ready(id)
	e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPost, Path: e.ordersPath(), Delay: time.Second, Times: 1})
	e.drain()
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	require.NotNil(t, row.LastError)
}

func TestTooOldRowsAreNotSent(t *testing.T) {
	t.Run("a create older than 48 hours is parked", func(t *testing.T) {
		e := newEnv(t)
		id := e.order(orderSpec{})
		e.ready(id)
		e.clock.Advance(49 * time.Hour)
		e.drainAll()
		assert.Empty(t, e.srv.Requests())
		assert.Equal(t, domain.OutboxParked, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
		assert.Equal(t, domain.PushParked, e.link(id).PushState)
		assert.Equal(t, domain.AttentionPushParked, *e.link(id).NeedsAttention)

		// A manual retry restarts the age: the order goes out this time.
		_, err := e.mod.Outbox.RetryAllParked(t.Context(), e.clock.Now())
		require.NoError(t, err)
		e.drainAll()
		require.Len(t, e.srv.Orders(), 1)
	})
	t.Run("a status change older than 6 hours is skipped and the reconciler sends the current one", func(t *testing.T) {
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.order(orderSpec{})
		e.ready(id)
		e.drainAll()
		e.setStatus(id, "PREPARING")
		e.clock.Advance(7 * time.Hour)
		e.drainAll()
		assert.Empty(t, e.patches())
		assert.Equal(t, domain.OutboxSkipped, e.outboxRows(id, domain.OutboxOrderStatus)[0].State)

		require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
		e.drainAll()
		assert.Equal(t, pkghubrise.StatusInPreparation, e.remote(id).Status)
	})
}

func TestRowsOfAnOrderThatNeverReachedHubRiseAreSkipped(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.drainAll()
	// The link lost its HubRise id (a manual repair): a status row has nothing to patch.
	e.exec(`UPDATE hubrise_orders SET hubrise_order_id = NULL WHERE order_id = $1`, id)
	e.setStatus(id, "CONFIRMED")
	e.drainAll()
	assert.Empty(t, e.patches())
	assert.Equal(t, domain.OutboxSkipped, e.outboxRows(id, domain.OutboxOrderStatus)[0].State)
}

func TestRowsWithoutALinkOrAnOrderAreSkipped(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	// Hand-made rows: a status row and a create row for an order with no link.
	e.rawRow("order_status", &id, `{"status":"CONFIRMED"}`)
	e.rawRow("order_create", &id, `{}`)
	e.rawRow("order_create", nil, `{}`)
	e.rawRow("order_status", nil, `{}`)
	e.drainAll()
	assert.Equal(t, 4, e.count(`SELECT count(*) FROM hubrise_outbox WHERE state = 'skipped'`))
	assert.Empty(t, e.srv.Requests())
}

func TestUnreadablePayloadsAreParked(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	e.exec(`UPDATE hubrise_outbox SET payload = '{"v": 99}' WHERE order_id = $1`, id)
	e.drainAll()
	assert.Equal(t, domain.OutboxParked, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)

	// And a status row with a payload that is not a status.
	id2 := e.order(orderSpec{})
	e.ready(id2)
	e.drainAll()
	e.rawRow("order_status", &id2, `[1]`)
	e.drainAll()
	assert.Equal(t, domain.OutboxParked, e.outboxRows(id2, domain.OutboxOrderStatus)[0].State)

	// A status that is never pushed is skipped.
	id3 := e.order(orderSpec{})
	e.ready(id3)
	e.drainAll()
	e.rawRow("order_status", &id3, `{"status":"PENDING"}`)
	e.drainAll()
	assert.Equal(t, domain.OutboxSkipped, e.outboxRows(id3, domain.OutboxOrderStatus)[0].State)
}

func TestAnOrderThatDisappearedIsSkipped(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	// Claim and delete the order from under the row (a cascade removes the row itself in production;
	// here the link survives, which is the narrow window the worker still has to survive).
	e.exec(`ALTER TABLE hubrise_outbox DROP CONSTRAINT hubrise_outbox_order_id_fkey`)
	e.exec(`ALTER TABLE hubrise_orders DROP CONSTRAINT hubrise_orders_order_id_fkey`)
	e.exec(`DELETE FROM order_product WHERE order_id = $1`, id)
	e.exec(`DELETE FROM orders WHERE id = $1`, id)
	e.drainAll()
	assert.Equal(t, domain.OutboxSkipped, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
	assert.Empty(t, e.srv.Requests())
}

func TestAMissingOrderIsRecoveredOnTheStatusPath(t *testing.T) {
	setup := func(t *testing.T) (*env, uuid.UUID) {
		t.Helper()
		e := newEnv(t)
		e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
		id := e.order(orderSpec{})
		e.ready(id)
		e.drainAll()
		return e, id
	}
	t.Run("HubRise knows the order under another id: the id is backfilled and the PATCH repeated", func(t *testing.T) {
		e, id := setup(t)
		oid := id
		e.exec(`UPDATE hubrise_orders SET hubrise_order_id = 'gone' WHERE order_id = $1`, oid)
		e.setStatus(oid, "CONFIRMED")
		e.drainAll()
		link := e.link(oid)
		assert.Equal(t, e.remote(oid).ID, *link.HubriseOrderID, "backfilled through private_ref")
		assert.Equal(t, pkghubrise.StatusAccepted, e.remote(oid).Status)
		assert.Equal(t, domain.OutboxSent, e.outboxRows(oid, domain.OutboxOrderStatus)[0].State)
	})
	t.Run("HubRise does not know it: it is created again, once, and the queued statuses are covered", func(t *testing.T) {
		e, id := setup(t)
		oid := id
		e.exec(`UPDATE hubrise_orders SET hubrise_order_id = 'ghost' WHERE order_id = $1`, oid)
		e.exec(`DELETE FROM hubrise_outbox WHERE kind = 'order_create'`) // as if the create were long gone
		e.rawRow("order_create", &oid, `{}`, "sent")
		e.srv.ClearRequests()
		hideRemote(t, e, oid)
		e.setStatus(oid, "CONFIRMED")
		e.setStatus(oid, "PREPARING")
		e.drainAll()

		creates := e.outboxRows(oid, domain.OutboxOrderCreate)
		require.Len(t, creates, 2, "one re-created order_create")
		assert.Equal(t, domain.OutboxSent, creates[1].State)
		for _, r := range e.outboxRows(oid, domain.OutboxOrderStatus) {
			assert.Equal(t, domain.OutboxSkipped, r.State, "covered by the re-created order")
		}
		assert.Equal(t, domain.PushSent, e.link(oid).PushState)
		assert.Equal(t, pkghubrise.StatusInPreparation, e.remote(oid).Status, "created again with the current status")
	})
	t.Run("a second loss parks the row and flags the order", func(t *testing.T) {
		e, id := setup(t)
		oid := id
		e.exec(`UPDATE hubrise_orders SET hubrise_order_id = 'ghost' WHERE order_id = $1`, oid)
		e.rawRow("order_create", &oid, `{}`, "sent")
		hideRemote(t, e, oid)
		e.setStatus(oid, "CONFIRMED")
		e.drainAll()
		rows := e.outboxRows(oid, domain.OutboxOrderStatus)
		assert.Equal(t, domain.OutboxParked, rows[0].State)
		assert.Equal(t, domain.AttentionRemoteMissing, *e.link(oid).NeedsAttention)
	})
	t.Run("a 404 for an order HubRise lists under the same id parks the row", func(t *testing.T) {
		e, id := setup(t)
		oid := id
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: e.ordersPath(), Status: 404, Times: 1})
		e.setStatus(oid, "CONFIRMED")
		e.drainAll()
		assert.Equal(t, domain.OutboxParked, e.outboxRows(oid, domain.OutboxOrderStatus)[0].State)
	})
	t.Run("a second 404 after the id was backfilled parks the row", func(t *testing.T) {
		e, id := setup(t)
		oid := id
		e.exec(`UPDATE hubrise_orders SET hubrise_order_id = 'gone' WHERE order_id = $1`, oid)
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodPatch, Path: e.ordersPath(), Status: 404, Times: 2})
		e.setStatus(oid, "CONFIRMED")
		e.drainAll()
		assert.Equal(t, domain.OutboxParked, e.outboxRows(oid, domain.OutboxOrderStatus)[0].State)
	})
	t.Run("the lookup itself failing leaves the row for a retry", func(t *testing.T) {
		e, id := setup(t)
		oid := id
		e.exec(`UPDATE hubrise_orders SET hubrise_order_id = 'gone' WHERE order_id = $1`, oid)
		e.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: e.ordersPath(), Status: 503})
		e.setStatus(oid, "CONFIRMED")
		e.drain()
		assert.Equal(t, domain.OutboxPending, e.outboxRows(oid, domain.OutboxOrderStatus)[0].State)
	})
}

func TestAConnectionThatCannotBuildAClientGivesTheRowsBack(t *testing.T) {
	e := newEnv(t, withClients(func(*env) clientProvider { return brokenClients{} }))
	id := e.order(orderSpec{})
	e.ready(id)
	assert.Equal(t, 1, e.drain())
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.Zero(t, row.Attempts, "no attempt was burnt")
	assert.Empty(t, e.srv.Requests())
}

func TestACanceledContextHandsTheBatchBack(t *testing.T) {
	e := newEnv(t)
	id := e.order(orderSpec{})
	e.ready(id)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// A claim with a canceled context fails; the row stays queued.
	_, err := e.push.Worker.DrainOnce(ctx)
	require.Error(t, err)
	assert.Equal(t, domain.OutboxPending, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
}

func TestShutdownMidSendReleasesTheRow(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	e := newEnv(t, withClients(func(e *env) clientProvider {
		hooked := &http.Client{Transport: hookTransport{next: e.srv.HTTPClient().Transport, before: func(*http.Request) { cancel() }}}
		return fixedClients{c: e.srv.NewClient(t, pkghubrise.WithHTTPClient(hooked))}
	}))
	id := e.order(orderSpec{})
	e.ready(id)
	_, err := e.push.Worker.DrainOnce(ctx)
	require.NoError(t, err)
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State, "an interrupted send is released at once")
	assert.Equal(t, 1, row.Attempts, "it may have gone out, so the attempt counts")
	assert.False(t, row.NextAttemptAt.After(e.clock.Now()))
}

func TestARunningWorkerSendsOnWakeUp(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.push.Worker.Run(ctx); close(done) }()

	id := e.order(orderSpec{})
	e.ready(id) // the pusher wakes the worker
	require.Eventually(t, func() bool { return len(e.srv.Orders()) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, id.String(), e.srv.Orders()[0].PrivateRef)
	e.push.Worker.Wake()
	e.push.Worker.Wake() // a second wake-up never blocks
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestADisabledConnectionMakesNoCallAndNoRow(t *testing.T) {
	cases := map[string][]hubtest.SeedOption{
		"disabled":          {hubtest.WithEnabled(false)},
		"orders toggle off": {hubtest.WithFeatures(false, true, true, false, true)},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withSeed(seed...))
			id := e.order(orderSpec{})
			e.ready(id)
			e.setStatus(id, "CONFIRMED")
			e.drainAll()
			require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
			assert.Empty(t, e.srv.Requests(), "zero HTTP calls")
			assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
			assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_outbox`))
		})
	}
}

func TestAConnectionThatWasNeverMadeDoesNothing(t *testing.T) {
	e := newEnv(t, withoutConnection())
	id := e.order(orderSpec{})
	e.ready(id)
	e.setStatus(id, "CONFIRMED")
	e.drainAll()
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	assert.Empty(t, e.srv.Requests())
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`))
}

func TestReEnablingAfterThreeDaysAgesTheStaleRowsOut(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	// One order is waiting to be created, another was sent and has a queued status change.
	waiting := e.order(orderSpec{})
	e.ready(waiting)
	e.setStatus(waiting, "CONFIRMED")
	sent := e.order(orderSpec{})
	e.ready(sent)
	// Let only the second one go out, then pause.
	e.exec(`UPDATE hubrise_outbox SET next_attempt_at = now() + interval '1 day' WHERE order_id = $1`, waiting)
	e.drainAll()
	e.exec(`UPDATE hubrise_outbox SET next_attempt_at = now() - interval '1 day' WHERE order_id = $1`, waiting)
	e.setStatus(sent, "PREPARING")
	_, err := e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: ptr(false)})
	require.NoError(t, err)
	e.clock.Advance(3 * 24 * time.Hour)
	e.drainAll()
	assert.Len(t, e.posts(), 1, "disabled: nothing leaves")

	_, err = e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{Enabled: ptr(true)})
	require.NoError(t, err)
	e.srv.ClearRequests()
	e.drainAll()
	assert.Empty(t, e.srv.Requests(), "stale rows are aged out, not replayed")
	assert.Equal(t, domain.OutboxParked, e.outboxRows(waiting, domain.OutboxOrderCreate)[0].State)
	assert.Equal(t, domain.OutboxSkipped, e.outboxRows(waiting, domain.OutboxOrderStatus)[0].State)
	assert.Equal(t, domain.OutboxSkipped, e.outboxRows(sent, domain.OutboxOrderStatus)[0].State)
}

func TestToggleOffOrderToggleOnPushesNoHistory(t *testing.T) {
	e := newEnv(t)
	e.linkProduct(e.fx.SalmonSushi.ID, "SUSHI-SALMON")
	_, err := e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{PushOrders: ptr(false)})
	require.NoError(t, err)

	old := e.order(orderSpec{createdAgo: 5 * time.Hour})
	recent := e.order(orderSpec{createdAgo: 20 * time.Minute})
	finished := e.order(orderSpec{status: "DELIVERED", createdAgo: 10 * time.Minute})
	e.ready(old)
	e.ready(recent)
	e.ready(finished)
	assert.Zero(t, e.count(`SELECT count(*) FROM hubrise_orders`), "nothing is queued while the toggle is off")

	now := e.clock.Now()
	_, err = e.mod.Connections.UpdateSettings(t.Context(), domain.SettingsPatch{PushOrders: ptr(true), PushOrdersSince: &now})
	require.NoError(t, err)
	require.NoError(t, e.push.Reconciler.RunOnce(t.Context()))
	e.drainAll()

	assert.Len(t, e.srv.Orders(), 1, "only the order in flight at activation (within the 1 h grace) is pushed")
	e.remote(recent)
	_, ok := e.srv.OrderByPrivateRef(old.String())
	assert.False(t, ok, "history is never replayed")
	_, ok = e.srv.OrderByPrivateRef(finished.String())
	assert.False(t, ok, "terminal orders are never pushed")
}
