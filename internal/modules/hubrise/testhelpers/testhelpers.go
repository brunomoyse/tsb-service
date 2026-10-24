// Package testhelpers is test support for the HubRise integration: a deterministic token key and
// codec, a fake clock, and a seeder that writes a connected hubrise_connection row. It depends on
// the domain and the token codec only (not on the repositories), so any package's tests can import
// it without an import cycle.
package testhelpers

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure/tokencrypt"
)

// TestTokenKey is the AES-256 key every test installation uses for HUBRISE_TOKEN_KEY.
var TestTokenKey = bytes.Repeat([]byte{0x7e}, tokencrypt.KeySize)

// TestCodec returns the token codec keyed with TestTokenKey.
func TestCodec(tb testing.TB) *tokencrypt.Codec {
	tb.Helper()
	c, err := tokencrypt.New(TestTokenKey)
	if err != nil {
		tb.Fatalf("testhelpers: codec: %v", err)
	}
	return c
}

// Default values of a seeded connection; they match the ids the HubRise mock serves.
const (
	DefaultToken            = "test-access-token"
	DefaultAccountID        = "acc-1"
	DefaultAccountName      = "Test Account"
	DefaultLocationID       = "loc-1"
	DefaultLocationName     = "Test Location"
	DefaultCatalogID        = "cat-1"
	DefaultCatalogName      = "Test Catalog"
	DefaultCustomerListID   = "cl-1"
	DefaultCustomerListName = "Test Customers"
	DefaultScope            = "location[orders.write,customer_list.write,catalog.read,inventory.read,inventory.write]"
	WriteScope              = "location[orders.write,customer_list.write,catalog.read,catalog.write,inventory.read,inventory.write]"
)

type seed struct {
	token                                                             string
	enabled                                                           bool
	pushOrders, syncStatus, syncInventory, syncCatalog, syncCustomers bool
	pushSince                                                         *time.Time
	locationID, catalogID                                             string
	customerListID, customerListName                                  string
	publish                                                           bool
	scope                                                             string
}

// SeedOption customizes SeedConnection.
type SeedOption func(*seed)

// WithToken sets the plain access token that gets sealed into the row.
func WithToken(token string) SeedOption { return func(s *seed) { s.token = token } }

// WithEnabled sets the runtime switch (default true).
func WithEnabled(enabled bool) SeedOption { return func(s *seed) { s.enabled = enabled } }

// WithFeatures sets the push_orders, sync_status, sync_inventory, sync_catalog and sync_customers
// toggles (default true, true, true, false, true).
func WithFeatures(pushOrders, syncStatus, syncInventory, syncCatalog, syncCustomers bool) SeedOption {
	return func(s *seed) {
		s.pushOrders, s.syncStatus, s.syncInventory, s.syncCatalog, s.syncCustomers =
			pushOrders, syncStatus, syncInventory, syncCatalog, syncCustomers
	}
}

// WithPushOrdersSince sets push_orders_since (default: unset).
func WithPushOrdersSince(t time.Time) SeedOption { return func(s *seed) { s.pushSince = &t } }

// WithLocation sets the location and catalog ids.
func WithLocation(locationID, catalogID string) SeedOption {
	return func(s *seed) { s.locationID, s.catalogID = locationID, catalogID }
}

// WithCustomerList sets the customer list id and name (default: DefaultCustomerListID). An empty id
// seeds a connection without a customer list.
func WithCustomerList(id, name string) SeedOption {
	return func(s *seed) { s.customerListID, s.customerListName = id, name }
}

// WithPublishCatalog seeds the dashboard-owned menu mode: publish_catalog on, catalog sync off, and
// a scope that grants catalog.write.
func WithPublishCatalog() SeedOption {
	return func(s *seed) {
		s.publish, s.syncCatalog = true, false
		s.scope = WriteScope
	}
}

// WithScope sets the scope string stored at the last authorization.
func WithScope(scope string) SeedOption { return func(s *seed) { s.scope = scope } }

// Seeded describes what SeedConnection wrote.
type Seeded struct {
	// Token is the plain access token (what the mock expects in X-Access-Token).
	Token      string
	LocationID string
	CatalogID  string
}

// SeedConnection writes (or replaces) the single hubrise_connection row as a connected, enabled
// installation: token sealed with TestCodec, account, location, catalog and customer list ids set.
// It is for tests of the workers and services that need a connection without running OAuth.
func SeedConnection(tb testing.TB, db sqlx.ExecerContext, opts ...SeedOption) Seeded {
	tb.Helper()
	s := &seed{
		token: DefaultToken, enabled: true,
		pushOrders: true, syncStatus: true, syncInventory: true, syncCatalog: false, syncCustomers: true,
		locationID: DefaultLocationID, catalogID: DefaultCatalogID,
		customerListID: DefaultCustomerListID, customerListName: DefaultCustomerListName, scope: DefaultScope,
	}
	for _, o := range opts {
		o(s)
	}
	enc, err := TestCodec(tb).Encrypt(s.token)
	if err != nil {
		tb.Fatalf("testhelpers: seal token: %v", err)
	}
	_, err = db.ExecContext(tb.Context(), `
		INSERT INTO hubrise_connection (id, enabled, access_token_enc, account_id, account_name, location_id,
			location_name, catalog_id, catalog_name, customer_list_id, customer_list_name, scope,
			push_orders, sync_status, sync_inventory, sync_catalog, sync_customers, push_orders_since, publish_catalog)
		VALUES (TRUE, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (id) DO UPDATE SET
			enabled = EXCLUDED.enabled, access_token_enc = EXCLUDED.access_token_enc,
			account_id = EXCLUDED.account_id, account_name = EXCLUDED.account_name,
			location_id = EXCLUDED.location_id, location_name = EXCLUDED.location_name,
			catalog_id = EXCLUDED.catalog_id, catalog_name = EXCLUDED.catalog_name,
			customer_list_id = EXCLUDED.customer_list_id, customer_list_name = EXCLUDED.customer_list_name,
			scope = EXCLUDED.scope, push_orders = EXCLUDED.push_orders, sync_status = EXCLUDED.sync_status,
			sync_inventory = EXCLUDED.sync_inventory, sync_catalog = EXCLUDED.sync_catalog,
			sync_customers = EXCLUDED.sync_customers, push_orders_since = EXCLUDED.push_orders_since,
			publish_catalog = EXCLUDED.publish_catalog,
			auth_error_at = NULL, relink_required_at = NULL, updated_at = now()`,
		s.enabled, enc, DefaultAccountID, DefaultAccountName, s.locationID, DefaultLocationName,
		s.catalogID, DefaultCatalogName, nullable(s.customerListID), nullable(s.customerListName), s.scope,
		s.pushOrders, s.syncStatus, s.syncInventory, s.syncCatalog, s.syncCustomers, s.pushSince, s.publish)
	if err != nil {
		tb.Fatalf("testhelpers: seed connection: %v", err)
	}
	return Seeded{Token: s.token, LocationID: s.locationID, CatalogID: s.catalogID}
}

// nullable stores an empty string as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// FakeClock is a manually advanced clock, safe for concurrent use. It implements domain.Clock.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

var _ domain.Clock = (*FakeClock)(nil)

// NewFakeClock returns a clock standing at start.
func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set moves the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
