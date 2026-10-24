// Package hubrise is the composition root of the HubRise integration: it reads the deployment
// configuration and builds the storage layer. The application services (connection, catalog,
// outbox, events...) are added to the Module by later tasks. When HubRise is not configured the
// constructor returns nil and nothing else of the integration runs.
package hubrise

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure"
	"tsb-service/internal/modules/hubrise/infrastructure/tokencrypt"
	"tsb-service/pkg/hubrise"
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
	Source       domain.OrderSource
	Availability domain.AvailabilityStore
	// CustomerLinks and Customers link shop users to their HubRise customers and read their profiles.
	CustomerLinks domain.CustomerLinkRepository
	Customers     domain.CustomerSource
	// Local reads and writes the shop's own menu for the catalog import.
	Local domain.LocalCatalogRepository
	// PublishState is the row of the catalog publisher, PublishItems what its last publish contained.
	PublishState domain.PublishStateRepository
	PublishItems domain.PublishedItemRepository
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
		Source:       infrastructure.NewOrderSource(adminDB),
		Availability: infrastructure.NewAvailabilityStore(adminDB),

		CustomerLinks: infrastructure.NewCustomerLinkRepository(adminDB),
		Customers:     infrastructure.NewCustomerSource(adminDB),
		Local:         infrastructure.NewLocalCatalogRepository(adminDB),
		PublishState:  infrastructure.NewPublishStateRepository(adminDB),
		PublishItems:  infrastructure.NewPublishedItemRepository(adminDB),
	}, nil
}

// Enabled reports whether the module exists (HubRise is available on this deployment). It is safe
// on a nil Module.
func (m *Module) Enabled() bool { return m != nil }

// PushOptions customizes NewPush; every field is optional.
type PushOptions struct {
	// Clients builds the API clients; the default opens the stored token against Config.APIBaseURL
	// with the default rate limits. Production builds the limiter from Limits.Split(replicas).
	Clients    application.ClientProvider
	Worker     application.WorkerConfig
	Reconciler application.ReconcilerConfig
	// Customer tunes the customer list sync; Holder identifies this process in its lease table (a
	// random one by default).
	Customer application.CustomerConfig
	Holder   string
	// Schedule, Alerter and Remote are the optional ports of the reconciler.
	Schedule application.Schedule
	Alerter  application.Alerter
	Remote   application.RemoteOrderHandler
}

// Push is the order push: the hooks that queue orders and status changes, the outbox worker that
// sends them, and the reconciler that backs them up.
type Push struct {
	Pusher     *application.OrderPusher
	Worker     *application.OutboxWorker
	Reconciler *application.Reconciler
	// Customers keeps the HubRise customer list in step with the shop's users. Its hooks
	// (ProfileChanged, AccountDeleted) are the profile observer of the user service; the worker
	// sends what they queue and upserts the customer of every order it pushes.
	Customers *application.CustomerSync
	// Clients builds the API clients; the status sync reuses the same provider (and so the same
	// rate limiter).
	Clients application.ClientProvider
}

// NewPush builds the order push on the module's repositories. The worker is not started: the caller
// runs Worker.Run and Reconciler.Run (the composition root does, once, when the module exists).
func (m *Module) NewPush(o PushOptions) *Push {
	clients := o.Clients
	if clients == nil {
		clients = application.WithAuthRecording(application.NewPooledClients(m.Codec, m.Config.APIBaseURL, nil, nil), m.Connections, m.Clock)
	}
	pusher := application.NewOrderPusher(application.PusherDeps{
		Connections: m.Connections, Orders: m.Orders, Outbox: m.Outbox, Source: m.Source, Clock: m.Clock,
	})
	customers := application.NewCustomerSync(application.CustomerDeps{
		Connections: m.Connections, Links: m.CustomerLinks, Source: m.Customers, Outbox: m.Outbox,
		Leases: m.Leases, Clock: m.Clock, Holder: o.Holder, Config: o.Customer,
	})
	worker := application.NewOutboxWorker(application.WorkerDeps{
		Connections: m.Connections, Outbox: m.Outbox, Orders: m.Orders, Links: m.CatalogLinks,
		Source: m.Source, Availability: m.Availability, Customers: customers, Clients: clients, Clock: m.Clock, Config: o.Worker,
	})
	pusher.SetWake(worker.Wake)
	customers.SetWake(worker.Wake)
	reconciler := application.NewReconciler(application.ReconcilerDeps{
		Connections: m.Connections, Orders: m.Orders, Source: m.Source, Leases: m.Leases, Events: m.Events, Outbox: m.Outbox,
		Foreign: m.Foreign, OAuthStates: m.OAuthStates, Clients: clients, Pusher: pusher, Clock: m.Clock,
		Config: o.Reconciler, Schedule: o.Schedule, Alerter: o.Alerter, Remote: o.Remote, Customers: customers,
	})
	return &Push{Pusher: pusher, Worker: worker, Reconciler: reconciler, Customers: customers, Clients: clients}
}

