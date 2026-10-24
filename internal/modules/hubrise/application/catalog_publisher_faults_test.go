package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

var errPubBoom = errors.New("boom")

// pubFaultyState fails the named method of the publisher row repository.
type pubFaultyState struct {
	domain.PublishStateRepository
	fail map[string]bool
	// extendFalse makes Extend report a lost lease.
	extendFalse bool
	// extendFailOn makes the nth call of Extend fail (0: none).
	extendFailOn int
	extendCalls  atomic.Int32
	claims       atomic.Int32
}

func (f *pubFaultyState) Get(ctx context.Context) (*domain.PublishState, error) {
	if f.fail["Get"] {
		return nil, errPubBoom
	}
	return f.PublishStateRepository.Get(ctx)
}

func (f *pubFaultyState) Extend(ctx context.Context, h string, now time.Time) (bool, error) {
	if n := int(f.extendCalls.Add(1)); f.extendFailOn > 0 && n == f.extendFailOn {
		return false, errPubBoom
	}
	if f.fail["Extend"] {
		return false, errPubBoom
	}
	if f.extendFalse {
		return false, nil
	}
	return f.PublishStateRepository.Extend(ctx, h, now)
}

func (f *pubFaultyState) Claim(ctx context.Context, h string, now time.Time) (int64, bool, error) {
	f.claims.Add(1)
	if f.fail["Claim"] {
		return 0, false, errPubBoom
	}
	return f.PublishStateRepository.Claim(ctx, h, now)
}

func (f *pubFaultyState) ClaimNow(ctx context.Context, h string, now time.Time) (int64, bool, error) {
	if f.fail["ClaimNow"] {
		return 0, false, errPubBoom
	}
	return f.PublishStateRepository.ClaimNow(ctx, h, now)
}

func (f *pubFaultyState) MarkDirty(ctx context.Context, now time.Time) (int64, error) {
	if f.fail["MarkDirty"] {
		return 0, errPubBoom
	}
	return f.PublishStateRepository.MarkDirty(ctx, now)
}

func (f *pubFaultyState) Complete(ctx context.Context, h string, seq int64, r domain.PublishResult, items []domain.PublishedItem, ids []domain.LinkHubriseID) error {
	if f.fail["Complete"] {
		return errPubBoom
	}
	return f.PublishStateRepository.Complete(ctx, h, seq, r, items, ids)
}

func (f *pubFaultyState) RecordError(ctx context.Context, h string, fl domain.PublishFailure, now time.Time) error {
	if f.fail["RecordError"] {
		return errPubBoom
	}
	return f.PublishStateRepository.RecordError(ctx, h, fl, now)
}

func (f *pubFaultyState) SetFirstPublishConfirmed(ctx context.Context, at *time.Time) error {
	if f.fail["SetFirstPublishConfirmed"] {
		return errPubBoom
	}
	return f.PublishStateRepository.SetFirstPublishConfirmed(ctx, at)
}

type pubFaultyItems struct {
	domain.PublishedItemRepository
	fail, count bool
}

func (f pubFaultyItems) Count(ctx context.Context, t domain.EntityType) (int, error) {
	if f.count {
		return 0, errPubBoom
	}
	return f.PublishedItemRepository.Count(ctx, t)
}

func (f pubFaultyItems) List(ctx context.Context) ([]domain.PublishedItem, error) {
	if f.fail {
		return nil, errPubBoom
	}
	return f.PublishedItemRepository.List(ctx)
}

type pubFaultyLocal struct {
	domain.LocalCatalogRepository
	snapshot, snap, batch bool
}

func (f pubFaultyLocal) SnapshotWithLinks(ctx context.Context) (*domain.LocalSnapshot, []domain.CatalogLink, error) {
	if f.snapshot {
		return nil, nil, errPubBoom
	}
	return f.LocalCatalogRepository.SnapshotWithLinks(ctx)
}

func (f pubFaultyLocal) Snapshot(ctx context.Context) (*domain.LocalSnapshot, error) {
	if f.snap {
		return nil, errPubBoom
	}
	return f.LocalCatalogRepository.Snapshot(ctx)
}

func (f pubFaultyLocal) InBatch(ctx context.Context, fn func(context.Context, domain.CatalogTx) error) error {
	if f.batch {
		return errPubBoom
	}
	return f.LocalCatalogRepository.InBatch(ctx, fn)
}

type pubFaultyConns struct {
	domain.ConnectionRepository
	get, authErr bool
}

func (f pubFaultyConns) Get(ctx context.Context) (*domain.Connection, error) {
	if f.get {
		return nil, errPubBoom
	}
	return f.ConnectionRepository.Get(ctx)
}

