package infrastructure_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
)

var t0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// publishEnv is a database with a connection that publishing may use.
type publishEnv struct {
	*env
	conn  domain.ConnectionRepository
	state domain.PublishStateRepository
	items domain.PublishedItemRepository
}

func newPublishEnv(t *testing.T) *publishEnv {
	t.Helper()
	e := &publishEnv{env: newEnv(t)}
	e.conn = infrastructure.NewConnectionRepository(e.db)
	e.state = infrastructure.NewPublishStateRepository(e.db)
	e.items = infrastructure.NewPublishedItemRepository(e.db)
	return e
}

// connect connects, enables and switches to PUBLISH with the first publish confirmed.
func (e *publishEnv) connect(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	_, err := e.conn.Connect(ctx, domain.ConnectParams{
		AccessTokenEnc: []byte("sealed"), LocationID: "loc", CatalogID: "cat", Scope: hubDefaultScope,
	})
	require.NoError(t, err)
	yes := true
	_, err = e.conn.UpdateSettings(ctx, domain.SettingsPatch{Enabled: &yes})
	require.NoError(t, err)
	_, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
	require.NoError(t, err)
	at := t0
	require.NoError(t, e.state.SetFirstPublishConfirmed(ctx, &at))
}

const hubDefaultScope = "location[orders.write,customer_list.write,catalog.read,catalog.write,inventory.read,inventory.write]"

func TestPublishStateStartsAsAnIdleSingleton(t *testing.T) {
	e := newPublishEnv(t)
	s, err := e.state.Get(t.Context())
	require.NoError(t, err)
	assert.Zero(t, s.ChangeSeq)
	assert.False(t, s.Dirty())
	assert.Nil(t, s.FirstPublishConfirmedAt)
	assert.Nil(t, s.LeaseHolder)
	assert.Empty(t, s.ErrorCode())
	assert.False(t, s.Leased(t0))

	var n int
	require.NoError(t, e.db.GetContext(t.Context(), &n, `SELECT count(*) FROM hubrise_catalog_publish`))
	assert.Equal(t, 1, n)
	_, err = e.db.ExecContext(t.Context(), `INSERT INTO hubrise_catalog_publish (id) VALUES (FALSE)`)
	assert.Error(t, err, "the table holds one row at most")
}

// The SQL debounce is the arithmetic of domain.NextDue: 10 s after the last change, at most 120 s after
// the first, never sooner than 30 s after the previous run started.
func TestMarkDirtyDebounceArithmetic(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		// changes are offsets of the edits from t0; lastStart is the offset of the previous run (nil: none).
		changes   []time.Duration
		lastStart *time.Duration
		want      time.Duration // next_attempt_at offset
	}{
		{"one edit waits ten seconds", []time.Duration{0}, nil, 10 * time.Second},
		{"each edit pushes it back", []time.Duration{0, 4 * time.Second, 9 * time.Second}, nil, 19 * time.Second},
		{"continuous edits cap at two minutes from the first", []time.Duration{0, 60 * time.Second, 115 * time.Second, 119 * time.Second}, nil, 120 * time.Second},
		{"a run that started 5 s ago holds the next one back to 30 s after its start", []time.Duration{10 * time.Second}, after(5 * time.Second), 35 * time.Second},
		{"a run long ago does not matter", []time.Duration{100 * time.Second}, after(0), 110 * time.Second},
		{"the minimum interval wins over the cap", []time.Duration{200 * time.Second, 300 * time.Second}, after(290 * time.Second), 320 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPublishEnv(t)
			if tc.lastStart != nil {
				_, err := e.db.ExecContext(ctx, `UPDATE hubrise_catalog_publish SET last_started_at = $1`, t0.Add(*tc.lastStart))
				require.NoError(t, err)
			}
			var seq int64
			var err error
			for _, c := range tc.changes {
				seq, err = e.state.MarkDirty(ctx, t0.Add(c))
				require.NoError(t, err)
			}
			assert.EqualValues(t, len(tc.changes), seq)
			s, err := e.state.Get(ctx)
			require.NoError(t, err)
			require.NotNil(t, s.NextAttemptAt)
			assert.WithinDuration(t, t0.Add(tc.want), *s.NextAttemptAt, time.Millisecond)
			assert.WithinDuration(t, t0.Add(tc.changes[0]), *s.DirtySince, time.Millisecond, "dirty_since is the first edit of the burst")
			assert.WithinDuration(t, t0.Add(tc.changes[len(tc.changes)-1]), *s.LastChangeAt, time.Millisecond)

			// The Go copy of the arithmetic gives the same answer.
			var last time.Time
			if tc.lastStart != nil {
				last = t0.Add(*tc.lastStart)
			}
			assert.WithinDuration(t, domain.NextDue(t0.Add(tc.changes[len(tc.changes)-1]), t0.Add(tc.changes[0]), last), *s.NextAttemptAt, time.Millisecond)
		})
	}
}

func after(d time.Duration) *time.Duration { return &d }