// SyncOptions customizes NewSync.
type SyncOptions struct {
	// Transitioner applies a remote change to the local order (required). The resolver package
	// implements it on top of ApplyOrderUpdate.
	Transitioner application.OrderTransitioner
	// Clients overrides the provider of the push.
	Clients   application.ClientProvider
	Processor application.EventProcessorConfig
	Poller    application.CallbackPollerConfig
	// Holder identifies this process in the lease table (a random one by default).
	Holder string
}

// Sync is the inbound side: the callback receiver the HTTP handler calls, the event processor, the
// status sync shared with the reconciler, and the poller that recovers lost events.
type Sync struct {
	Receiver  *application.CallbackReceiver
	Processor *application.EventProcessor
	Status    *application.StatusSync
	Poller    *application.CallbackPoller
}

// NewSync builds the inbound side on the module's repositories and plugs the status sync into the
// reconciler of push, which must not be nil. Nothing is started: the caller runs Processor.Run and
// Poller.Run, and serves the callback endpoint.
func (m *Module) NewSync(push *Push, o SyncOptions) *Sync {
	clients := o.Clients
	if clients == nil {
		clients = push.Clients
	}
	status := application.NewStatusSync(application.StatusSyncDeps{
		Connections: m.Connections, Orders: m.Orders, Source: m.Source, Leases: m.Leases,
		Transitioner: o.Transitioner, Clock: m.Clock, Holder: o.Holder,
	})
	processor := application.NewEventProcessor(application.EventProcessorDeps{
		Connections: m.Connections, Orders: m.Orders, Events: m.Events, Foreign: m.Foreign,
		Clients: clients, Sync: status, Clock: m.Clock, Config: o.Processor,
	})
	pcfg := o.Poller
	if pcfg.Holder == "" {
		pcfg.Holder = o.Holder
	}
	poller := application.NewCallbackPoller(application.CallbackPollerDeps{
		Connections: m.Connections, Events: m.Events, Leases: m.Leases, Clients: clients, Clock: m.Clock,
		Config: pcfg, Wake: []func(){processor.Wake},
	})
	push.Reconciler.SetRemote(status)
	return &Sync{
		Receiver:  application.NewCallbackReceiver(string(m.Config.ClientSecret), m.Connections, m.Events, m.Clock, processor.Wake),
		Processor: processor, Status: status, Poller: poller,
	}
}

// InventoryOptions customizes NewInventory; every field is optional.
type InventoryOptions struct {
	// Clients overrides the provider of the push.
	Clients application.ClientProvider
	// Notifier publishes productUpdated when inventory changes a product (the resolver).
	Notifier application.ProductNotifier
	// Drift is the catalog service seen by the drift job; without it no drift job runs.
	Drift application.CatalogDriftChecker
	// Publish is the catalog publisher seen by the drift job: while the dashboard owns the menu,
	// catalog events only record that HubRise was edited elsewhere.
	Publish application.PublishWatcher
	// Holder identifies this process in the lease table (a random one by default).
	Holder string

	Service application.InventoryConfig
	Events  application.InventoryEventsConfig
	Jobs    application.InventoryJobsConfig
	Catalog application.CatalogDriftConfig
}

