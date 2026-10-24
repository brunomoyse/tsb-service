package hubrise

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a manually advanced Clock whose timers fire when Advance passes them.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.waiters = append(c.waiters, t)
	return t.ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, w := range c.waiters {
			if w == t {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				return
			}
		}
	}
}

// Advance moves time forward and fires the timers that are due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			due = append(due, w)
		} else {
			rest = append(rest, w)
		}
	}
	c.waiters = rest
	now := c.now
	c.mu.Unlock()
	for _, w := range due {
		w.ch <- now
	}
}

// pending is the number of timers waiting.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func take(t *testing.T, l *Limiter, class Class, bucket Bucket) time.Duration {
	t.Helper()
	wait, err := l.tryTake(class, bucket)
	require.NoError(t, err)
	return wait
}

func TestLimiterMinuteWindow(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{PerMinute: 3}, WithClock(clock))
	for range 3 {
		assert.Zero(t, take(t, l, ClassCritical, BucketNone))
	}
	assert.Equal(t, time.Minute, take(t, l, ClassCritical, BucketNone), "the 4th waits for the 1st to leave the window")

	clock.Advance(20 * time.Second)
	assert.Equal(t, 40*time.Second, take(t, l, ClassCritical, BucketNone))

	done := make(chan error, 1)
	go func() { done <- l.Wait(context.Background(), ClassCritical, BucketNone) }()
	waitFor(t, "the waiter to sleep", func() bool { return clock.pending() == 1 })
	clock.Advance(40 * time.Second)
	require.NoError(t, <-done)
	assert.Equal(t, 1, l.Usage().Minute, "the first three left the window")
}

func TestLimiterHourAndDayWindows(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{PerHour: 4, PerDay: 6}, WithClock(clock))
	for range 4 {
		assert.Zero(t, take(t, l, ClassCritical, BucketNone))
		clock.Advance(time.Minute)
	}
	assert.Equal(t, 56*time.Minute, take(t, l, ClassCritical, BucketNone), "the hourly ceiling holds")

	clock.Advance(56 * time.Minute)
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))
	clock.Advance(time.Minute)
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))
	clock.Advance(time.Minute)
	// 6 requests in the last day: the daily ceiling now holds even though the hour is free.
	clock.Advance(2 * time.Hour)
	wait := take(t, l, ClassCritical, BucketNone)
	assert.Greater(t, wait, 20*time.Hour)
	clock.Advance(wait)
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))

	u := l.Usage()
	assert.LessOrEqual(t, u.Day, 6)
}

func TestLimiterBackgroundIsCutOffBeforeTheCriticalBudget(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{PerHour: 10, PerDay: 100, BackgroundShare: 0.6}, WithClock(clock))
	for range 6 {
		assert.Zero(t, take(t, l, ClassBackground, BucketNone))
		clock.Advance(time.Second)
	}
	_, err := l.tryTake(ClassBackground, BucketNone)
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, KindRateLimited, he.Kind)
	assert.Greater(t, he.RetryAfter, time.Duration(0))
	assert.Contains(t, he.Message, "background")

	// The same budget is still open to order traffic, up to the full ceiling.
	for range 4 {
		assert.Zero(t, take(t, l, ClassCritical, BucketNone))
	}
	assert.Greater(t, take(t, l, ClassCritical, BucketNone), time.Duration(0), "now even critical waits")

	// Wait returns the refusal instead of sleeping for the whole window.
	_, err = l.tryTake(ClassBackground, BucketNone)
	require.Error(t, err)
	err = l.Wait(Background(context.Background()), ClassBackground, BucketNone)
	require.ErrorAs(t, err, &he)
}

func TestLimiterBackgroundCutOffOnTheDailyBudget(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{PerDay: 10, BackgroundShare: 0.5}, WithClock(clock))
	for range 5 {
		assert.Zero(t, take(t, l, ClassBackground, BucketNone))
		clock.Advance(time.Hour)
	}
	_, err := l.tryTake(ClassBackground, BucketNone)
	require.Error(t, err)
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))
}

func TestLimiterBackgroundShareOutOfRangeDisablesTheCutOff(t *testing.T) {
	for _, share := range []float64{0, 1, 1.5, -1} {
		l := NewLimiter(Limits{PerHour: 2, BackgroundShare: share}, WithClock(newFakeClock()))
		assert.Zero(t, take(t, l, ClassBackground, BucketNone))
		assert.Zero(t, take(t, l, ClassBackground, BucketNone))
	}
}