func TestMarkDirtyUnblocksAndWorksWithoutTheMigrationRow(t *testing.T) {
	e := newPublishEnv(t)
	ctx := t.Context()
	_, err := e.db.ExecContext(ctx, `UPDATE hubrise_catalog_publish SET blocked_at = $1, last_error_code = 'REJECTED'`, t0)
	require.NoError(t, err)
	_, err = e.state.MarkDirty(ctx, t0)
	require.NoError(t, err)
	s, _ := e.state.Get(ctx)
	assert.Nil(t, s.BlockedAt, "the next edit re-arms a refused publish")
	assert.Equal(t, domain.PublishRejected, s.ErrorCode(), "the message stays until a publish succeeds")

	_, err = e.db.ExecContext(ctx, `DELETE FROM hubrise_catalog_publish`)
	require.NoError(t, err)
	seq, err := e.state.MarkDirty(ctx, t0)
	require.NoError(t, err, "the row is recreated")
	assert.EqualValues(t, 1, seq)
}

func TestClaimPredicate(t *testing.T) {
	ctx := t.Context()
	due := t0.Add(time.Minute)

	// ready returns an environment where a claim at `due` succeeds.
	ready := func(t *testing.T) *publishEnv {
		e := newPublishEnv(t)
		e.connect(t)
		_, err := e.state.MarkDirty(ctx, t0)
		require.NoError(t, err)
		return e
	}
	t.Run("the base case claims and returns the covered seq", func(t *testing.T) {
		e := ready(t)
		seq, ok, err := e.state.Claim(ctx, "replica-a", due)
		require.NoError(t, err)
		require.True(t, ok)
		assert.EqualValues(t, 2, seq, "the direction switch is change 1, the edit change 2")
		s, _ := e.state.Get(ctx)
		assert.Equal(t, "replica-a", *s.LeaseHolder)
		assert.WithinDuration(t, due.Add(domain.PublishLeaseTTL), *s.LeasedUntil, time.Millisecond)
		assert.WithinDuration(t, due, *s.LastStartedAt, time.Millisecond)
		assert.True(t, s.Leased(due))
	})

	for name, mutate := range map[string]string{
		"not enabled":                 `UPDATE hubrise_connection SET enabled = FALSE`,
		"not publishing":              `UPDATE hubrise_connection SET publish_catalog = FALSE`,
		"auth error":                  `UPDATE hubrise_connection SET auth_error_at = now()`,
		"relink required":             `UPDATE hubrise_connection SET relink_required_at = now()`,
		"disconnected":                `UPDATE hubrise_connection SET access_token_enc = NULL`,
		"first publish not confirmed": `UPDATE hubrise_catalog_publish SET first_publish_confirmed_at = NULL`,
		"blocked":                     `UPDATE hubrise_catalog_publish SET blocked_at = now()`,
		"not dirty":                   `UPDATE hubrise_catalog_publish SET dirty_since = NULL`,
		"not due yet":                 `UPDATE hubrise_catalog_publish SET next_attempt_at = '2026-10-24T13:00:00Z'`,
		"lease still live":            `UPDATE hubrise_catalog_publish SET lease_holder = 'other', leased_until = '2026-10-24T13:00:00Z'`,
		"no connection row at all":    `DELETE FROM hubrise_connection`,
	} {
		t.Run("refused when "+name, func(t *testing.T) {
			e := ready(t)
			_, err := e.db.ExecContext(ctx, mutate)
			require.NoError(t, err)
			_, ok, err := e.state.Claim(ctx, "replica-a", due)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}

	t.Run("a null next attempt is due at once", func(t *testing.T) {
		e := ready(t)
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_catalog_publish SET next_attempt_at = NULL`)
		require.NoError(t, err)
		_, ok, err := e.state.Claim(ctx, "replica-a", t0)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("only one of two replicas wins", func(t *testing.T) {
		e := ready(t)
		_, okA, err := e.state.Claim(ctx, "a", due)
		require.NoError(t, err)
		_, okB, err := e.state.Claim(ctx, "b", due)
		require.NoError(t, err)
		assert.True(t, okA)
		assert.False(t, okB)
	})

	t.Run("an expired lease is taken over, and the old holder is cut off", func(t *testing.T) {
		e := ready(t)
		_, ok, err := e.state.Claim(ctx, "a", due)
		require.NoError(t, err)
		require.True(t, ok)
		later := due.Add(domain.PublishLeaseTTL + time.Second)
		seq, ok, err := e.state.Claim(ctx, "b", later)
		require.NoError(t, err)
		require.True(t, ok, "the killed replica's lease expired")
		assert.EqualValues(t, 2, seq)

		alive, err := e.state.Extend(ctx, "a", later)
		require.NoError(t, err)
		assert.False(t, alive, "the old holder lost it")
		alive, err = e.state.Extend(ctx, "b", later.Add(time.Second))
		require.NoError(t, err)
		assert.True(t, alive)
		s, _ := e.state.Get(ctx)
		assert.WithinDuration(t, later.Add(time.Second).Add(domain.PublishLeaseTTL), *s.LeasedUntil, time.Millisecond)

		require.ErrorIs(t, e.state.Complete(ctx, "a", 2, domain.PublishResult{At: later}, nil, nil), domain.ErrPublishBusy, "a taken-over run writes nothing")
	})

	t.Run("claim now ignores the schedule but not a live lease", func(t *testing.T) {
		e := newPublishEnv(t)
		e.connect(t)
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_catalog_publish SET first_publish_confirmed_at = NULL`)
		require.NoError(t, err)
		_, ok, err := e.state.Claim(ctx, "a", due)
		require.NoError(t, err)
		require.False(t, ok, "the background publisher waits for the confirmation")
		_, ok, err = e.state.ClaimNow(ctx, "mutation", due)
		require.NoError(t, err)
		require.True(t, ok, "the mutation carries the confirmation itself")
		_, ok, err = e.state.ClaimNow(ctx, "other", due)
		require.NoError(t, err)
		assert.False(t, ok)
		require.NoError(t, e.state.Release(ctx, "mutation"))
		_, ok, err = e.state.ClaimNow(ctx, "other", due)
		require.NoError(t, err)
		assert.True(t, ok)
	})
}

func TestCompleteKeepsTheRowDirtyWhenAnEditArrivedDuringTheRun(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	_, err := e.state.MarkDirty(ctx, t0)
	require.NoError(t, err)
	seq, ok, err := e.state.Claim(ctx, "a", t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)

	// An edit lands while the PUT is in flight.
	_, err = e.state.MarkDirty(ctx, t0.Add(70*time.Second))
	require.NoError(t, err)

	done := t0.Add(80 * time.Second)
	ids := []domain.LinkHubriseID{}
	err = e.state.Complete(ctx, "a", seq, domain.PublishResult{At: done, LocalHash: "lh", RemoteHash: "rh"}, []domain.PublishedItem{
		{EntityType: domain.EntityCategory, HubriseKey: "c", HubriseRef: "c", ContentHash: "h", PublishedAt: done},
	}, ids)
	require.NoError(t, err)

	s, _ := e.state.Get(ctx)
	assert.True(t, s.Dirty(), "the newer edit is still to publish")
	assert.WithinDuration(t, done.Add(domain.PublishMinInterval), *s.NextAttemptAt, time.Millisecond)
	assert.Nil(t, s.LeaseHolder)
	assert.Equal(t, seq, s.LastPublishedSeq)
	assert.Equal(t, "lh", *s.LastPublishedLocalHash)
	assert.Equal(t, "rh", *s.LastPublishedRemoteHash)
	assert.WithinDuration(t, done, *s.LastPublishedAt, time.Millisecond)

	// The follow-up run covers it and clears the flag.
	seq2, ok, err := e.state.Claim(ctx, "b", done.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.state.Complete(ctx, "b", seq2, domain.PublishResult{At: done.Add(time.Minute)}, nil, nil))
	s, _ = e.state.Get(ctx)
	assert.False(t, s.Dirty())
	assert.Nil(t, s.NextAttemptAt)
}

func TestCompleteClearsErrorsAndTheExternalChangeWarning(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	_, err := e.state.MarkDirty(ctx, t0)
	require.NoError(t, err)
	require.NoError(t, e.state.SetExternalChange(ctx, &t0))
	seq, ok, err := e.state.Claim(ctx, "a", t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.state.RecordError(ctx, "a", domain.PublishFailure{Code: domain.PublishUnavailable, Message: "boom", CountAttempt: true}, t0))
	_, ok, err = e.state.Claim(ctx, "a", t0.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.state.Complete(ctx, "a", seq, domain.PublishResult{At: t0.Add(3 * time.Minute)}, nil, nil))
	s, _ := e.state.Get(ctx)
	assert.Nil(t, s.ExternalChangeAt)
	assert.Nil(t, s.LastError)
	assert.Nil(t, s.LastErrorCode)
	assert.Zero(t, s.Attempts)
	assert.False(t, s.Dirty())
}

func TestRecordErrorAndBlock(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	_, err := e.state.MarkDirty(ctx, t0)
	require.NoError(t, err)
	_, ok, err := e.state.Claim(ctx, "a", t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)

	retry := t0.Add(5 * time.Minute)
	require.NoError(t, e.state.RecordError(ctx, "a", domain.PublishFailure{Code: domain.PublishUnavailable, Message: "HubRise is down", RetryAt: &retry, CountAttempt: true}, t0.Add(time.Minute)))
	s, _ := e.state.Get(ctx)
	assert.Equal(t, 1, s.Attempts)
	assert.Equal(t, domain.PublishUnavailable, s.ErrorCode())
	assert.Equal(t, "HubRise is down", *s.LastError)
	assert.WithinDuration(t, retry, *s.NextAttemptAt, time.Millisecond)
	assert.True(t, s.Dirty(), "nothing is ever dropped")
	assert.Nil(t, s.LeaseHolder, "the lease is released")
	assert.Nil(t, s.BlockedAt)

	// A rate limit wait does not burn an attempt.
	_, ok, err = e.state.Claim(ctx, "a", retry.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	later := retry.Add(time.Minute)
	require.NoError(t, e.state.RecordError(ctx, "a", domain.PublishFailure{Code: domain.PublishUnavailable, Message: "429", RetryAt: &later}, retry))
	s, _ = e.state.Get(ctx)
	assert.Equal(t, 1, s.Attempts)

	// An error from the callback path has no lease: it must not release someone else's.
	_, ok, err = e.state.Claim(ctx, "b", later.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.state.RecordError(ctx, "", domain.PublishFailure{Code: domain.PublishCatalogNotFound, Message: "gone"}, later))
	s, _ = e.state.Get(ctx)
	assert.Equal(t, "b", *s.LeaseHolder, "b still holds the lease")
	assert.Equal(t, domain.PublishCatalogNotFound, s.ErrorCode())

	// A run that lost its lease cannot record anything.
	require.NoError(t, e.state.RecordError(ctx, "a", domain.PublishFailure{Code: domain.PublishAuth, Message: "stale"}, later))
	s, _ = e.state.Get(ctx)
	assert.Equal(t, domain.PublishCatalogNotFound, s.ErrorCode())

	require.NoError(t, e.state.Block(ctx, "b", domain.PublishRejected, "products[0].skus: must have one", later))
	s, _ = e.state.Get(ctx)
	assert.NotNil(t, s.BlockedAt)
	assert.Nil(t, s.LeaseHolder)
	_, ok, err = e.state.Claim(ctx, "c", later.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "a blocked row waits for an edit")
	require.NoError(t, e.state.Block(ctx, "", domain.PublishWriteScopeMissing, "no scope", later))
	s, _ = e.state.Get(ctx)
	assert.Equal(t, domain.PublishWriteScopeMissing, s.ErrorCode())

	long := strings.Repeat("é", 400) // 800 bytes
	require.NoError(t, e.state.Block(ctx, "", domain.PublishRejected, long, later))
	s, _ = e.state.Get(ctx)
	assert.LessOrEqual(t, len(*s.LastError), 500)
	assert.True(t, utf8.ValidString(*s.LastError), "cut on a rune boundary")

	euros := strings.Repeat("€", 200) // 600 bytes, the 500th byte is inside a rune
	require.NoError(t, e.state.Block(ctx, "", domain.PublishRejected, euros, later))
	s, _ = e.state.Get(ctx)
	assert.True(t, utf8.ValidString(*s.LastError))
	assert.Equal(t, strings.Repeat("€", 166), *s.LastError, "166 whole runes fit in 500 bytes")

	require.NoError(t, e.state.ClearError(ctx))
	s, _ = e.state.Get(ctx)
	assert.Nil(t, s.BlockedAt)
	assert.Nil(t, s.LastError)
	assert.Zero(t, s.Attempts)
}

func TestExternalChangeKeepsTheEarliestStamp(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	require.NoError(t, e.state.SetExternalChange(ctx, &t0))
	later := t0.Add(time.Hour)
	require.NoError(t, e.state.SetExternalChange(ctx, &later))
	s, _ := e.state.Get(ctx)
	assert.WithinDuration(t, t0, *s.ExternalChangeAt, time.Millisecond, "a burst collapses into its first event")
	require.NoError(t, e.state.SetExternalChange(ctx, nil))
	s, _ = e.state.Get(ctx)
	assert.Nil(t, s.ExternalChangeAt)
}

func TestResetReturnsToAwaitingFirstPublish(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	require.NoError(t, e.state.Block(ctx, "", domain.PublishRejected, "x", t0))
	require.NoError(t, e.state.SetExternalChange(ctx, &t0))
	require.NoError(t, e.state.Reset(ctx, t0.Add(time.Hour)))
	s, _ := e.state.Get(ctx)
	assert.Nil(t, s.FirstPublishConfirmedAt)
	assert.True(t, s.Dirty())
	assert.Nil(t, s.BlockedAt)
	assert.Nil(t, s.ExternalChangeAt)
	assert.Nil(t, s.LastError)
}

func TestPublishedItems(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	at := t0
	item := func(typ domain.EntityType, key, hash string) domain.PublishedItem {
		return domain.PublishedItem{EntityType: typ, HubriseKey: key, HubriseRef: key, ContentHash: hash, PublishedAt: at}
	}
	require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{item(domain.EntityProduct, "A", "1"), item(domain.EntityProduct, "B", "1"), item(domain.EntityCategory, "C", "1")}))
	got, err := e.items.List(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 3)
	n, err := e.items.Count(ctx, domain.EntityProduct)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	by, err := e.items.ByKeys(ctx, domain.EntityProduct, []string{"A", "Z", "C"})
	require.NoError(t, err)
	require.Len(t, by, 1, "keys are per entity type")
	assert.Equal(t, "1", by["A"].ContentHash)
	none, err := e.items.ByKeys(ctx, domain.EntityProduct, nil)
	require.NoError(t, err)
	assert.Empty(t, none)

	// Replace is total: what is not in the new set is gone, so the removal set can be derived.
	require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{item(domain.EntityProduct, "A", "2")}))
	got, _ = e.items.List(ctx)
	require.Len(t, got, 1)
	assert.Equal(t, "2", got[0].ContentHash)
	require.NoError(t, e.items.Replace(ctx, nil))
	got, _ = e.items.List(ctx)
	assert.Empty(t, got)

	require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{item(domain.EntityChoice, "x|y|z", "1"), item(domain.EntityChoiceGroup, "x|y", "1")}))
	require.NoError(t, e.items.Clear(ctx))
	got, _ = e.items.List(ctx)
	assert.Empty(t, got)

	_, err = e.db.ExecContext(ctx, `INSERT INTO hubrise_published_items (entity_type, hubrise_key, hubrise_ref, content_hash) VALUES ('deal', 'x', 'x', 'h')`)
	assert.Error(t, err, "only the four entity types are accepted")
}

