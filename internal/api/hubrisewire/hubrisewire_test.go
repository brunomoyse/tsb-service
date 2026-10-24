package hubrisewire

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/modules/hubrise"
	hubDomain "tsb-service/internal/modules/hubrise/domain"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	orderApplication "tsb-service/internal/modules/order/application"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/internal/shared/audit"
)

// The services the wiring installs observers on, without a database: only the installation is
// observed.
type orderServiceSpy struct {
	orderApplication.OrderService
	observer orderApplication.StatusObserver
}

func (s *orderServiceSpy) SetStatusObserver(o orderApplication.StatusObserver) { s.observer = o }

type userServiceSpy struct {
	userApplication.UserService
	observer userApplication.ProfileObserver
}

func (s *userServiceSpy) SetProfileObserver(o userApplication.ProfileObserver) { s.observer = o }

type productServiceSpy struct {
	productApplication.ProductService
	observer productApplication.MenuObserver
}

func (s *productServiceSpy) SetMenuObserver(o productApplication.MenuObserver) { s.observer = o }

// noImages is an image uploader the caller supplies instead of the file service.
type noImages struct{}

func (noImages) UploadProductImage(context.Context, uuid.UUID, string, []byte) error { return nil }

func testModule(t *testing.T) *hubrise.Module {
	t.Helper()
	cfg := hubrise.Config{
		ClientID: "id", ClientSecret: hubDomain.Redacted("secret"), TokenKey: hubtest.TestTokenKey,
		APIBaseURL: hubrise.DefaultAPIBaseURL, OAuthBaseURL: hubrise.DefaultOAuthBaseURL, Replicas: 2,
	}
	mod, err := hubrise.NewModule(cfg, &sqlx.DB{}, nil)
	require.NoError(t, err)
	require.NotNil(t, mod)
	return mod
}

func testResolver() (*resolver.Resolver, *orderServiceSpy, *userServiceSpy) {
	r, orders, users, _ := testResolverWithProducts()
	return r, orders, users
}

func testResolverWithProducts() (*resolver.Resolver, *orderServiceSpy, *userServiceSpy, *productServiceSpy) {
	orders, users, products := &orderServiceSpy{}, &userServiceSpy{}, &productServiceSpy{}
	return &resolver.Resolver{OrderService: orders, UserService: users, ProductService: products}, orders, users, products
}

func TestInstallWithoutAModuleDoesNothing(t *testing.T) {
	r, orders, users := testResolver()
	rt, err := Install(Options{Resolver: r})
	require.NoError(t, err)
	assert.Nil(t, rt)
	assert.Nil(t, r.Hubrise)
	assert.Nil(t, r.HubriseAdmin)
	assert.Nil(t, orders.observer)
	assert.Nil(t, users.observer)
}

func TestInstallConnectsTheIntegrationToTheShop(t *testing.T) {
	r, orders, users, products := testResolverWithProducts()
	rt, err := Install(Options{
		Module: testModule(t), Resolver: r, APIBaseURL: "https://shop.example.test/api/v1", DashboardURL: "https://dash.example.test",
		Audit: audit.NewRecorder(nil),
	})
	require.NoError(t, err)
	require.NotNil(t, rt)
	t.Cleanup(rt.Stop)

	assert.NotNil(t, r.Hubrise, "the hooks")
	assert.Equal(t, rt.Hooks, r.Hubrise)
	assert.Same(t, rt.Admin, r.HubriseAdmin)
	assert.Equal(t, rt.Hooks, orders.observer, "the status observer of the order service")
	assert.Same(t, rt.Push.Customers, users.observer, "the profile observer of the user service")
	assert.Equal(t, rt.Hooks, products.observer, "the menu observer of the product service")
	assert.NotNil(t, rt.Catalog.Publisher)

	router := gin.New()
	rt.Register(router.Group("/api/v1"))
	var routes []string
	for _, route := range router.Routes() {
		routes = append(routes, route.Method+" "+route.Path)
	}
	assert.ElementsMatch(t, []string{"POST /api/v1/hubrise/callback", "GET /api/v1/hubrise/oauth/callback"}, routes)
}