// Inventory is the sold-out sync: the service (inbound sync, baseline, expiry, and the outbound hook
// the resolver calls), the processor of inventory callback events, the periodic jobs, and the catalog
// drift job.
type Inventory struct {
	Service *application.InventoryService
	Events  *application.InventoryEvents
	Jobs    *application.InventoryJobs
	// Drift is nil without a catalog service.
	Drift *application.CatalogDrift
}

// NewInventory builds the inventory sync on the module's repositories. The outbox worker of push sends
// the inventory rows (it is built with the availability store), so a row queued by the hook is
// drained at once. Nothing is started: the caller runs Run, and Attach wakes the event processor when
// a callback arrives.
func (m *Module) NewInventory(push *Push, o InventoryOptions) *Inventory {
	clients := o.Clients
	if clients == nil {
		clients = push.Clients
	}
	holder := cmp.Or(o.Holder, uuid.NewString())
	scfg, jcfg, ccfg := o.Service, o.Jobs, o.Catalog
	scfg.Holder, jcfg.Holder, ccfg.Holder = holder, holder, holder
	svc := application.NewInventoryService(application.InventoryDeps{
		Connections: m.Connections, Links: m.CatalogLinks, Outbox: m.Outbox, Leases: m.Leases, Store: m.Availability,
		Clients: clients, Clock: m.Clock, Config: scfg, Notifier: o.Notifier, Wake: push.Worker.Wake,
	})
	inv := &Inventory{
		Service: svc,
		Events: application.NewInventoryEvents(application.InventoryEventsDeps{
			Connections: m.Connections, Events: m.Events, Inventory: svc, Clock: m.Clock, Config: o.Events,
		}),
		Jobs: application.NewInventoryJobs(application.InventoryJobsDeps{
			Inventory: svc, Leases: m.Leases, Clock: m.Clock, Config: jcfg,
		}),
	}
	if o.Drift != nil {
		inv.Drift = application.NewCatalogDrift(application.CatalogDriftDeps{
			Connections: m.Connections, Events: m.Events, Leases: m.Leases, Checker: o.Drift, Publish: o.Publish, Clock: m.Clock, Config: ccfg,
		})
	}
	return inv
}

// Attach makes the callback receiver and the poller of inbound wake the inventory event processor when
// they store events.
func (i *Inventory) Attach(inbound *Sync) {
	inbound.Receiver.AddWake(i.Events.Wake)
	inbound.Poller.AddWake(i.Events.Wake)
}

// Run runs the inventory event processor, the periodic jobs and, when there is one, the drift job,
// until ctx ends.
func (i *Inventory) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, run := range []func(context.Context){i.Events.Run, i.Jobs.Run} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(ctx)
		}()
	}
	if i.Drift != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i.Drift.Run(ctx)
		}()
	}
	wg.Wait()
}

// CatalogOptions customizes NewCatalog; every field is optional.
type CatalogOptions struct {
	// Clients overrides the provider of the push.
	Clients application.ClientProvider
	// Notifier publishes productUpdated for the products an import changed (the resolver).
	Notifier application.CatalogNotifier
	// Images stores imported product images; without it nothing is downloaded.
	Images application.ImageUploader
	// Holder identifies this process in the import lease (a random one by default).
	Holder string
	// Audit records the catalog publishes (counts only).
	Audit application.AuditFunc
	// ProductImages reads the product images for publish_images; by default the public product
	// bucket named by the configuration (Config.ImageBaseURL), and without one the images cannot be
	// published.
	ProductImages application.ProductImageSource
}

// Catalog is the catalog side of the integration: the import service and the ownership guard that
// refuses edits of the fields HubRise owns.
type Catalog struct {
	Service *application.CatalogService
	Guard   *application.Guard
	// Drift is the catalog service as the drift job's port (see InventoryOptions.Drift).
	Drift application.CatalogDriftChecker
	// Publisher publishes the dashboard's menu to the HubRise catalog; Run drives it.
	Publisher *application.CatalogPublisher
}