func TestCompleteRefreshesTheLinkIDsAndReplacesTheItemsAtomically(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	links := infrastructure.NewCatalogLinkRepository(e.db)
	l := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: "SKU-1", HubriseRef: "SKU-1", Origin: domain.OriginPublished}
	l.SetLocalID(e.fx.SalmonSushi.ID)
	old := "old-id"
	l.HubriseID = &old
	require.NoError(t, links.Upsert(ctx, l))

	_, err := e.state.MarkDirty(ctx, t0)
	require.NoError(t, err)
	seq, ok, err := e.state.Claim(ctx, "a", t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "OLD", HubriseRef: "OLD", ContentHash: "h", PublishedAt: t0}}))

	// A lost lease rolls back the items and the ids with the state.
	err = e.state.Complete(ctx, "stranger", seq, domain.PublishResult{At: t0}, []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "NEW", HubriseRef: "NEW", ContentHash: "h", PublishedAt: t0}},
		[]domain.LinkHubriseID{{EntityType: domain.EntityProduct, Key: "SKU-1", HubriseID: "fresh"}})
	require.ErrorIs(t, err, domain.ErrPublishBusy)
	got, _ := e.items.List(ctx)
	require.Len(t, got, 1)
	assert.Equal(t, "OLD", got[0].HubriseKey)
	cur, _ := links.FindByKey(ctx, domain.EntityProduct, "SKU-1")
	assert.Equal(t, "old-id", *cur.HubriseID)

	err = e.state.Complete(ctx, "a", seq, domain.PublishResult{At: t0}, []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "NEW", HubriseRef: "NEW", ContentHash: "h", PublishedAt: t0}},
		[]domain.LinkHubriseID{{EntityType: domain.EntityProduct, Key: "SKU-1", HubriseID: "fresh"}, {EntityType: domain.EntityProduct, Key: "no-such-link", HubriseID: "x"}})
	require.NoError(t, err)
	got, _ = e.items.List(ctx)
	require.Len(t, got, 1)
	assert.Equal(t, "NEW", got[0].HubriseKey)
	cur, _ = links.FindByKey(ctx, domain.EntityProduct, "SKU-1")
	assert.Equal(t, "fresh", *cur.HubriseID)
}