func TestLimiterListBucketSplit(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(DefaultLimits(), WithClock(clock))

	// 6 order lookups use their reserved share, the 7th borrows the spare slot, the 8th waits.
	for i := range 7 {
		assert.Zero(t, take(t, l, ClassCritical, BucketOrderLookup), "lookup %d", i+1)
	}
	assert.Equal(t, time.Minute, take(t, l, ClassCritical, BucketOrderLookup))

	// The catalog still has its own 3 reads; its 4th finds the spare slot taken.
	for i := range 3 {
		assert.Zero(t, take(t, l, ClassCritical, BucketCatalog), "catalog read %d", i+1)
	}
	assert.Equal(t, time.Minute, take(t, l, ClassCritical, BucketCatalog))

	// Calls with no extra cap are not touched by the list bucket.
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))

	clock.Advance(time.Minute)
	assert.Zero(t, take(t, l, ClassCritical, BucketCatalog), "the buckets reopen after a minute")
}

func TestLimiterCatalogMayBorrowTheSpareSlot(t *testing.T) {
	l := NewLimiter(DefaultLimits(), WithClock(newFakeClock()))
	for range 4 {
		assert.Zero(t, take(t, l, ClassCritical, BucketCatalog))
	}
	assert.Greater(t, take(t, l, ClassCritical, BucketCatalog), time.Duration(0))
	// ...and then lookups no longer find the spare slot, only their own six.
	for range 6 {
		assert.Zero(t, take(t, l, ClassCritical, BucketOrderLookup))
	}
	assert.Greater(t, take(t, l, ClassCritical, BucketOrderLookup), time.Duration(0))
}

func TestLimiterListBucketWithNoShareForTheBucketNeverSpins(t *testing.T) {
	l := NewLimiter(Limits{OrderLookupPerMinute: 1}, WithClock(newFakeClock()))
	assert.Zero(t, take(t, l, ClassCritical, BucketOrderLookup))
	assert.Equal(t, time.Minute, take(t, l, ClassCritical, BucketCatalog), "no catalog share and no spare: not served, not spun on")
}

func TestLimiterZeroLimitsAreUnlimited(t *testing.T) {
	l := NewLimiter(Limits{}, WithClock(newFakeClock()))
	for range 1000 {
		require.Zero(t, take(t, l, ClassBackground, BucketCatalog))
	}
}

// A hundred order lookups queued behind the 10 a minute list bucket must not stop the catalog
// drift check from reading, and must themselves drain at the bucket's pace.
func TestLimiterQueuedLookupsDoNotStarveTheCatalog(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(DefaultLimits(), WithClock(clock))
	ctx := context.Background()

	var done atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if err := l.Wait(ctx, ClassCritical, BucketOrderLookup); err == nil {
				done.Add(1)
			}
		})
	}
	waitFor(t, "7 lookups served and 93 queued", func() bool { return done.Load() == 7 && clock.pending() == 93 })

	// Three catalog reads go through at once while the 93 lookups are queued.
	for i := range 3 {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		require.NoError(t, l.Wait(cctx, ClassBackground, BucketCatalog), "catalog read %d", i+1)
		cancel()
	}
	assert.Equal(t, int32(7), done.Load())

	// The queue drains at 7 a minute (6 reserved plus the spare), never faster.
	minutes := 0
	for done.Load() < 100 {
		before := done.Load()
		clock.Advance(time.Minute)
		minutes++
		waitFor(t, "the queue to settle", func() bool { return int(done.Load())+clock.pending() == 100 })
		assert.LessOrEqual(t, done.Load()-before, int32(7), "at most 7 lookups a minute")
		require.LessOrEqual(t, minutes, 30)
	}
	wg.Wait()
	assert.GreaterOrEqual(t, minutes, 14, "100 lookups cannot drain faster than 7 a minute")
}

func TestLimiterPause(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{}, WithClock(clock))
	l.Pause(0)
	l.Pause(-time.Second)
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))

	l.Pause(2 * time.Second)
	assert.Equal(t, 2*time.Second, take(t, l, ClassCritical, BucketNone))
	l.Pause(time.Second) // a shorter pause never shortens an existing one
	assert.Equal(t, 2*time.Second, take(t, l, ClassCritical, BucketNone))
	clock.Advance(2 * time.Second)
	assert.Zero(t, take(t, l, ClassCritical, BucketNone))

	l.Pause(time.Hour)
	assert.Equal(t, maxPause, take(t, l, ClassCritical, BucketNone), "a pause is capped")
}

