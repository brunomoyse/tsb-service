package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"tsb-service/internal/modules/hubrise/domain"
)

// gateTTL is how long the hook path trusts a read of the connection row. The cache only saves
// queries on the hot path of an order: it is never a correctness boundary, because the workers
// re-read the switches inside the claim statement.
const gateTTL = 15 * time.Second

// gate answers "may the integration act right now?" with the connection row, cached for gateTTL.
type gate struct {
	conns domain.ConnectionRepository
	clock domain.Clock

	mu   sync.Mutex
	at   time.Time
	conn *domain.Connection // nil: not connected
	ok   bool
}

func newGate(conns domain.ConnectionRepository, clock domain.Clock) *gate {
	return &gate{conns: conns, clock: clock}
}

// Connection returns the connection row, or nil when the installation never connected.
func (g *gate) Connection(ctx context.Context) (*domain.Connection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock.Now()
	if g.ok && now.Sub(g.at) < gateTTL {
		return g.conn, nil
	}
	conn, err := g.conns.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		conn, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.conn, g.at, g.ok = conn, now, true
	return conn, nil
}

// Invalidate forgets the cached row, after a settings change.
func (g *gate) Invalidate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ok = false
}