func TestLinkRekeyAndPublishedOrigin(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	links := infrastructure.NewCatalogLinkRepository(e.db)
	groupID, _ := e.newChoice(t)
	l := &domain.CatalogLink{EntityType: domain.EntityChoiceGroup, HubriseKey: "SKU|LIST", HubriseRef: "LIST", Origin: domain.OriginPublished}
	l.SetLocalID(groupID)
	require.NoError(t, links.Upsert(ctx, l), "the widened CHECK accepts the new origin")
	assert.Equal(t, domain.OriginPublished, l.Origin)

	require.NoError(t, links.Rekey(ctx, l.ID, "SKU|LIST~SKU", "LIST~SKU"))
	got, err := links.FindByKey(ctx, domain.EntityChoiceGroup, "SKU|LIST~SKU")
	require.NoError(t, err)
	assert.Equal(t, "LIST~SKU", got.HubriseRef)
	_, err = links.FindByKey(ctx, domain.EntityChoiceGroup, "SKU|LIST")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, links.Rekey(ctx, 999999, "k", "r"), domain.ErrNotFound)
	assert.ErrorIs(t, links.Rekey(ctx, l.ID, "", "r"), domain.ErrInvalidLink)

	require.NoError(t, links.SetHubriseID(ctx, domain.EntityChoiceGroup, "SKU|LIST~SKU", "h1"))
	require.NoError(t, links.SetHubriseID(ctx, domain.EntityChoiceGroup, "SKU|LIST~SKU", "h1"), "same id: nothing to do")
	require.NoError(t, links.SetHubriseID(ctx, domain.EntityChoiceGroup, "missing", "h2"))
	got, _ = links.FindByKey(ctx, domain.EntityChoiceGroup, "SKU|LIST~SKU")
	assert.Equal(t, "h1", *got.HubriseID)

	_, err = e.db.ExecContext(ctx, `UPDATE hubrise_catalog_links SET origin = 'garbage'`)
	assert.Error(t, err, "other origins are still refused")
	bad := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: "X", HubriseRef: "X", Origin: "garbage"}
	bad.SetLocalID(e.fx.SalmonSushi.ID)
	assert.ErrorIs(t, links.Upsert(ctx, bad), domain.ErrInvalidLink)
}