func TestInstallWithAnUnusableAPIAddressChangesNothing(t *testing.T) {
	r, orders, users := testResolver()
	rt, err := Install(Options{Module: testModule(t), Resolver: r, APIBaseURL: "not a url", Images: noImages{}})
	require.ErrorContains(t, err, "API_BASE_URL")
	assert.Nil(t, rt)
	assert.Nil(t, r.Hubrise)
	assert.Nil(t, r.HubriseAdmin)
	assert.Nil(t, orders.observer)
	assert.Nil(t, users.observer)
}

func TestAuditFuncIsNilWithoutARecorder(t *testing.T) {
	assert.Nil(t, auditFunc(nil))
	// With a recorder, the entry goes to it (this one has no database and drops it).
	assert.NotPanics(t, func() {
		auditFunc(audit.NewRecorder(nil))(t.Context(), "hubrise.connect", true, map[string]any{"a": 1})
	})
}

func TestSuperviseRestartsAWorkerThatPanics(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervise(ctx, "test", time.Millisecond, func(ctx context.Context) {
			if runs.Add(1) < 3 {
				panic("boom")
			}
			<-ctx.Done() // the third run is healthy and ends with the context
		})
	}()
	require.Eventually(t, func() bool { return runs.Load() == 3 }, 5*time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not return")
	}
	assert.EqualValues(t, 3, runs.Load())
}

func TestSuperviseStopsWhileWaitingToRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervise(ctx, "test", time.Hour, func(context.Context) {
			runs.Add(1)
			panic("boom")
		})
	}()
	require.Eventually(t, func() bool { return runs.Load() == 1 }, 5*time.Second, time.Millisecond)
	cancel() // the hour of waiting is cut short by the shutdown
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not return")
	}
	assert.EqualValues(t, 1, runs.Load())
}

func TestSuperviseDoesNotRunAfterTheContextEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	supervise(ctx, "test", time.Hour, func(context.Context) { t.Error("must not run") })
}

func TestSafelyReportsAPanic(t *testing.T) {
	assert.NoError(t, safely(func(context.Context) {}, t.Context()))
	err := safely(func(context.Context) { panic("bad") }, t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestRunStartsEveryRegisteredWorker(t *testing.T) {
	rt, err := Install(Options{Module: testModule(t), Resolver: func() *resolver.Resolver { r, _, _ := testResolver(); return r }(),
		APIBaseURL: "https://shop.example.test/api/v1", Images: noImages{}})
	require.NoError(t, err)
	require.NotNil(t, rt)
	t.Cleanup(rt.Stop)

	workers := rt.workers()
	names := make([]string, 0, len(workers))
	for name, run := range workers {
		assert.NotNil(t, run, name)
		names = append(names, name)
	}
	assert.ElementsMatch(t, []string{"outbox", "reconciler", "events", "poller", "inventory", "publisher"}, names,
		"the outbox drain, the reconciler, the callback event processor, the callback poller, the inventory side and the catalog publisher")
}

func TestRunAllStartsEachWorkerAndWaitsForAllOfThem(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{}), "c": make(chan struct{})}
	workers := map[string]func(context.Context){}
	var crashed atomic.Bool
	for name, ch := range started {
		workers[name] = func(ctx context.Context) {
			if name == "c" && !crashed.Swap(true) {
				panic("boom") // supervised: it starts again after the delay
			}
			close(ch)
			<-ctx.Done()
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAll(ctx, time.Millisecond, workers)
	}()
	for name, ch := range started {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("worker %s was never started", name)
		}
	}
	select {
	case <-done:
		t.Fatal("runAll returned while its workers ran")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runAll did not return after the context ended")
	}
}
