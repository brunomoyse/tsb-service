package hubrise

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Class is the priority of a caller of the API. It travels in the context (WithClass), so that the
// layer that knows why a call is made decides, not the endpoint method.
type Class int

const (
	// ClassCritical is the default: order create, status PATCH, private_ref lookups, inventory
	// PATCH. It may spend the whole hourly and daily budget.
	ClassCritical Class = iota
	// ClassBackground is housekeeping (reconciler refresh, catalog drift check, inventory poll,
	// callback poller). It is cut off once the hourly or daily budget is BackgroundShare used, so
	// it can never eat the headroom the critical calls need.
	ClassBackground
)

type classKey struct{}

// WithClass returns a context whose API calls count as class.
func WithClass(ctx context.Context, c Class) context.Context {
	return context.WithValue(ctx, classKey{}, c)
}

// Background marks the API calls made with the returned context as background work.
func Background(ctx context.Context) context.Context { return WithClass(ctx, ClassBackground) }

// ClassFrom returns the class of the calls made with ctx, ClassCritical when none was set.
func ClassFrom(ctx context.Context) Class {
	if c, ok := ctx.Value(classKey{}).(Class); ok {
		return c
	}
	return ClassCritical
}

// Bucket is the extra per minute cap some endpoints have on top of the connection wide windows:
// HubRise allows only 10 requests a minute on GET /catalogs/:id and on the order LIST endpoint
// (which includes the ?private_ref= lookup). The 10 are split by priority.
type Bucket int

const (
	// BucketNone is an endpoint with no extra cap.
	BucketNone Bucket = iota
	// BucketOrderLookup is the order list endpoint (idempotency lookups): 6 a minute reserved.
	BucketOrderLookup
	// BucketCatalog is GET /catalogs/:id: 3 a minute reserved.
	BucketCatalog
)

// Limits are the ceilings the limiter enforces. A value <= 0 disables that window, so the zero
// Limits is "unlimited" (handy in tests that are not about rate limiting).
type Limits struct {
	// PerMinute, PerHour and PerDay are the connection wide windows.
	PerMinute, PerHour, PerDay int
	// OrderLookupPerMinute and CatalogPerMinute are the reserved shares of the 10 a minute list
	// bucket, SparePerMinute the remainder that either may borrow.
	OrderLookupPerMinute, CatalogPerMinute, SparePerMinute int
	// BackgroundShare is the fraction of the hourly and daily ceilings after which background calls
	// are refused (0.6).
	BackgroundShare float64
}

// DefaultLimits are HubRise's documented limits (500/min, 2,500/h, 10,000/day, 10/min on the list
// bucket) with headroom: 400, 2,000 and 8,000, and the list bucket split 6 order lookups, 3
// catalog reads and 1 spare.
//
// A Limiter lives in process memory, while HubRise counts per connection and production runs two
// replicas. These ceilings are therefore per process: with N replicas sending HubRise traffic the
// connection can see N times as much, and a 429 pause only stops the replica that received it.
// Every caller that can send from several replicas (the outbox drain claims rows on each one)
// must build its Limiter from DefaultLimits().Split(replicas), so that the sum stays under the
// documented caps. The lease guarded singleton jobs share the connection with the other replicas
// too, so the split applies to them as well.
func DefaultLimits() Limits {
	return Limits{
		PerMinute: 400, PerHour: 2000, PerDay: 8000,
		OrderLookupPerMinute: 6, CatalogPerMinute: 3, SparePerMinute: 1,
		BackgroundShare: 0.6,
	}
}

// Split returns the limits one of n replicas may spend so that all n together stay within l. Each
// positive window is divided by n, rounded down but never below 1 (0 would disable the window);
// the spare list slot rounds down to 0. A window <= 0 (disabled) stays as it is and n < 2 returns
// l unchanged.
func (l Limits) Split(n int) Limits {
	if n < 2 {
		return l
	}
	div := func(v, floor int) int {
		if v <= 0 {
			return v
		}
		return max(v/n, floor)
	}
	l.PerMinute = div(l.PerMinute, 1)
	l.PerHour = div(l.PerHour, 1)
	l.PerDay = div(l.PerDay, 1)
	l.OrderLookupPerMinute = div(l.OrderLookupPerMinute, 1)
	l.CatalogPerMinute = div(l.CatalogPerMinute, 1)
	l.SparePerMinute = div(l.SparePerMinute, 0)
	return l
}