func TestChangeCatalogDirection(t *testing.T) {
	ctx := t.Context()
	yes, no := true, false

	t.Run("without a connection", func(t *testing.T) {
		e := newPublishEnv(t)
		_, _, err := e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		_, _, err = e.conn.ChangeCatalogDirection(ctx, "SIDEWAYS", t0, nil)
		var is *domain.InvalidSettingError
		assert.ErrorAs(t, err, &is)
	})

	t.Run("to PUBLISH switches the import off and starts awaiting the first publish", func(t *testing.T) {
		e := newPublishEnv(t)
		_, err := e.conn.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("x"), LocationID: "l", CatalogID: "c", Scope: hubDefaultScope})
		require.NoError(t, err)
		_, err = e.conn.UpdateSettings(ctx, domain.SettingsPatch{SyncCatalog: &yes, CatalogAutoImport: &yes})
		require.NoError(t, err)
		require.NoError(t, e.conn.SetCatalogUpdateAvailable(ctx, &t0))
		at := t0
		require.NoError(t, e.state.SetFirstPublishConfirmed(ctx, &at))

		c, before, err := e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
		require.NoError(t, err)
		assert.Equal(t, domain.DirectionImport, before)
		assert.Equal(t, domain.DirectionPublish, c.CatalogDirection())
		assert.False(t, c.SyncCatalog)
		assert.False(t, c.CatalogAutoImport)
		assert.True(t, c.PublishCatalog)
		assert.Nil(t, c.CatalogUpdateAvailableAt)
		s, _ := e.state.Get(ctx)
		assert.Nil(t, s.FirstPublishConfirmedAt, "the new direction needs its own confirmation")
		assert.True(t, s.Dirty())

		// Back to IMPORT: the publisher forgets its dirty flag and warnings.
		require.NoError(t, e.state.SetExternalChange(ctx, &t0))
		c, before, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionImport, t0.Add(time.Hour), nil)
		require.NoError(t, err)
		assert.Equal(t, domain.DirectionPublish, before)
		assert.True(t, c.SyncCatalog)
		assert.False(t, c.PublishCatalog)
		s, _ = e.state.Get(ctx)
		assert.False(t, s.Dirty())
		assert.Nil(t, s.ExternalChangeAt)

		c, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionNone, t0, nil)
		require.NoError(t, err)
		assert.Equal(t, domain.DirectionNone, c.CatalogDirection())
	})

	t.Run("a direction change forgets what was published and its hashes", func(t *testing.T) {
		e := newPublishEnv(t)
		e.connect(t)
		require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "K", HubriseRef: "K", ContentHash: "h", PublishedAt: t0}}))
		_, err := e.db.ExecContext(ctx, `UPDATE hubrise_catalog_publish SET last_published_local_hash = 'l', last_published_remote_hash = 'r'`)
		require.NoError(t, err)
		_, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionImport, t0, nil)
		require.NoError(t, err)
		s, _ := e.state.Get(ctx)
		assert.Nil(t, s.LastPublishedLocalHash)
		assert.Nil(t, s.LastPublishedRemoteHash)
		n, err := e.items.Count(ctx, domain.EntityProduct)
		require.NoError(t, err)
		assert.Zero(t, n)

		// And entering PUBLISH again starts from nothing too.
		require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "K", HubriseRef: "K", ContentHash: "h", PublishedAt: t0}}))
		_, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
		require.NoError(t, err)
		n, err = e.items.Count(ctx, domain.EntityProduct)
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("staying in PUBLISH does not reset the publisher", func(t *testing.T) {
		e := newPublishEnv(t)
		e.connect(t)
		_, _, err := e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
		require.NoError(t, err)
		s, _ := e.state.Get(ctx)
		assert.NotNil(t, s.FirstPublishConfirmedAt)
	})

	t.Run("a refusing guard rolls everything back and sees both rows", func(t *testing.T) {
		e := newPublishEnv(t)
		e.connect(t)
		_, err := e.state.MarkDirty(ctx, t0)
		require.NoError(t, err)
		sentinel := fmt.Errorf("refused")
		_, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionNone, t0, func(_ context.Context, c *domain.Connection, s *domain.PublishState) error {
			assert.True(t, c.PublishCatalog)
			assert.True(t, s.Dirty())
			return sentinel
		})
		assert.ErrorIs(t, err, sentinel)
		got, _ := e.conn.Get(ctx)
		assert.True(t, got.PublishCatalog)
		s, _ := e.state.Get(ctx)
		assert.True(t, s.Dirty())
	})

	t.Run("settings refuse both directions together", func(t *testing.T) {
		e := newPublishEnv(t)
		e.connect(t)
		_, err := e.conn.UpdateSettings(ctx, domain.SettingsPatch{SyncCatalog: &yes})
		assert.ErrorIs(t, err, domain.ErrDirectionConflict)
		_, err = e.conn.UpdateSettings(ctx, domain.SettingsPatch{SyncCatalog: &no, PublishCatalog: &no})
		require.NoError(t, err)
		_, err = e.conn.UpdateSettings(ctx, domain.SettingsPatch{SyncCatalog: &yes, PublishCatalog: &yes})
		assert.ErrorIs(t, err, domain.ErrDirectionConflict)
		c, err := e.conn.UpdateSettings(ctx, domain.SettingsPatch{PublishImages: &yes})
		require.NoError(t, err)
		assert.True(t, c.PublishImages)
		assert.False(t, c.PublishCatalog)
	})
}

