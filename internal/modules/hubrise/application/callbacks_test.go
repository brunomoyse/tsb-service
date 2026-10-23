package application_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/interfaces"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func TestTheCallbackRefusesWhatIsNotSignedByHubRise(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	ev := s.queued()[0]

	t.Run("a wrong signature", func(t *testing.T) {
		for _, d := range s.srv.Dispatch(t.Context(), hubrisetest.Dispatcher{
			Secret: hubrisetest.ClientSecret, Client: s.server.Client(), BadSignature: true, Keep: true,
		}) {
			assert.Equal(t, http.StatusUnauthorized, d.Status)
		}
		assert.Zero(t, s.count(`SELECT count(*) FROM hubrise_events`))
	})
	t.Run("no signature", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.postRaw([]byte(`{"id":"x"}`), ""))
		assert.Zero(t, s.count(`SELECT count(*) FROM hubrise_events`))
	})
	t.Run("a signature that is not hex", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.postRaw([]byte(`{"id":"x"}`), "not-hex"))
	})
	t.Run("a signature of another body", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.postRaw([]byte(`{"id":"x"}`), pkghubrise.Sign(hubrisetest.ClientSecret, []byte(`{"id":"y"}`))))
	})
	t.Run("a signed body that is not an event", func(t *testing.T) {
		for _, body := range []string{`not json`, `{"id":"x"}`} {
			assert.Equal(t, http.StatusBadRequest, s.postRaw([]byte(body), pkghubrise.Sign(hubrisetest.ClientSecret, []byte(body))), body)
		}
	})
	t.Run("a signed event of another location", func(t *testing.T) {
		other := ev
		other.LocationID = "someone-elses"
		assert.Equal(t, http.StatusForbidden, s.post(other))
		noLocation := ev
		noLocation.LocationID = ""
		assert.Equal(t, http.StatusForbidden, s.post(noLocation))
		assert.Zero(t, s.count(`SELECT count(*) FROM hubrise_events`))
	})
	t.Run("a body over the limit", func(t *testing.T) {
		body := bytes.Repeat([]byte("a"), 2<<20)
		assert.Equal(t, http.StatusRequestEntityTooLarge, s.postRaw(body, pkghubrise.Sign(hubrisetest.ClientSecret, body)))
	})
	t.Run("the genuine event is accepted", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, s.post(ev))
		assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`))
		conn, err := s.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.NotNil(t, conn.LastEventAt)
	})
}

// Only failed verifications count: a forger is cut off after 30 a minute, HubRise's own retries never are.
func TestOnlyFailedVerificationsAreRateLimited(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)

	statuses := map[int]int{}
	for range 35 {
		statuses[s.postRaw([]byte(`{"id":"x"}`), "00")]++
	}
	assert.Equal(t, map[int]int{http.StatusUnauthorized: 30, http.StatusTooManyRequests: 5}, statuses)
	require.NotEmpty(t, s.failures.keys)
	assert.NotEmpty(t, s.failures.keys[0], "keyed on the client address")

	// 20 signed events in a burst, with the limiter spent: all of them get a 200.
	for range 20 {
		s.clock.Advance(2 * time.Second)
		s.eposQuiet(id, pkghubrise.StatusAccepted)
		s.eposQuiet(id, pkghubrise.StatusInPreparation)
	}
	require.Len(t, s.queued(), 40)
	deliveries := s.deliver()
	require.Len(t, deliveries, 40)
	for _, d := range deliveries {
		assert.Equal(t, http.StatusOK, d.Status)
	}
	assert.Equal(t, 40, s.count(`SELECT count(*) FROM hubrise_events`))
}

func TestTheCallbackWithoutALimiterStillRefuses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newSyncEnv(t)
	router := gin.New()
	interfaces.NewCallbackHandler(s.sync.Receiver, nil).Register(router)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hubrise/callback", bytes.NewReader([]byte(`{}`)))
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestTheCallbackIsAcknowledgedButNotKeptWhileThePipelineIsPaused(t *testing.T) {
	for name, apply := range map[string]func(s *syncEnv){
		"disabled":   func(s *syncEnv) { s.exec(`UPDATE hubrise_connection SET enabled = FALSE`) },
		"auth error": func(s *syncEnv) { s.exec(`UPDATE hubrise_connection SET auth_error_at = now()`) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newSyncEnv(t)
			id := s.pushed(orderSpec{})
			s.clock.Advance(time.Minute)
			apply(s)
			s.eposQuiet(id, pkghubrise.StatusAccepted)
			for _, d := range s.deliver() {
				assert.Equal(t, http.StatusOK, d.Status, "HubRise must not retry")
			}
			assert.Zero(t, s.count(`SELECT count(*) FROM hubrise_events`))
			assert.Zero(t, s.process())
		})
	}
}

func TestTheCallbackOfAnInstallationThatNeverConnectedAnswers404(t *testing.T) {
	s := newSyncEnv(t, withoutConnection())
	body := []byte(`{"id":"e","resource_type":"order","event_type":"update","location_id":"l"}`)
	assert.Equal(t, http.StatusNotFound, s.postRaw(body, pkghubrise.Sign(hubrisetest.ClientSecret, body)))
	s.exec(`INSERT INTO hubrise_connection (id) VALUES (TRUE)`) // a row without a token
	assert.Equal(t, http.StatusNotFound, s.postRaw(body, pkghubrise.Sign(hubrisetest.ClientSecret, body)))
}

func TestTheCallbackKeepsEventsOfEveryRegisteredResourceAndDropsTheRest(t *testing.T) {
	s := newSyncEnv(t)
	send := func(id, resource, event string) int {
		return s.post(pkghubrise.Event{ID: id, ResourceType: resource, EventType: event, LocationID: hubrisetest.LocationID, CreatedAt: s.clock.Now()})
	}
	assert.Equal(t, http.StatusOK, send("c1", "catalog", "update"))
	assert.Equal(t, http.StatusOK, send("i1", "inventory", "patch"))
	assert.Equal(t, http.StatusOK, send("o1", "order", "create"))
	assert.Equal(t, http.StatusOK, send("x1", "customer", "update"), "not a kind we registered for: acknowledged")
	assert.Equal(t, 3, s.count(`SELECT count(*) FROM hubrise_events`))
	assert.Equal(t, 1, s.process(), "only the order event is the order processor's")
	// The order processor ignored the create and left the others for the catalog and inventory processors.
	assert.Equal(t, 2, s.count(`SELECT count(*) FROM hubrise_events WHERE state = 'pending'`))
}

// brokenEvents fails to store anything.
type brokenEvents struct{ domain.EventRepository }

func (brokenEvents) Insert(context.Context, domain.Event) (bool, error) {
	return false, errors.New("disk full")
}

func TestTheCallbackAnswers500WhenItCannotStoreTheEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newSyncEnv(t)
	receiver := application.NewCallbackReceiver(hubrisetest.ClientSecret, s.mod.Connections, brokenEvents{s.mod.Events}, s.clock)
	router := gin.New()
	interfaces.NewCallbackHandler(receiver, nil).Register(router)
	body := []byte(`{"id":"e","resource_type":"order","event_type":"update","location_id":"` + hubrisetest.LocationID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/hubrise/callback", bytes.NewReader(body))
	req.Header.Set(pkghubrise.SignatureHeader, pkghubrise.Sign(hubrisetest.ClientSecret, body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "HubRise retries a 5xx")
}