func (f pubFaultyConns) RecordAuthError(ctx context.Context, at time.Time, msg string) error {
	if f.authErr {
		return errPubBoom
	}
	return f.ConnectionRepository.RecordAuthError(ctx, at, msg)
}

// stubAPI answers the PUT with what the test wants.
type stubAPI struct {
	application.PublishAPI
	put *hubrise.RawCatalog
}

func (s stubAPI) ReplaceCatalog(context.Context, string, json.RawMessage) (*hubrise.RawCatalog, error) {
	return s.put, nil
}

func TestRepositoryFailuresAreReportedAndReleaseTheLease(t *testing.T) {
	e := newPubEnv(t)
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	e.clock.Advance(time.Minute)
	ctx := t.Context()
	base := *e.pub

	t.Run("snapshot", func(t *testing.T) {
		p := base
		p.Local = pubFaultyLocal{LocalCatalogRepository: base.Local, snapshot: true}
		_, err := p.Tick(ctx)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.PublishNow(ctx, application.PublishRequest{})
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.Preview(ctx, false)
		assert.ErrorIs(t, err, errPubBoom)
		assert.Nil(t, e.publisherState().LeaseHolder)
	})
	t.Run("published items", func(t *testing.T) {
		p := base
		p.Items = pubFaultyItems{PublishedItemRepository: base.Items, fail: true}
		_, err := p.Tick(ctx)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.Preview(ctx, false)
		assert.ErrorIs(t, err, errPubBoom)
		assert.Nil(t, e.publisherState().LeaseHolder)
	})
	t.Run("state read", func(t *testing.T) {
		p := base
		p.State = &pubFaultyState{PublishStateRepository: base.State, fail: map[string]bool{"Get": true}}
		_, err := p.Preview(ctx, false)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.Tick(ctx)
		assert.ErrorIs(t, err, errPubBoom)
		require.Error(t, p.Scan(ctx))
		assert.Nil(t, e.publisherState().LeaseHolder)
	})
	t.Run("lease extension", func(t *testing.T) {
		p := base
		p.State = &pubFaultyState{PublishStateRepository: base.State, fail: map[string]bool{"Extend": true}}
		_, err := p.Tick(ctx)
		assert.ErrorIs(t, err, errPubBoom)
		p.State = &pubFaultyState{PublishStateRepository: base.State, extendFalse: true}
		_, err = p.Tick(ctx)
		assert.ErrorIs(t, err, domain.ErrPublishBusy)
		// The fake lost the lease for the test only: the row still names this run.
		e.exec(`UPDATE hubrise_catalog_publish SET lease_holder = NULL, leased_until = NULL`)
	})
	t.Run("claims", func(t *testing.T) {
		p := base
		p.State = &pubFaultyState{PublishStateRepository: base.State, fail: map[string]bool{"Claim": true, "ClaimNow": true}}
		_, err := p.Tick(ctx)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.PublishNow(ctx, application.PublishRequest{})
		assert.ErrorIs(t, err, errPubBoom)
	})
	t.Run("connection read after the claim", func(t *testing.T) {
		p := base
		p.Connections = pubFaultyConns{ConnectionRepository: base.Connections, get: true}
		_, err := p.Tick(ctx)
		assert.Error(t, err)
		_, err = p.Preview(ctx, false)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.PublishNow(ctx, application.PublishRequest{})
		assert.ErrorIs(t, err, errPubBoom)
		require.NoError(t, p.Scan(ctx), "a scan with no readable connection has nothing to do")
	})
	t.Run("client", func(t *testing.T) {
		p := base
		p.NewClient = func(*domain.Connection) (application.PublishAPI, error) { return nil, errPubBoom }
		_, err := p.Tick(ctx)
		assert.ErrorIs(t, err, errPubBoom)
		_, err = p.Preview(ctx, false)
		assert.ErrorIs(t, err, errPubBoom)
	})
	assert.Nil(t, e.publisherState().LeaseHolder, "no failure kept the lease")

	// And the publisher still works afterwards.
	assert.True(t, e.tick(0))
	assert.Equal(t, 2, e.puts())
}

func TestScanReadFailureIsReported(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	p := *e.pub
	p.Local = pubFaultyLocal{LocalCatalogRepository: e.pub.Local, snap: true}
	assert.ErrorIs(t, p.Scan(t.Context()), errPubBoom)
}