// Clock is the time source of the limiter, replaceable in tests.
type Clock interface {
	Now() time.Time
	// After returns a channel that fires after d, and a function releasing the timer.
	After(d time.Duration) (<-chan time.Time, func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// maxPause bounds how long a 429 can stop the whole connection.
const maxPause = 5 * time.Minute

const (
	minute = time.Minute
	hour   = time.Hour
	day    = 24 * time.Hour
)

// Limiter spends one connection's request budget. It is safe for concurrent use and must be shared
// by every Client built for the same HubRise connection (the budget is per connection, not per
// Client value).
type Limiter struct {
	clock Clock
	lim   Limits

	mu          sync.Mutex
	all         []time.Time // every request of the last 24 h, oldest first
	lookups     []time.Time // the list bucket, last minute, per share
	catalogs    []time.Time
	spare       []time.Time
	pausedUntil time.Time
}

// LimiterOption configures a Limiter.
type LimiterOption func(*Limiter)

// WithClock replaces the time source (tests).
func WithClock(c Clock) LimiterOption { return func(l *Limiter) { l.clock = c } }

// NewLimiter returns a limiter enforcing lim.
func NewLimiter(lim Limits, opts ...LimiterOption) *Limiter {
	l := &Limiter{clock: realClock{}, lim: lim}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Wait blocks until a request of the given class on the given bucket may be sent, and counts it.
// It returns early with ctx.Err() when the context ends. A background request that would cross the
// background cut-off is not queued but refused at once with a KindRateLimited *Error carrying the
// time until the window frees, because waiting hours for housekeeping would only pile up
// goroutines.
func (l *Limiter) Wait(ctx context.Context, class Class, bucket Bucket) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wait, err := l.tryTake(class, bucket)
		if err != nil {
			return err
		}
		if wait <= 0 {
			return nil
		}
		timer, stop := l.clock.After(wait)
		select {
		case <-ctx.Done():
			stop()
			return ctx.Err()
		case <-timer:
		}
	}
}

// Pause stops every request for d (capped), typically after a 429 with a Retry-After.
func (l *Limiter) Pause(d time.Duration) {
	if d <= 0 {
		return
	}
	d = min(d, maxPause)
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := l.clock.Now().Add(d); until.After(l.pausedUntil) {
		l.pausedUntil = until
	}
}

// Usage is how many requests were counted in each window.
type Usage struct {
	Minute, Hour, Day int
}

// Usage returns the current counts.
func (l *Limiter) Usage() Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.prune(now)
	return Usage{
		Minute: countSince(l.all, now.Add(-minute)),
		Hour:   countSince(l.all, now.Add(-hour)),
		Day:    len(l.all),
	}
}

// tryTake counts the request when it may go now (returning 0), else returns how long to wait.
func (l *Limiter) tryTake(class Class, bucket Bucket) (time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.prune(now)

	if class == ClassBackground {
		if err := l.backgroundCutoff(now); err != nil {
			return 0, err
		}
	}

	wait := time.Duration(0)
	if now.Before(l.pausedUntil) {
		wait = l.pausedUntil.Sub(now)
	}
	wait = max(wait,
		windowWait(l.all, l.lim.PerMinute, minute, now),
		windowWait(l.all, l.lim.PerHour, hour, now),
		windowWait(l.all, l.lim.PerDay, day, now))
	slot, bw := l.pickSlot(bucket, now)
	wait = max(wait, bw)
	if wait > 0 {
		return wait, nil
	}

	l.all = append(l.all, now)
	if slot != nil {
		*slot = append(*slot, now)
	}
	return 0, nil
}