// NewCatalog builds the catalog import on the module's repositories.
func (m *Module) NewCatalog(push *Push, o CatalogOptions) *Catalog {
	clients := o.Clients
	if clients == nil {
		clients = push.Clients
	}
	svc := &application.CatalogService{
		Connections: m.Connections, Links: m.CatalogLinks, Leases: m.Leases, Local: m.Local, Clock: m.Clock,
		NewClient: func(conn *domain.Connection) (application.CatalogAPI, error) {
			c, err := clients.Client(context.Background(), conn)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Notifier: o.Notifier, Images: o.Images, Holder: cmp.Or(o.Holder, uuid.NewString()),
		PublishItems: m.PublishItems, PublishState: m.PublishState,
	}
	publisher := &application.CatalogPublisher{
		Connections: m.Connections, Links: m.CatalogLinks, State: m.PublishState, Items: m.PublishItems, Local: m.Local,
		Clock: m.Clock, Leases: m.Leases,
		NewClient: func(conn *domain.Connection) (application.PublishAPI, error) {
			c, err := clients.Client(context.Background(), conn)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Notifier: o.Notifier, Audit: o.Audit, Holder: svc.Holder,
		ImageSource: o.ProductImages, ImageStamps: application.NewImageStamps(),
	}
	if publisher.ImageSource == nil && m.Config.ImageBaseURL != "" {
		publisher.ImageSource = application.HTTPProductImages{BaseURL: m.Config.ImageBaseURL}
	}
	svc.Publish = publisher
	return &Catalog{Service: svc, Guard: &application.Guard{Connections: m.Connections, Links: m.CatalogLinks}, Drift: svc.DriftChecker(), Publisher: publisher}
}

// AdminOptions customizes NewAdmin.
type AdminOptions struct {
	// Clients overrides the provider of the push.
	Clients application.ClientProvider
	// TokenClient opens a client for a freshly issued token; the default talks to Config.APIBaseURL.
	TokenClient func(token string) (*hubrise.Client, error)
	// OAuthHTTP is the HTTP client of the authorization server calls (tests point it at the mock).
	OAuthHTTP *http.Client
	// APIBaseURL is the public base URL of this API (API_BASE_URL): the callback and the OAuth
	// redirect URLs are derived from it.
	APIBaseURL string
	// Inventory is the inventory service, when the sold-out sync is wired.
	Inventory *application.InventoryService
	// Audit records the connect and disconnect entries.
	Audit application.AuditFunc
}

// NewAdmin builds the connection management: the OAuth connection, the settings, the disconnection
// and the operations behind the GraphQL admin API.
func (m *Module) NewAdmin(push *Push, catalog *Catalog, o AdminOptions) (*application.Admin, error) {
	callback, redirect, err := application.CallbackURLFor(o.APIBaseURL)
	if err != nil {
		return nil, err
	}
	var oauthOpts []hubrise.OAuthOption
	if o.OAuthHTTP != nil {
		oauthOpts = append(oauthOpts, hubrise.WithOAuthHTTPClient(o.OAuthHTTP))
	}
	oauth, err := hubrise.NewOAuth(m.Config.OAuthBaseURL, m.Config.ClientID, string(m.Config.ClientSecret), oauthOpts...)
	if err != nil {
		return nil, err
	}
	clients := o.Clients
	if clients == nil {
		clients = push.Clients
	}
	tokenClient := o.TokenClient
	if tokenClient == nil {
		tokenClient = func(token string) (*hubrise.Client, error) { return hubrise.New(m.Config.APIBaseURL, token) }
	}
	return application.NewAdmin(application.AdminDeps{
		Config: application.AdminConfig{RedirectURL: redirect, CallbackURL: callback},
		OAuth:  oauth, Connections: m.Connections, OAuthStates: m.OAuthStates, Links: m.CatalogLinks, Orders: m.Orders,
		Outbox: m.Outbox, Codec: m.Codec, Clients: clients, TokenClient: tokenClient, Catalog: catalog.Service,
		Inventory: o.Inventory, Publisher: catalog.Publisher, Clock: m.Clock, Audit: o.Audit, Wake: push.Worker.Wake,
	}), nil
}