// Hammering both setters from several goroutines never leaves sync_catalog and publish_catalog both on.
func TestConcurrentDirectionSettersNeverLeaveBothModesOn(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	yes := true

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				if i%2 == 0 {
					_, err := e.conn.UpdateSettings(ctx, domain.SettingsPatch{SyncCatalog: &yes})
					if err != nil {
						assert.ErrorIs(t, err, domain.ErrDirectionConflict)
					}
				} else {
					_, _, err := e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
					assert.NoError(t, err)
				}
				var both bool
				assert.NoError(t, e.db.GetContext(ctx, &both, `SELECT sync_catalog AND publish_catalog FROM hubrise_connection`))
				assert.False(t, both)
			}
		}()
	}
	wg.Wait()
}

func TestConnectEffectsOnThePublisher(t *testing.T) {
	ctx := t.Context()
	e := newPublishEnv(t)
	e.connect(t)
	require.NoError(t, e.state.Block(ctx, "", domain.PublishWriteScopeMissing, "no scope", t0))
	require.NoError(t, e.items.Replace(ctx, []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "A", HubriseRef: "A", ContentHash: "h", PublishedAt: t0}}))

	// A plain reconnect clears the errors a new authorization can fix and keeps the rest.
	_, err := e.conn.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("x2"), LocationID: "loc", CatalogID: "cat", Scope: hubDefaultScope})
	require.NoError(t, err)
	s, _ := e.state.Get(ctx)
	assert.Nil(t, s.BlockedAt)
	assert.Nil(t, s.LastErrorCode)
	assert.NotNil(t, s.FirstPublishConfirmedAt)
	items, _ := e.items.List(ctx)
	assert.Len(t, items, 1)

	// A rejection is not something a reconnect fixes.
	require.NoError(t, e.state.Block(ctx, "", domain.PublishRejected, "422", t0))
	_, err = e.conn.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("x3"), LocationID: "loc", CatalogID: "cat", Scope: hubDefaultScope})
	require.NoError(t, err)
	s, _ = e.state.Get(ctx)
	assert.NotNil(t, s.BlockedAt)

	// Another location: a fresh preview and confirmation, and nothing is remembered of the old catalog.
	relink := t0
	_, err = e.conn.Connect(ctx, domain.ConnectParams{AccessTokenEnc: []byte("x4"), LocationID: "other", CatalogID: "cat2", Scope: hubDefaultScope, RelinkRequiredAt: &relink})
	require.NoError(t, err)
	s, _ = e.state.Get(ctx)
	assert.Nil(t, s.FirstPublishConfirmedAt)
	assert.Nil(t, s.BlockedAt)
	assert.True(t, s.Dirty())
	items, _ = e.items.List(ctx)
	assert.Empty(t, items)
	c, _ := e.conn.Get(ctx)
	assert.True(t, c.PublishCatalog, "the direction is kept, the pipeline is paused by the relink flag")
	assert.False(t, c.Enabled)
}