// backgroundCutoff refuses a background request once the hourly or daily budget is used past the
// background share.
func (l *Limiter) backgroundCutoff(now time.Time) error {
	share := l.lim.BackgroundShare
	if share <= 0 || share >= 1 {
		return nil
	}
	var retry time.Duration
	for _, w := range []struct {
		ceiling int
		window  time.Duration
	}{{l.lim.PerHour, hour}, {l.lim.PerDay, day}} {
		if w.ceiling <= 0 {
			continue
		}
		limit := int(float64(w.ceiling) * share)
		if countSince(l.all, now.Add(-w.window)) >= limit {
			retry = max(retry, windowWait(l.all, limit, w.window, now))
		}
	}
	if retry <= 0 {
		return nil
	}
	return &Error{
		Kind:       KindRateLimited,
		Message:    "background request budget exhausted, keeping the rest for order traffic",
		RetryAfter: retry,
	}
}

// pickSlot chooses the bucket share a list request is charged to: its own reserved share while it
// has room, else the spare one. It returns the wait when neither has room.
func (l *Limiter) pickSlot(bucket Bucket, now time.Time) (*[]time.Time, time.Duration) {
	var own *[]time.Time
	var ownCap int
	switch bucket {
	case BucketOrderLookup:
		own, ownCap = &l.lookups, l.lim.OrderLookupPerMinute
	case BucketCatalog:
		own, ownCap = &l.catalogs, l.lim.CatalogPerMinute
	default:
		return nil, 0
	}
	spareCap := l.lim.SparePerMinute
	if l.lim.OrderLookupPerMinute+l.lim.CatalogPerMinute+spareCap <= 0 {
		return nil, 0 // the list bucket is not enforced
	}
	if ownCap > 0 && countSince(*own, now.Add(-minute)) < ownCap {
		return own, 0
	}
	if spareCap > 0 && countSince(l.spare, now.Add(-minute)) < spareCap {
		return &l.spare, 0
	}
	// Neither has room: wait for the first of them to free a slot.
	wait := time.Duration(-1)
	for _, c := range []struct {
		events  []time.Time
		ceiling int
	}{{*own, ownCap}, {l.spare, spareCap}} {
		if c.ceiling <= 0 {
			continue
		}
		if w := windowWait(c.events, c.ceiling, minute, now); wait < 0 || w < wait {
			wait = w
		}
	}
	if wait < 0 {
		// Both shares are zero for this bucket: it can never be served, so do not spin.
		wait = minute
	}
	return nil, wait
}

// prune drops what is older than the longest window.
func (l *Limiter) prune(now time.Time) {
	l.all = dropBefore(l.all, now.Add(-day))
	l.lookups = dropBefore(l.lookups, now.Add(-minute))
	l.catalogs = dropBefore(l.catalogs, now.Add(-minute))
	l.spare = dropBefore(l.spare, now.Add(-minute))
}

// dropBefore removes the events at or before cutoff from a sorted slice.
func dropBefore(events []time.Time, cutoff time.Time) []time.Time {
	i := sort.Search(len(events), func(i int) bool { return events[i].After(cutoff) })
	if i == 0 {
		return events
	}
	return append(events[:0], events[i:]...)
}

// countSince counts the events strictly after cutoff in a sorted slice.
func countSince(events []time.Time, cutoff time.Time) int {
	i := sort.Search(len(events), func(i int) bool { return events[i].After(cutoff) })
	return len(events) - i
}

// windowWait is how long until one more event fits in a sliding window of at most limit events, 0
// when it fits now. A limit <= 0 is no limit.
func windowWait(events []time.Time, limit int, window time.Duration, now time.Time) time.Duration {
	if limit <= 0 {
		return 0
	}
	n := countSince(events, now.Add(-window))
	if n < limit {
		return 0
	}
	// The event that has to expire is the limit-th most recent one.
	return events[len(events)-limit].Add(window).Sub(now)
}
