package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// stubCalls scripts the answers of HubRise to the calls of the publisher.
type stubCalls struct {
	application.PublishAPI
	getRaw *hubrise.RawCatalog
	getErr error
	putErr error
}

func (s stubCalls) GetCatalogRaw(ctx context.Context, id string) (*hubrise.RawCatalog, error) {
	if s.getErr != nil || s.getRaw != nil {
		return s.getRaw, s.getErr
	}
	return s.PublishAPI.GetCatalogRaw(ctx, id)
}

func (s stubCalls) ReplaceCatalog(ctx context.Context, id string, data json.RawMessage) (*hubrise.RawCatalog, error) {
	if s.putErr != nil {
		return nil, s.putErr
	}
	return s.PublishAPI.ReplaceCatalog(ctx, id, data)
}

func (e *penv) publisherOver(stub func(application.PublishAPI) stubCalls) *application.CatalogPublisher {
	p := *e.pub
	p.NewClient = func(c *domain.Connection) (application.PublishAPI, error) {
		real, err := e.pub.NewClient(c)
		return stub(real), err
	}
	return &p
}

// armed gives the env a menu, a confirmed first publish and one edit waiting for the debounce.
func armed(t *testing.T) *penv {
	t.Helper()
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	e.clock.Advance(time.Minute)
	return e
}

func TestACanceledCallIsAnOutageThatBurnsNoAttempt(t *testing.T) {
	canceled := &hubrise.Error{Kind: hubrise.KindCanceled, Op: "test", Message: "context canceled"}
	for name, stub := range map[string]func(application.PublishAPI) stubCalls{
		"read":  func(api application.PublishAPI) stubCalls { return stubCalls{PublishAPI: api, getErr: canceled} },
		"write": func(api application.PublishAPI) stubCalls { return stubCalls{PublishAPI: api, putErr: canceled} },
	} {
		t.Run(name, func(t *testing.T) {
			e := armed(t)
			ran, err := e.publisherOver(stub).Tick(t.Context())
			require.NoError(t, err)
			assert.True(t, ran)
			st := e.publisherState()
			assert.Equal(t, domain.PublishUnavailable, st.ErrorCode())
			assert.Equal(t, "canceled", *st.LastError)
			assert.Nil(t, st.LeaseHolder)
			assert.True(t, st.Dirty(), "the edit is still waiting")
		})
	}
}

func TestACatalogHubRiseCannotBeReadIsAnErrorAndNeverALeakedLease(t *testing.T) {
	e := armed(t)
	garbage := func(api application.PublishAPI) stubCalls {
		return stubCalls{PublishAPI: api, getRaw: &hubrise.RawCatalog{ID: hubrisetest.CatalogID, LocationID: hubrisetest.LocationID, Data: json.RawMessage(`[1]`)}}
	}
	p := e.publisherOver(garbage)
	_, err := p.Tick(t.Context())
	assert.Error(t, err)
	assert.Nil(t, e.publisherState().LeaseHolder)
	_, err = p.Preview(t.Context(), false)
	assert.Error(t, err)
	assert.Equal(t, 1, e.puts())
}

func TestPreviewingAnEmptyMenuIsRefused(t *testing.T) {
	e := newPubEnv(t)
	_, err := e.pub.Preview(t.Context(), false)
	assert.ErrorIs(t, err, domain.ErrEmptyMenu)
}

func TestTheLeaseIsExtendedBeforeEveryStepThatTakesTime(t *testing.T) {
	// The claim, the read of the catalog and the write each renew the lease; a failure of any of them
	// ends the run, releases the lease and writes nothing.
	for _, failOn := range []int{1, 2, 3} {
		e := armed(t)
		p := *e.pub
		p.State = &pubFaultyState{PublishStateRepository: e.pub.State, extendFailOn: failOn}
		_, err := p.Tick(t.Context())
		assert.ErrorIs(t, err, errPubBoom, "extension %d", failOn)
		assert.Equal(t, 1, e.puts(), "nothing was written (extension %d)", failOn)
		assert.Nil(t, e.publisherState().LeaseHolder)
	}
}

func TestARunOnAConnectionThatLostItsCatalogIsReportedAndReleasesTheLease(t *testing.T) {
	e := armed(t)
	e.exec(`UPDATE hubrise_connection SET catalog_id = NULL`)
	e.clock.Advance(time.Minute)
	ran, err := e.pub.Tick(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "the claim skips a connection without a catalog: no error every tick")
	assert.Nil(t, e.publisherState().LeaseHolder)
	assert.Equal(t, 1, e.puts())

	// An interactive publish is told why.
	_, err = e.pub.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, domain.ErrNotConnected)
}