type brokenConnections struct{ domain.ConnectionRepository }

func (brokenConnections) Get(context.Context) (*domain.Connection, error) {
	return nil, errors.New("database is down")
}

func TestTheCallbackAnswers500WhenItCannotReadTheConnection(t *testing.T) {
	s := newSyncEnv(t)
	receiver := application.NewCallbackReceiver(hubrisetest.ClientSecret, brokenConnections{}, s.mod.Events, s.clock)
	body := []byte(`{"id":"e","resource_type":"order","event_type":"update","location_id":"l"}`)
	_, err := receiver.Receive(t.Context(), body, pkghubrise.Sign(hubrisetest.ClientSecret, body))
	require.Error(t, err)
	assert.False(t, errors.Is(err, application.ErrNotConnected))
}

// ---- the poller ----

func TestThePollerRecoversTheEventsHubRiseCouldNotDeliver(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.register("") // a passive callback: HubRise keeps the events for the shop to fetch
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	require.Len(t, s.queued(), 1)
	s.srv.ClearRequests()

	require.NoError(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`))
	assert.Empty(t, s.queued(), "acknowledged")
	assert.Len(t, s.srv.RequestsTo(http.MethodDelete, "/v1/callback/events/"), 1)

	s.process()
	assert.Len(t, s.gets(), 1, "listed events carry no state: the order is read")
	assert.Equal(t, "CONFIRMED", s.localStatus(id))
	assert.Equal(t, map[string]int{"done": 1}, s.eventStates())

	// Nothing left: another poll changes nothing.
	require.NoError(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`))
}

