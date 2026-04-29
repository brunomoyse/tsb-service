package auth

import (
	"sync"
	"time"
)

// idempotencyGate serializes concurrent operations sharing the same primary
// key and caches successful responses so a duplicate or retried request
// returns the same response without hitting the upstream. Used to protect
// Zitadel session/OIDC endpoints whose underlying state machines consume
// one-shot codes (the OTP-email code, the OIDC auth code) — without it, a
// double-submit causes the second call to fail and the user sees an
// "expired" error even though the first call actually succeeded.
//
// Cache key is (primaryKey, fingerprint). primaryKey gates the per-key mutex
// (e.g., sessionID); fingerprint distinguishes a legitimate retry of the
// same operation from a new attempt with different inputs (e.g., the user
// types the wrong code, then the right one — only the right one should hit
// the cache). Failures are never cached: a wrong code must remain retryable.
type idempotencyGate[T any] struct {
	mu      sync.Mutex
	entries map[string]*idempotencyEntry[T]
	ttl     time.Duration
}

type idempotencyEntry[T any] struct {
	mu          sync.Mutex
	fingerprint string
	response    *T
	expires     time.Time
}

func newIdempotencyGate[T any](ttl time.Duration) *idempotencyGate[T] {
	return &idempotencyGate[T]{
		entries: make(map[string]*idempotencyEntry[T]),
		ttl:     ttl,
	}
}

// acquire returns the entry for primaryKey with its per-key mutex held.
// Caller MUST defer gate.release(entry). While locked, callers may read
// entry.hit() and call gate.cache().
func (g *idempotencyGate[T]) acquire(primaryKey string) *idempotencyEntry[T] {
	g.mu.Lock()
	g.gcLocked()
	e, ok := g.entries[primaryKey]
	if !ok {
		e = &idempotencyEntry[T]{}
		g.entries[primaryKey] = e
	}
	g.mu.Unlock()
	e.mu.Lock()
	return e
}

func (g *idempotencyGate[T]) release(e *idempotencyEntry[T]) {
	e.mu.Unlock()
}

// cache records a successful response for the given fingerprint. The entry
// must be held (caller acquired it).
func (g *idempotencyGate[T]) cache(e *idempotencyEntry[T], fingerprint string, response T) {
	cp := response
	e.fingerprint = fingerprint
	e.response = &cp
	e.expires = time.Now().Add(g.ttl)
}

// gcLocked deletes expired entries. Caller must hold g.mu. Each entry is
// inspected via TryLock so a held entry (currently in cache() or hit() under
// the caller's per-key mutex) is left alone — it'll be revisited on the
// next acquire. This avoids racing with concurrent writers.
func (g *idempotencyGate[T]) gcLocked() {
	now := time.Now()
	for k, e := range g.entries {
		if !e.mu.TryLock() {
			continue
		}
		expired := e.response != nil && e.expires.Before(now)
		e.mu.Unlock()
		if expired {
			delete(g.entries, k)
		}
	}
}

// hit returns the cached response if its fingerprint matches and the entry
// has not expired.
func (e *idempotencyEntry[T]) hit(fingerprint string) (T, bool) {
	var zero T
	if e.response == nil || e.fingerprint != fingerprint || time.Now().After(e.expires) {
		return zero, false
	}
	return *e.response, true
}

// Package-level gates. Verify TTL matches Zitadel's OTP code TTL (~5min) so
// a cached success cannot outlive a session that Zitadel itself has rotated
// out. Finalize TTL is shorter — the cached callbackUrl carries an OIDC
// auth code that Zitadel typically expires after ~60s anyway.
var (
	verifyGate   = newIdempotencyGate[verifyOtpResponse](5 * time.Minute)
	finalizeGate = newIdempotencyGate[finalizeResponse](2 * time.Minute)
)

// resetIdempotencyGatesForTest re-initializes both gates so tests start from
// a clean slate. Called from setupMockZitadel.
func resetIdempotencyGatesForTest() {
	verifyGate = newIdempotencyGate[verifyOtpResponse](5 * time.Minute)
	finalizeGate = newIdempotencyGate[finalizeResponse](2 * time.Minute)
}