func TestConnectionDomainHelpers(t *testing.T) {
	assert.Equal(t, domain.DirectionNone, (*domain.Connection)(nil).CatalogDirection())
	assert.Equal(t, domain.DirectionImport, (&domain.Connection{SyncCatalog: true}).CatalogDirection())
	assert.Equal(t, domain.DirectionPublish, (&domain.Connection{SyncCatalog: true, PublishCatalog: true}).CatalogDirection())
	assert.False(t, (*domain.Connection)(nil).HasWriteScope())
	assert.False(t, (&domain.Connection{}).HasWriteScope())
	old := "location[orders.write,customer_list.write,catalog.read,inventory.read,inventory.write]"
	assert.False(t, (&domain.Connection{Scope: &old}).HasWriteScope(), "the scope of the first release")
	assert.True(t, (&domain.Connection{Scope: sp(hubDefaultScope)}).HasWriteScope())
	assert.True(t, domain.HasWriteScope("account[catalog.*]"))
	assert.True(t, domain.HasWriteScope("catalog.write"))
	assert.False(t, domain.HasWriteScope("location[catalogs.write]"))
	assert.True(t, domain.DirectionPublish.Valid())
	assert.False(t, domain.CatalogDirection("x").Valid())
}

// A transaction that fails half way leaves the old state, items and ids as they were.
func TestCompleteFailingInsideItsTransactionChangesNothing(t *testing.T) {
	ctx := t.Context()
	run := func(t *testing.T, table string, items []domain.PublishedItem, ids []domain.LinkHubriseID) (*publishEnv, error) {
		e := newPublishEnv(t)
		e.connect(t)
		links := infrastructure.NewCatalogLinkRepository(e.db)
		l := &domain.CatalogLink{EntityType: domain.EntityProduct, HubriseKey: "SKU-1", HubriseRef: "SKU-1", Origin: domain.OriginPublished}
		l.SetLocalID(e.fx.SalmonSushi.ID)
		require.NoError(t, links.Upsert(ctx, l))
		_, err := e.state.MarkDirty(ctx, t0)
		require.NoError(t, err)
		seq, ok, err := e.state.Claim(ctx, "a", t0.Add(time.Minute))
		require.NoError(t, err)
		require.True(t, ok)
		_, err = e.db.ExecContext(ctx, `ALTER TABLE `+table+` RENAME TO `+table+`_away`)
		require.NoError(t, err)
		return e, e.state.Complete(ctx, "a", seq, domain.PublishResult{At: t0}, items, ids)
	}
	item := []domain.PublishedItem{{EntityType: domain.EntityProduct, HubriseKey: "NEW", HubriseRef: "NEW", ContentHash: "h", PublishedAt: t0}}
	id := []domain.LinkHubriseID{{EntityType: domain.EntityProduct, Key: "SKU-1", HubriseID: "fresh"}}

	t.Run("the state row", func(t *testing.T) {
		_, err := run(t, "hubrise_catalog_publish", item, id)
		assert.Error(t, err)
	})
	t.Run("the published items", func(t *testing.T) {
		_, err := run(t, "hubrise_published_items", item, nil)
		assert.Error(t, err)
	})
	t.Run("the links", func(t *testing.T) {
		_, err := run(t, "hubrise_catalog_links", nil, id)
		assert.Error(t, err)
	})
}

