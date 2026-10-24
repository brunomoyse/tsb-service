// Package hubrisewire is the composition root of the HubRise integration: it builds every part of
// the module from one place, plugs it into the resolver, the order service and the user service
// through the hooks and observers they expose, mounts the two REST endpoints and runs the
// background workers. The main program calls Install once, and only when HubRise is configured on
// the deployment; without it none of this code runs and the shop is exactly the standalone shop.
//
// It lives in a package of its own (not in cmd/app) so that the end-to-end tests wire the
// integration the way production does.
package hubrisewire

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/interfaces"
	"tsb-service/internal/shared/audit"
	"tsb-service/internal/shared/middleware"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// Options are the inputs of Install. The embedded option structs are the tuning knobs of the parts
// (worker cadences, limits, test doubles); Install fills in what only the composition root knows.
type Options struct {
	// Module is the HubRise module; nil means HubRise is not available and Install does nothing.
	Module *hubrise.Module
	// Resolver is the GraphQL resolver the integration follows. Its services are the ones the hooks
	// and observers are installed on.
	Resolver *resolver.Resolver
	// APIBaseURL is the public base URL of this API (API_BASE_URL, with /api/v1): the callback and
	// the OAuth redirect URLs given to HubRise derive from it.
	APIBaseURL string
	// DashboardURL is where the browser lands after the OAuth round trip, with ?hubrise=<outcome>
	// (the HubRise page of the dashboard).
	DashboardURL string
	// Audit records the connect and disconnect entries; nil records nothing.
	Audit *audit.Recorder
	// Alerter emails the owner when an order stays new at HubRise; nil sends no mail (the flag on
	// the order is raised anyway).
	Alerter application.Alerter
	// Images stores imported product images; the default sends them to the file service.
	Images application.ImageUploader
	// Clients overrides the provider of the API clients (tests point it at the mock). The default
	// opens the stored token against the configured API with a rate limiter shared by every part of
	// this process and sized for Config.Replicas.
	Clients application.ClientProvider
	// OAuthHTTP is the HTTP client of the authorization server calls (tests).
	OAuthHTTP *http.Client
	// Holder identifies this process in the lease tables; a random one by default.
	Holder string

	Push      hubrise.PushOptions
	Sync      hubrise.SyncOptions
	Inventory hubrise.InventoryOptions
	Catalog   hubrise.CatalogOptions
}

// Runtime is the installed integration.
type Runtime struct {
	Module    *hubrise.Module
	Push      *hubrise.Push
	Sync      *hubrise.Sync
	Catalog   *hubrise.Catalog
	Inventory *hubrise.Inventory
	Admin     *application.Admin
	Hooks     resolver.HubriseHooksSet

	callback *interfaces.CallbackHandler
	oauth    *interfaces.OAuthHandler
	limiters []*middleware.RateLimiter
}

// failureRate is the allowance of unverifiable requests of the public endpoints: 30 a minute per
// client address. A request with a valid signature never counts against it.
const failureRate, failureBurst = 30.0 / 60, 10

// Install builds the integration and connects it to the shop. It returns (nil, nil) when HubRise is
// not available (nil Module): nothing is touched then. Otherwise it sets Resolver.Hubrise and
// Resolver.HubriseAdmin, installs the status observer on the order service and the profile observer
// on the user service. Nothing is started and no route is mounted: the caller calls Register and
// runs Run.
func Install(o Options) (*Runtime, error) {
	if o.Module == nil {
		return nil, nil // not configured: standalone
	}
	m, r := o.Module, o.Resolver
	holder := cmp.Or(o.Holder, uuid.NewString())

	clients := o.Clients
	if clients == nil {
		limiter := pkghubrise.NewLimiter(pkghubrise.DefaultLimits().Split(m.Config.Replicas))
		clients = application.WithAuthRecording(application.NewPooledClients(m.Codec, m.Config.APIBaseURL, nil, limiter), m.Connections, m.Clock)
	}

	push := o.Push
	push.Clients, push.Holder = clients, holder
	if r.RestaurantService != nil {
		push.Schedule = orderingHours{r.RestaurantService}
	}
	push.Alerter = o.Alerter
	pushed := m.NewPush(push)

	sync := o.Sync
	sync.Clients, sync.Holder = clients, holder
	sync.Transitioner = resolver.HubriseOrderTransitioner{Resolver: r}
	inbound := m.NewSync(pushed, sync)

	cat := o.Catalog
	cat.Clients, cat.Holder, cat.Notifier = clients, holder, r
	cat.Audit = auditFunc(o.Audit)
	if o.Images != nil {
		cat.Images = o.Images
	} else {
		cat.Images = application.UtilsImageUploader{}
	}
	catalog := m.NewCatalog(pushed, cat)

	inv := o.Inventory
	inv.Clients, inv.Holder, inv.Notifier, inv.Drift, inv.Publish = clients, holder, r, catalog.Drift, catalog.Publisher
	inventory := m.NewInventory(pushed, inv)
	inventory.Attach(inbound)

	admin, err := m.NewAdmin(pushed, catalog, hubrise.AdminOptions{
		Clients: clients, OAuthHTTP: o.OAuthHTTP, APIBaseURL: o.APIBaseURL, Inventory: inventory.Service,
		Audit: auditFunc(o.Audit),
	})
	if err != nil {
		return nil, err
	}

	rt := &Runtime{
		Module: m, Push: pushed, Sync: inbound, Catalog: catalog, Inventory: inventory, Admin: admin,
		Hooks: resolver.HubriseHooksSet{
			Order:     resolver.HubriseOrderHooks{Push: pushed.Pusher},
			Inventory: resolver.HubriseInventoryHooks{Inventory: inventory.Service},
			Catalog:   resolver.CatalogGuardHooks{Guard: catalog.Guard},
			Publish:   resolver.HubrisePublishHooks{Publisher: catalog.Publisher},
		},
	}
	callbackFailures := middleware.NewRateLimiter(failureRate, failureBurst)
	oauthFailures := middleware.NewRateLimiter(failureRate, failureBurst)
	rt.limiters = []*middleware.RateLimiter{callbackFailures, oauthFailures}
	rt.callback = interfaces.NewCallbackHandler(inbound.Receiver, callbackFailures)
	rt.oauth = interfaces.NewOAuthHandler(admin, o.DashboardURL, oauthFailures)

	r.Hubrise = rt.Hooks
	r.HubriseAdmin = admin
	r.OrderService.SetStatusObserver(rt.Hooks)
	r.ProductService.SetMenuObserver(rt.Hooks)
	r.UserService.SetProfileObserver(pushed.Customers)
	return rt, nil
}

