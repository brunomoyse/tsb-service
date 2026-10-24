package infrastructure_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/infrastructure"
)

func TestOAuthStateRepository(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewOAuthStateRepository(e.db)
	ctx := t.Context()
	now := time.Now()
	admin := e.fx.AdminUser.ID

	require.NoError(t, repo.Create(ctx, "state-ok", admin, now.Add(10*time.Minute)))
	require.Error(t, repo.Create(ctx, "state-ok", admin, now.Add(10*time.Minute)), "a state is unique")

	got, ok, err := repo.Consume(ctx, "state-ok", now)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, admin, got)

	_, ok, err = repo.Consume(ctx, "state-ok", now)
	require.NoError(t, err)
	assert.False(t, ok, "a state is single use")

	_, ok, err = repo.Consume(ctx, "never-issued", now)
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, repo.Create(ctx, "state-old", admin, now.Add(-time.Second)))
	got, ok, err = repo.Consume(ctx, "state-old", now)
	require.NoError(t, err)
	assert.False(t, ok, "an expired state is refused")
	assert.Equal(t, uuid.Nil, got)
	_, ok, _ = repo.Consume(ctx, "state-old", now.Add(-time.Hour))
	assert.False(t, ok, "and it was consumed anyway")

	require.NoError(t, repo.Create(ctx, "a", admin, now.Add(-time.Minute)))
	require.NoError(t, repo.Create(ctx, "b", admin, now.Add(time.Minute)))
	n, err := repo.PurgeExpired(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	_, ok, _ = repo.Consume(ctx, "b", now)
	assert.True(t, ok)
}

func TestLeaseRepository(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLeaseRepository(e.db)
	ctx := t.Context()
	now := time.Now()

	ok, err := repo.Acquire(ctx, "reconciler", "pod-a", now, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "a free lease is taken")

	ok, _ = repo.Acquire(ctx, "reconciler", "pod-b", now.Add(30*time.Second), time.Minute)
	assert.False(t, ok, "a live lease is not stolen")

	ok, _ = repo.Acquire(ctx, "reconciler", "pod-a", now.Add(30*time.Second), time.Minute)
	assert.True(t, ok, "the holder renews")

	ok, _ = repo.Acquire(ctx, "reconciler", "pod-b", now.Add(80*time.Second), time.Minute)
	assert.False(t, ok, "the renewal pushed the expiry out")

	ok, _ = repo.Acquire(ctx, "reconciler", "pod-b", now.Add(91*time.Second), time.Minute)
	assert.True(t, ok, "an expired lease is taken over")

	ok, _ = repo.Acquire(ctx, "sweeper", "pod-a", now, time.Minute)
	assert.True(t, ok, "leases are per name")

	// Release only works for the holder.
	require.NoError(t, repo.Release(ctx, "reconciler", "pod-a"))
	ok, _ = repo.Acquire(ctx, "reconciler", "pod-c", now.Add(92*time.Second), time.Minute)
	assert.False(t, ok, "pod-a does not hold it any more, so its release changed nothing")
	require.NoError(t, repo.Release(ctx, "reconciler", "pod-b"))
	ok, _ = repo.Acquire(ctx, "reconciler", "pod-c", now.Add(92*time.Second), time.Minute)
	assert.True(t, ok)
}

func TestLeaseRepositoryConcurrent(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewLeaseRepository(e.db)
	now := time.Now()

	const workers = 12
	results := make(chan bool, workers)
	for i := range workers {
		go func() {
			ok, err := repo.Acquire(t.Context(), "job", "holder-"+string(rune('a'+i)), now, time.Minute)
			if err != nil {
				ok = false
			}
			results <- ok
		}()
	}
	won := 0
	for range workers {
		if <-results {
			won++
		}
	}
	assert.Equal(t, 1, won, "exactly one replica gets the lease")
}

func TestForeignOrderRepository(t *testing.T) {
	e := newEnv(t)
	repo := infrastructure.NewForeignOrderRepository(e.db)
	ctx := t.Context()
	now := time.Now()

	found, err := repo.IsForeign(ctx, "ho-1")
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Mark(ctx, "ho-1", now.Add(-10*24*time.Hour)))
	require.NoError(t, repo.Mark(ctx, "ho-2", now))
	found, _ = repo.IsForeign(ctx, "ho-1")
	assert.True(t, found)

	// Marking again refreshes the timestamp instead of failing.
	require.NoError(t, repo.Mark(ctx, "ho-1", now))
	n, err := repo.Purge(ctx, now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)

	require.NoError(t, repo.Mark(ctx, "ho-3", now.Add(-8*24*time.Hour)))
	n, err = repo.Purge(ctx, now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	found, _ = repo.IsForeign(ctx, "ho-3")
	assert.False(t, found)
	found, _ = repo.IsForeign(ctx, "ho-2")
	assert.True(t, found)
}