func TestThePollerDoesNotStoreAnEventTwice(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	// The callback delivered it but its answer was lost: it is still listed.
	s.srv.Dispatch(t.Context(), hubrisetest.Dispatcher{Secret: hubrisetest.ClientSecret, Client: s.server.Client(), Keep: true})
	require.Len(t, s.queued(), 1)
	require.NoError(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`))
	assert.Empty(t, s.queued())
}

func TestThePollerReadsEveryPage(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.register("")
	for range 5 {
		s.clock.Advance(time.Second)
		s.eposQuiet(id, pkghubrise.StatusAccepted)
	}
	cfg := application.CallbackPollerConfig{PageSize: 2, MaxPages: 2}
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: s.mod.Events, Leases: s.mod.Leases, Clients: s.push.Clients,
		Clock: s.clock, Config: cfg,
	})
	require.NoError(t, poller.PollOnce(t.Context()))
	assert.Equal(t, 4, s.count(`SELECT count(*) FROM hubrise_events`), "two pages of two; the rest waits for the next poll")
	require.NoError(t, poller.PollOnce(t.Context()))
	require.NoError(t, poller.PollOnce(t.Context()))
	assert.Equal(t, 5, s.count(`SELECT count(*) FROM hubrise_events`))
}

func TestThePollerStaysAsideWhilePausedAndFollowsTheLease(t *testing.T) {
	s := newSyncEnv(t)
	s.register("")
	s.srv.ClearRequests()

	ran, err := s.sync.Poller.Tick(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)

	other := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: s.mod.Events, Leases: s.mod.Leases, Clients: s.push.Clients,
		Clock: s.clock, Config: application.CallbackPollerConfig{Holder: "another replica"},
	})
	ran, err = other.Tick(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "the lease is taken")
	s.clock.Advance(11 * time.Minute)
	ran, err = other.Tick(t.Context())
	require.NoError(t, err)
	assert.True(t, ran)

	s.exec(`UPDATE hubrise_connection SET enabled = FALSE`)
	s.srv.ClearRequests()
	require.NoError(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Empty(t, s.srv.Requests(), "zero calls while paused")
}

func TestThePollerWithoutAConnectionDoesNothing(t *testing.T) {
	s := newSyncEnv(t, withoutConnection())
	s.srv.ClearRequests()
	require.NoError(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Empty(t, s.srv.Requests())
}

func TestThePollerPausesThePipelineWhenHubRiseRefusesTheConnection(t *testing.T) {
	s := newSyncEnv(t)
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/callback/events", Status: http.StatusUnauthorized})
	err := s.sync.Poller.PollOnce(t.Context())
	require.Error(t, err)
	conn, gerr := s.mod.Connections.Get(t.Context())
	require.NoError(t, gerr)
	assert.NotNil(t, conn.AuthErrorAt)
}

func TestThePollerOnlyLogsAServerError(t *testing.T) {
	s := newSyncEnv(t)
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodGet, Path: "/v1/callback/events", Times: 1, Status: http.StatusInternalServerError})
	require.Error(t, s.sync.Poller.PollOnce(t.Context()))
	conn, err := s.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.Nil(t, conn.AuthErrorAt)
}

func TestThePollerKeepsWhatItCouldNotAcknowledge(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.register("")
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodDelete, Path: "/v1/callback/events/", Times: 1, Status: http.StatusInternalServerError})
	require.Error(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Len(t, s.queued(), 1, "still listed: the next poll acknowledges it")
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`))
	require.NoError(t, s.sync.Poller.PollOnce(t.Context()))
	assert.Empty(t, s.queued())
	assert.Equal(t, 1, s.count(`SELECT count(*) FROM hubrise_events`), "not stored twice")
}