// Run keeps ticking whatever a tick returns, publishes what is due, and stops with its context.
func TestRunKeepsTickingAndPublishesWhatIsDue(t *testing.T) {
	t.Run("a failing tick does not stop it", func(t *testing.T) {
		e := armed(t)
		failing := &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"Claim": true}}
		p := *e.pub
		p.State, p.Leases, p.TickInterval = failing, nil, 5*time.Millisecond
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { p.Run(ctx); close(done) }()
		require.Eventually(t, func() bool { return failing.claims.Load() >= 3 }, 10*time.Second, 5*time.Millisecond)
		cancel()
		<-done
	})
	t.Run("a due edit is published", func(t *testing.T) {
		e := armed(t)
		p := *e.pub
		p.TickInterval = 5 * time.Millisecond
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { p.Run(ctx); close(done) }()
		// The PUT reaches HubRise before the run records it: wait for both.
		require.Eventually(t, func() bool { return e.puts() == 2 && !e.publisherState().Dirty() }, 10*time.Second, 5*time.Millisecond)
		cancel()
		<-done
	})
}

func TestStatusAndProductInfosReportWhatTheyCannotRead(t *testing.T) {
	e := armed(t) // edits wait: the status computes the pending products
	conn := e.conn()
	ctx := t.Context()
	base := *e.pub

	t.Run("the publisher row", func(t *testing.T) {
		p := base
		p.State = &pubFaultyState{PublishStateRepository: base.State, fail: map[string]bool{"Get": true}}
		_, err := p.Status(ctx, conn)
		assert.ErrorIs(t, err, errPubBoom)
	})
	t.Run("the published products count", func(t *testing.T) {
		p := base
		p.Items = pubFaultyItems{PublishedItemRepository: base.Items, count: true}
		_, err := p.Status(ctx, conn)
		assert.ErrorIs(t, err, errPubBoom)
	})
	t.Run("the menu", func(t *testing.T) {
		p := base
		p.Local = pubFaultyLocal{LocalCatalogRepository: base.Local, snapshot: true}
		_, err := p.Status(ctx, conn)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.ProductInfos(ctx, nil)
		assert.ErrorIs(t, err, errPubBoom)
	})
	t.Run("the published items", func(t *testing.T) {
		p := base
		p.Items = pubFaultyItems{PublishedItemRepository: base.Items, fail: true}
		_, err := p.ProductInfos(ctx, nil)
		assert.ErrorIs(t, err, errPubBoom)
	})
	t.Run("without a connection there is nothing to report", func(t *testing.T) {
		st, err := base.Status(ctx, nil)
		require.NoError(t, err)
		assert.False(t, st.Enabled)
	})
}

func TestExternalChangesAreNotedOrReportedWhenTheyCannotBe(t *testing.T) {
	e := armed(t)
	p := *e.pub
	p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"Get": true}}
	assert.ErrorIs(t, p.NoteExternalChange(t.Context(), false, e.clock.Now()), errPubBoom)
}

func TestTheRemoteDriftCheckStaysQuietUnlessItHasSomethingToCompare(t *testing.T) {
	ctx := t.Context()

	t.Run("not publishing, or never published", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		require.NoError(t, e.pub.CheckRemoteDrift(ctx), "the first publish was not confirmed")
		assert.Zero(t, e.gets())
		e.confirmFirst()
		gets := e.gets()
		e.exec(`UPDATE hubrise_connection SET publish_catalog = FALSE`)
		require.NoError(t, e.pub.CheckRemoteDrift(ctx), "the direction is not PUBLISH")
		assert.Equal(t, gets, e.gets())
	})
	t.Run("a publish is running", func(t *testing.T) {
		e := armed(t)
		_, ok, err := e.state.ClaimNow(ctx, "replica-b:x", e.clock.Now())
		require.NoError(t, err)
		require.True(t, ok)
		gets := e.gets()
		require.NoError(t, e.pub.CheckRemoteDrift(ctx))
		assert.Equal(t, gets, e.gets(), "the catalog is changing, nothing to compare")
	})
	t.Run("what cannot be read is reported", func(t *testing.T) {
		e := newPubEnv(t)
		e.product("M1", "Maki", "8.90")
		e.confirmFirst()
		p := *e.pub
		p.Connections = pubFaultyConns{ConnectionRepository: e.conns, get: true}
		assert.ErrorIs(t, p.CheckRemoteDrift(ctx), errPubBoom)
		p = *e.pub
		p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"Get": true}}
		assert.ErrorIs(t, p.CheckRemoteDrift(ctx), errPubBoom)
		p = *e.pub
		p.NewClient = func(*domain.Connection) (application.PublishAPI, error) { return nil, errPubBoom }
		assert.ErrorIs(t, p.CheckRemoteDrift(ctx), errPubBoom)
		p = *e.pub
		e.srv.AddFault(hubrisetestFault(401, "GET", "/v1/catalogs/"))
		assert.ErrorIs(t, p.CheckRemoteDrift(ctx), hubrise.KindUnauthorized)
		assert.NotNil(t, e.conn().AuthErrorAt, "a refused token pauses the connection")
	})
}