// A connection that changes the publisher in the same transaction undoes both when the publisher
// cannot be written.
func TestConnectionChangesThatTouchThePublisherFailAsOne(t *testing.T) {
	ctx := t.Context()
	boom := func(t *testing.T, e *publishEnv, table string) {
		t.Helper()
		_, err := e.db.ExecContext(ctx, `CREATE FUNCTION boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`)
		require.NoError(t, err)
		_, err = e.db.ExecContext(ctx, `CREATE TRIGGER boom BEFORE UPDATE OR DELETE ON `+table+` FOR EACH STATEMENT EXECUTE FUNCTION boom()`)
		require.NoError(t, err)
	}
	connectParams := func(relink bool) domain.ConnectParams {
		p := domain.ConnectParams{AccessTokenEnc: []byte("x"), LocationID: "l", CatalogID: "c", Scope: hubDefaultScope}
		if relink {
			at := t0
			p.RelinkRequiredAt = &at
		}
		return p
	}

	t.Run("a relinking connection resets the publisher", func(t *testing.T) {
		e := newPublishEnv(t)
		boom(t, e, "hubrise_catalog_publish")
		_, err := e.conn.Connect(ctx, connectParams(true))
		assert.Error(t, err)
	})
	t.Run("a relinking connection forgets the published items", func(t *testing.T) {
		e := newPublishEnv(t)
		boom(t, e, "hubrise_published_items")
		_, err := e.conn.Connect(ctx, connectParams(true))
		assert.Error(t, err)
	})
	t.Run("the connection row cannot be written", func(t *testing.T) {
		e := newPublishEnv(t)
		_, err := e.conn.Connect(ctx, connectParams(false))
		require.NoError(t, err)
		boom(t, e, "hubrise_connection")
		_, err = e.conn.Connect(ctx, connectParams(false))
		assert.Error(t, err)
	})
	t.Run("the direction needs the publisher row", func(t *testing.T) {
		e := newPublishEnv(t)
		_, err := e.conn.Connect(ctx, connectParams(false))
		require.NoError(t, err)
		_, err = e.db.ExecContext(ctx, `DELETE FROM hubrise_catalog_publish`)
		require.NoError(t, err)
		_, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
		assert.Error(t, err)
	})
	t.Run("the direction is written or not at all", func(t *testing.T) {
		e := newPublishEnv(t)
		_, err := e.conn.Connect(ctx, connectParams(false))
		require.NoError(t, err)
		boom(t, e, "hubrise_connection")
		_, _, err = e.conn.ChangeCatalogDirection(ctx, domain.DirectionPublish, t0, nil)
		assert.Error(t, err)
	})
}