func TestThePollerPausesOnARefusedAcknowledgement(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	s.register("")
	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.srv.AddFault(hubrisetest.Fault{Method: http.MethodDelete, Path: "/v1/callback/events/", Status: http.StatusForbidden})
	require.Error(t, s.sync.Poller.PollOnce(t.Context()))
	conn, err := s.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, conn.AuthErrorAt)
}

// brokenClients is used by the processor test; here a poller with no client.
func TestThePollerReportsAClientThatCannotBeBuilt(t *testing.T) {
	s := newSyncEnv(t)
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: s.mod.Events, Leases: s.mod.Leases, Clients: brokenClients{}, Clock: s.clock,
	})
	require.Error(t, poller.PollOnce(t.Context()))
}

type erroringConnections struct{ domain.ConnectionRepository }

func (erroringConnections) Get(context.Context) (*domain.Connection, error) {
	return nil, errors.New("database is down")
}

func TestThePollerAndTheProcessorReportADatabaseFailure(t *testing.T) {
	s := newSyncEnv(t)
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: erroringConnections{}, Events: s.mod.Events, Leases: s.mod.Leases, Clients: s.push.Clients, Clock: s.clock,
	})
	require.Error(t, poller.PollOnce(t.Context()))
	proc := application.NewEventProcessor(application.EventProcessorDeps{Connections: erroringConnections{}, Clock: s.clock})
	_, err := proc.ProcessOnce(t.Context())
	require.Error(t, err)
	status := application.NewStatusSync(application.StatusSyncDeps{Connections: erroringConnections{}, Clock: s.clock})
	require.Error(t, status.ApplyRemote(t.Context(), uuid.New(), &pkghubrise.Order{}))
}

func TestTheRunLoopsStopWithTheirContext(t *testing.T) {
	s := newSyncEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{}, 2)
	go func() { s.sync.Processor.Run(ctx); done <- struct{}{} }()
	go func() { s.sync.Poller.Run(ctx); done <- struct{}{} }()
	s.sync.Processor.Wake()
	s.sync.Processor.Wake() // the second one is absorbed
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a loop did not stop")
		}
	}
}

func TestTheProcessorRunLoopHandlesAWokenEvent(t *testing.T) {
	s := newSyncEnv(t)
	id := s.pushed(orderSpec{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.sync.Processor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	s.clock.Advance(time.Minute)
	s.eposQuiet(id, pkghubrise.StatusAccepted)
	s.deliver() // the receiver wakes the processor
	require.Eventually(t, func() bool { return s.localStatus(id) == "CONFIRMED" }, 5*time.Second, 20*time.Millisecond)
}

var (
	_ = hubtest.DefaultToken
	_ = hubrise.Config{}
)

// The poller and the reconciler are housekeeping: once the hourly budget is mostly spent they stop,
// so they never eat what the order pushes need. The poll is made with the background class.
func TestThePollerIsABackgroundCaller(t *testing.T) {
	s := newSyncEnv(t)
	limiter := pkghubrise.NewLimiter(pkghubrise.Limits{PerHour: 10, BackgroundShare: 0.6})
	client := s.srv.NewClient(t, pkghubrise.WithLimiter(limiter))
	for range 6 { // critical calls: 6 of 10 spent
		_, err := client.GetLocation(t.Context())
		require.NoError(t, err)
	}
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: s.mod.Connections, Events: s.mod.Events, Leases: s.mod.Leases,
		Clients: fixedClients{c: client}, Clock: s.clock,
	})
	s.srv.ClearRequests()
	err := poller.PollOnce(t.Context())
	require.Error(t, err)
	assert.Equal(t, pkghubrise.KindRateLimited, pkghubrise.KindOf(err))
	assert.Empty(t, s.srv.Requests(), "refused before it reached HubRise")
}