func TestLimiterWaitHonoursTheContext(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{PerMinute: 1}, WithClock(clock))
	require.NoError(t, l.Wait(context.Background(), ClassCritical, BucketNone))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Wait(ctx, ClassCritical, BucketNone) }()
	waitFor(t, "the waiter to sleep", func() bool { return clock.pending() == 1 })
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	waitFor(t, "the timer to be released", func() bool { return clock.pending() == 0 })

	assert.ErrorIs(t, l.Wait(ctx, ClassCritical, BucketNone), context.Canceled, "an ended context never waits")
	assert.Equal(t, 1, l.Usage().Minute, "the refused calls were not counted")
}

func TestLimiterUsageSlides(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(Limits{}, WithClock(clock))
	for range 3 {
		take(t, l, ClassCritical, BucketNone)
	}
	clock.Advance(90 * time.Second)
	take(t, l, ClassCritical, BucketNone)
	clock.Advance(2 * time.Hour)
	assert.Equal(t, Usage{Minute: 0, Hour: 0, Day: 4}, l.Usage())
	clock.Advance(25 * time.Hour)
	assert.Equal(t, Usage{}, l.Usage())
}

func TestClassInContext(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, ClassCritical, ClassFrom(ctx))
	assert.Equal(t, ClassBackground, ClassFrom(Background(ctx)))
	assert.Equal(t, ClassCritical, ClassFrom(WithClass(Background(ctx), ClassCritical)))
}

func TestRealClock(t *testing.T) {
	c := realClock{}
	assert.WithinDuration(t, time.Now(), c.Now(), time.Second)
	ch, stop := c.After(time.Millisecond)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("the timer never fired")
	}
	stop()
	_, stop = c.After(time.Hour)
	stop()

	l := NewLimiter(DefaultLimits())
	require.NoError(t, l.Wait(context.Background(), ClassCritical, BucketNone))
}

func TestWindowHelpers(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []time.Time{base, base.Add(10 * time.Second), base.Add(20 * time.Second)}
	now := base.Add(30 * time.Second)

	assert.Equal(t, 3, countSince(events, now.Add(-time.Minute)))
	assert.Equal(t, 2, countSince(events, now.Add(-25*time.Second)))
	assert.Equal(t, 1, countSince(events, now.Add(-20*time.Second)), "strictly after the cutoff")
	assert.Equal(t, 0, countSince(nil, now))

	assert.Zero(t, windowWait(events, 4, time.Minute, now))
	assert.Zero(t, windowWait(events, 0, time.Minute, now))
	assert.Equal(t, 30*time.Second, windowWait(events, 3, time.Minute, now))
	assert.Equal(t, 40*time.Second, windowWait(events, 2, time.Minute, now), "the 2nd most recent has to leave")

	kept := dropBefore(append([]time.Time(nil), events...), base.Add(10*time.Second))
	assert.Equal(t, events[2:], kept[len(kept)-1:])
	assert.Len(t, kept, 1)
	assert.True(t, sort.SliceIsSorted(kept, func(i, j int) bool { return kept[i].Before(kept[j]) }))
	assert.Len(t, dropBefore(events, base.Add(-time.Second)), 3)
}

func TestLimitsSplit(t *testing.T) {
	d := DefaultLimits()
	assert.Equal(t, d, d.Split(0))
	assert.Equal(t, d, d.Split(1))

	half := d.Split(2)
	assert.Equal(t, Limits{
		PerMinute: 200, PerHour: 1000, PerDay: 4000,
		OrderLookupPerMinute: 3, CatalogPerMinute: 1, SparePerMinute: 0,
		BackgroundShare: 0.6,
	}, half)
	// Two replicas together stay under the real HubRise caps.
	assert.LessOrEqual(t, 2*half.PerMinute, 500)
	assert.LessOrEqual(t, 2*half.PerHour, 2500)
	assert.LessOrEqual(t, 2*half.PerDay, 10000)
	assert.LessOrEqual(t, 2*(half.OrderLookupPerMinute+half.CatalogPerMinute+half.SparePerMinute), 10)

	// A positive window never rounds down to zero (that would disable it), a disabled one stays so.
	in := Limits{PerMinute: 1, PerHour: 0, PerDay: -1, OrderLookupPerMinute: 1}
	assert.Equal(t, in, in.Split(4))
}