func TestFailingToWriteTheLinksIsAnOutageNotACrash(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	prev := e.firstPlan()
	p := *e.pub
	p.Local = pubFaultyLocal{LocalCatalogRepository: e.pub.Local, batch: true}
	_, err := p.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev})
	assert.ErrorIs(t, err, errPubBoom)
	st := e.publisherState()
	assert.Equal(t, domain.PublishUnavailable, st.ErrorCode())
	assert.Equal(t, 1, st.Attempts)
	assert.Zero(t, e.puts(), "nothing was written to HubRise")
	assert.Nil(t, st.FirstPublishConfirmedAt, "and the confirmation was not consumed")
}

func TestFailingToRecordIsReportedWithItsCause(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	prev := e.firstPlan()
	p := *e.pub
	p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"RecordError": true}}
	e.srv.AddFault(hubrisetestFault500())
	_, err := p.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev})
	assert.ErrorIs(t, err, errPubBoom)
	assert.ErrorIs(t, err, hubrise.KindServer)
}

func TestFailingToConfirmOrCompleteIsReported(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	prev := e.firstPlan()
	p := *e.pub
	p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"SetFirstPublishConfirmed": true}}
	_, err := p.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev})
	assert.ErrorIs(t, err, errPubBoom, "the confirmation is recorded after the write")
	assert.Equal(t, 1, e.puts())

	e2 := newPubEnv(t)
	e2.product("M1", "Maki", "8.90")
	prev2 := e2.firstPlan()
	p2 := *e2.pub
	p2.State = &pubFaultyState{PublishStateRepository: e2.pub.State, fail: map[string]bool{"Complete": true}}
	_, err = p2.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev2})
	assert.ErrorIs(t, err, errPubBoom)
}

func TestMarkDirtyFailureWhileQueueing(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	_, ok, err := e.state.ClaimNow(t.Context(), "replica-b:x", e.clock.Now())
	require.NoError(t, err)
	require.True(t, ok)
	p := *e.pub
	p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"MarkDirty": true}}
	_, err = p.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, errPubBoom)
	p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"Get": true}}
	_, err = p.PublishNow(t.Context(), application.PublishRequest{})
	assert.ErrorIs(t, err, errPubBoom)
}

func TestAuthErrorRecordingFailureDoesNotHideTheCause(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	p := *e.pub
	p.Connections = pubFaultyConns{ConnectionRepository: e.conns, authErr: true}
	e.srv.AddFault(hubrisetestFault(401, "GET", "/v1/catalogs/"))
	_, err := p.Preview(t.Context(), false)
	assert.ErrorIs(t, err, hubrise.KindUnauthorized)
}

func TestTheIdsOfTheWrittenCatalogAreOptional(t *testing.T) {
	for name, put := range map[string]*hubrise.RawCatalog{
		"no answer body": {},
		"nil answer":     nil,
		"garbage":        {Data: json.RawMessage(`[1]`)},
	} {
		t.Run(name, func(t *testing.T) {
			e := newPubEnv(t)
			e.product("M1", "Maki", "8.90")
			prev := e.firstPlan()
			p := *e.pub
			p.NewClient = func(c *domain.Connection) (application.PublishAPI, error) {
				real, err := e.pub.NewClient(c)
				return stubAPI{PublishAPI: real, put: put}, err
			}
			out, err := p.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev})
			require.NoError(t, err)
			assert.True(t, out.Published)
			var hid *string
			require.NoError(t, e.db.GetContext(t.Context(), &hid, `SELECT hubrise_id FROM hubrise_catalog_links WHERE hubrise_key = 'M1'`))
			assert.Nil(t, hid, "the id is refreshed by the next publish")
		})
	}
}

func TestACanceledContextEndsAPublishWithoutBlocking(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	prev := e.firstPlan()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := e.pub.PublishNow(ctx, application.PublishRequest{PlanHash: prev})
	require.Error(t, err)
	assert.Nil(t, e.publisherState().LeaseHolder)
	assert.Zero(t, e.puts())
}

func TestTheVariantAndImagesSettingsReachThePlanner(t *testing.T) {
	e := newPubEnv(t)
	e.product("M1", "Maki", "8.90")
	e.exec(`UPDATE hubrise_connection SET variant_ref = 'delivery'`)
	p, err := e.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	hash := p.Plan.LocalMenuHash
	e.exec(`UPDATE hubrise_connection SET variant_ref = NULL`)
	p, err = e.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	assert.NotEqual(t, hash, p.Plan.LocalMenuHash)
}

func hubrisetestFault500() hubrisetest.Fault {
	return hubrisetest.Fault{Method: "PUT", Status: 500, Times: 1}
}

func hubrisetestFault(status int, method, path string) hubrisetest.Fault {
	return hubrisetest.Fault{Method: method, Path: path, Status: status, Times: 1}
}