// auditFunc adapts the staff audit log to the connection management's audit port.
func auditFunc(rec *audit.Recorder) application.AuditFunc {
	if rec == nil {
		return nil
	}
	return func(ctx context.Context, action string, success bool, details map[string]any) {
		rec.Record(ctx, audit.Entry{Action: action, Success: success, Variables: details})
	}
}

// Register mounts the two public REST endpoints on the API group: the callback HubRise posts its
// events to (POST /hubrise/callback, HMAC authenticated) and the OAuth return
// (GET /hubrise/oauth/callback, authenticated by the one-time state).
func (rt *Runtime) Register(api gin.IRoutes) {
	rt.callback.Register(api)
	rt.oauth.Register(api)
}

// restartDelay is how long a worker that panicked waits before it starts again.
const restartDelay = 5 * time.Second

// Run runs every background worker until ctx ends and returns when all of them have stopped: the
// outbox drain, the reconciler, the callback event processor, the callback poller, the catalog
// publisher (the debounced publish of the dashboard's menu and its local-hash scan), and the
// inventory side (event processor, 30 minute read, sold-out sweeper, catalog drift check). The
// singleton jobs take their lease in the database, so every replica runs the same set. The workers
// run as the system (admin context): the HubRise tables and the shop tables they touch are reached
// through the admin pool.
func (rt *Runtime) Run(ctx context.Context) {
	runAll(utils.SetIsAdmin(ctx, true), restartDelay, rt.workers())
}

// workers are the background workers by name, the names the crash log carries.
func (rt *Runtime) workers() map[string]func(context.Context) {
	return map[string]func(context.Context){
		"outbox":     rt.Push.Worker.Run,
		"reconciler": rt.Push.Reconciler.Run,
		"events":     rt.Sync.Processor.Run,
		"poller":     rt.Sync.Poller.Run,
		"inventory":  rt.Inventory.Run,
		"publisher":  rt.Catalog.Publisher.Run,
	}
}

// runAll runs every worker under supervise until ctx ends and returns when all of them have stopped.
func runAll(ctx context.Context, delay time.Duration, workers map[string]func(context.Context)) {
	var wg sync.WaitGroup
	for name, run := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervise(ctx, name, delay, run)
		}()
	}
	wg.Wait()
}

// Stop releases what Install allocated (the limiters of the public endpoints). Call it once the
// HTTP server and Run are done.
func (rt *Runtime) Stop() {
	for _, l := range rt.limiters {
		l.Stop()
	}
}

// supervise runs one worker until ctx ends. A panic is logged (and reported) instead of taking the
// process down, since the integration must never be able to stop the shop, and the worker starts
// again after delay.
func supervise(ctx context.Context, name string, delay time.Duration, run func(context.Context)) {
	for ctx.Err() == nil {
		if err := safely(run, ctx); err != nil {
			logging.FromContext(ctx).Error("hubrise: worker crashed, restarting",
				zap.String("worker", name), zap.Duration("delay", delay), zap.Error(err))
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
			continue
		}
		return // the worker returned: it only does when ctx ended
	}
}

// safely calls run and turns a panic into an error.
func safely(run func(context.Context), ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			sentry.CurrentHub().Recover(p)
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	run(ctx)
	return nil
}
