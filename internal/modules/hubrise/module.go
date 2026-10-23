// Package hubrise is the composition root of the HubRise integration: it reads the deployment
// configuration and builds the storage layer. The application services (connection, catalog,
// outbox, events...) are added to the Module by later tasks. When HubRise is not configured the
// constructor returns nil and nothing else of the integration runs.
package hubrise

import (
	"fmt"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	"tsb-service/internal/modules/hubrise/infrastructure/tokencrypt"
)

// Module holds the ports of the integration, wired to the admin database pool.
type Module struct {
	Config Config
	Clock  domain.Clock
	Codec  domain.TokenCodec

	Connections  domain.ConnectionRepository
	OAuthStates  domain.OAuthStateRepository
	Leases       domain.LeaseRepository
	CatalogLinks domain.CatalogLinkRepository
	Outbox       domain.OutboxRepository
	Orders       domain.OrderLinkRepository
	Events       domain.EventRepository
	Foreign      domain.ForeignOrderRepository
}

// NewModule builds the module on the admin pool (the hubrise tables get no per-role grants). It
// returns (nil, nil) when cfg is not Available: callers keep a nil *Module and every hook is a
// no-op. clock may be nil (the Europe/Brussels system clock is used).
func NewModule(cfg Config, adminDB *sqlx.DB, clock domain.Clock) (*Module, error) {
	if !cfg.Available() {
		return nil, nil // a nil Module is the documented "not configured" value
	}
	codec, err := tokencrypt.New(cfg.TokenKey)
	if err != nil {
		return nil, fmt.Errorf("hubrise: %w", err)
	}
	if clock == nil {
		clock = infrastructure.SystemClock{}
	}
	return &Module{
		Config:       cfg,
		Clock:        clock,
		Codec:        codec,
		Connections:  infrastructure.NewConnectionRepository(adminDB),
		OAuthStates:  infrastructure.NewOAuthStateRepository(adminDB),
		Leases:       infrastructure.NewLeaseRepository(adminDB),
		CatalogLinks: infrastructure.NewCatalogLinkRepository(adminDB),
		Outbox:       infrastructure.NewOutboxRepository(adminDB),
		Orders:       infrastructure.NewOrderLinkRepository(adminDB),
		Events:       infrastructure.NewEventRepository(adminDB),
		Foreign:      infrastructure.NewForeignOrderRepository(adminDB),
	}, nil
}

// Enabled reports whether the module exists (HubRise is available on this deployment). It is safe
// on a nil Module.
func (m *Module) Enabled() bool { return m != nil }
