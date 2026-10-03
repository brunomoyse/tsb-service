package application

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// deviceStatusCache remembers, per device, the outcome of the last
// enrolled/revoked check (nil = active) for a short TTL so every POS request
// does not hit the database.
type deviceStatusCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[uuid.UUID]deviceStatusEntry
}

type deviceStatusEntry struct {
	err     error
	expires time.Time
}

func newDeviceStatusCache(ttl time.Duration) *deviceStatusCache {
	return &deviceStatusCache{ttl: ttl, now: time.Now, entries: make(map[uuid.UUID]deviceStatusEntry)}
}

func (c *deviceStatusCache) get(id uuid.UUID) (found bool, status error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || !c.now().Before(e.expires) {
		return false, nil
	}
	return true, e.err
}

func (c *deviceStatusCache) put(id uuid.UUID, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// The device table is tiny; drop expired entries on write to stay bounded.
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[id] = deviceStatusEntry{err: err, expires: now.Add(c.ttl)}
}
