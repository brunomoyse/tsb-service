package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/time/rate"

	orderApplication "tsb-service/internal/modules/order/application"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	productApplication "tsb-service/internal/modules/product/application"
	productDomain "tsb-service/internal/modules/product/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

func serve(h http.Handler, method, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLanguageExtractor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got string
	r := gin.New()
	r.Use(LanguageExtractor())
	r.GET("/", func(c *gin.Context) { got = utils.GetLang(c.Request.Context()); c.Status(http.StatusNoContent) })

	cases := []struct{ header, want string }{
		{"fr", "fr"},
		{"fr-BE,fr;q=0.9,en;q=0.8", "fr"},
		{"nl-BE", "nl"},
		{"zh-CN,zh;q=0.9", "zh"},
		{"en-GB", "en"},
		{"en;q=0.5, nl;q=0.9", "nl"},
		{"de-DE,de;q=0.9", "en"},
		{"", "en"},
		{"*", "en"},
		{"fr;q=abc, nl;q=0.5", "fr"},
		{"nl;q=0.2, fr;q=0.8, en;q=0.5", "fr"},
		{"de, fr;q=0.1", "fr"},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			serve(r, http.MethodGet, "/", func(req *http.Request) {
				if tc.header != "" {
					req.Header.Set("Accept-Language", tc.header)
				}
			})
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var inHandler string
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/", func(c *gin.Context) {
		inHandler = logging.GetRequestID(c.Request.Context())
		c.Status(http.StatusNoContent)
	})

	rec := serve(r, http.MethodGet, "/", nil)
	header := rec.Header().Get("X-Request-ID")
	assert.Len(t, header, 16)
	assert.Equal(t, header, inHandler, "the handler's context carries the id sent in the response header")

	rec2 := serve(r, http.MethodGet, "/", nil)
	assert.NotEqual(t, header, rec2.Header().Get("X-Request-ID"), "every request gets its own id")
}

func TestRateLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl := NewRateLimiter(rate.Every(time.Hour), 2) // a burst of 2, no refill within the test
	t.Cleanup(rl.Stop)
	r := gin.New()
	r.Use(rl.Middleware())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	from := func(ip string) func(*http.Request) { return func(req *http.Request) { req.RemoteAddr = ip + ":1234" } }

	assert.Equal(t, http.StatusNoContent, serve(r, http.MethodGet, "/", from("10.0.0.1")).Code)
	assert.Equal(t, http.StatusNoContent, serve(r, http.MethodGet, "/", from("10.0.0.1")).Code)
	blocked := serve(r, http.MethodGet, "/", from("10.0.0.1"))
	assert.Equal(t, http.StatusTooManyRequests, blocked.Code)
	assert.JSONEq(t, `{"error":"too many requests"}`, blocked.Body.String())

	assert.Equal(t, http.StatusNoContent, serve(r, http.MethodGet, "/", from("10.0.0.2")).Code, "another client has its own budget")
}

func TestRateLimiterAllowKeyAndSweep(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Hour), 1)
	t.Cleanup(rl.Stop)

	assert.True(t, rl.AllowKey("user-1"))
	assert.False(t, rl.AllowKey("user-1"), "the key's budget is spent")
	assert.True(t, rl.AllowKey("user-2"))

	now := time.Now()
	rl.sweep(now.Add(visitorTTL - time.Second))
	assert.Len(t, rl.visitors, 2, "recently seen visitors are kept")
	assert.False(t, rl.AllowKey("user-1"), "and keep their spent budget")

	rl.sweep(now.Add(visitorTTL + time.Minute))
	assert.Empty(t, rl.visitors, "idle visitors are forgotten")
	assert.True(t, rl.AllowKey("user-1"), "a forgotten key starts with a fresh budget")
}

func metricValue(t *testing.T, name string, labels map[string]string) (*dto.Metric, bool) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metric:
		for _, m := range f.GetMetric() {
			for k, v := range labels {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == k && l.GetValue() == v {
						found = true
					}
				}
				if !found {
					continue metric
				}
			}
			return m, true
		}
	}
	return nil, false
}

func TestPrometheusMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PrometheusMetrics())
	r.GET("/orders/:id", func(c *gin.Context) { c.Status(http.StatusCreated) })
	r.GET("/boom", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	r.GET("/metrics", func(c *gin.Context) { c.Status(http.StatusOK) })

	count := func(method, route, status string) float64 {
		m, ok := metricValue(t, "http_requests_total", map[string]string{"method": method, "route": route, "status": status})
		if !ok {
			return 0
		}
		return m.GetCounter().GetValue()
	}
	beforeOrders, beforeBoom := count("GET", "/orders/:id", "201"), count("GET", "/boom", "500")
	beforeUnmatched, beforeMetrics := count("GET", "unmatched", "404"), count("GET", "/metrics", "200")

	serve(r, http.MethodGet, "/orders/1", nil)
	serve(r, http.MethodGet, "/orders/2", nil)
	serve(r, http.MethodGet, "/boom", nil)
	serve(r, http.MethodGet, "/nowhere", nil)
	serve(r, http.MethodGet, "/metrics", nil)

	assert.Equal(t, beforeOrders+2, count("GET", "/orders/:id", "201"), "the route template is the label, so ids do not explode cardinality")
	assert.Equal(t, beforeBoom+1, count("GET", "/boom", "500"))
	assert.Equal(t, beforeUnmatched+1, count("GET", "unmatched", "404"))
	assert.Equal(t, beforeMetrics, count("GET", "/metrics", "200"), "the metrics endpoint is not self-reported")

	h, ok := metricValue(t, "http_request_duration_seconds", map[string]string{"method": "GET", "route": "/orders/:id"})
	require.True(t, ok)
	assert.GreaterOrEqual(t, h.GetHistogram().GetSampleCount(), uint64(2))

	g, ok := metricValue(t, "http_in_flight_requests", nil)
	require.True(t, ok)
	assert.Zero(t, g.GetGauge().GetValue(), "no request in flight once they all returned")
}

func TestPrometheusMetricsInFlightGauge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	started, release := make(chan struct{}), make(chan struct{})
	r := gin.New()
	r.Use(PrometheusMetrics())
	r.GET("/slow", func(c *gin.Context) { close(started); <-release; c.Status(http.StatusNoContent) })

	done := make(chan struct{})
	go func() { serve(r, http.MethodGet, "/slow", nil); close(done) }()
	<-started
	g, ok := metricValue(t, "http_in_flight_requests", nil)
	require.True(t, ok)
	assert.Equal(t, 1.0, g.GetGauge().GetValue())
	close(release)
	<-done
	g, _ = metricValue(t, "http_in_flight_requests", nil)
	assert.Zero(t, g.GetGauge().GetValue())
}

// captureLogs swaps the global zap logger for an observer for the duration of the test.
func captureLogs(t *testing.T, level zapcore.Level) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(level)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)
	return logs
}

func TestZapRequestLogger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := captureLogs(t, zapcore.DebugLevel)
	r := gin.New()
	r.Use(RequestIDMiddleware(), ZapRequestLogger())
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/bad", func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	r.GET("/err", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	r.GET("/api/v1/up", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, p := range []string{"/ok", "/bad", "/err", "/api/v1/up"} {
		serve(r, http.MethodGet, p, func(req *http.Request) { req.RemoteAddr = "203.0.113.9:5555" })
	}

	entries := logs.All()
	require.Len(t, entries, 4)
	want := []struct {
		path  string
		level zapcore.Level
		code  int64
	}{{"/ok", zapcore.InfoLevel, 200}, {"/bad", zapcore.WarnLevel, 400}, {"/err", zapcore.ErrorLevel, 500}, {"/api/v1/up", zapcore.DebugLevel, 200}}
	for i, w := range want {
		e := entries[i]
		fields := e.ContextMap()
		assert.Equal(t, "request completed", e.Message)
		assert.Equal(t, w.level, e.Level, w.path)
		assert.Equal(t, w.path, fields["path"])
		assert.Equal(t, "GET", fields["method"])
		assert.Equal(t, w.code, fields["status"])
		assert.Equal(t, "203.0.113.9", fields["client_ip"])
		assert.Contains(t, fields, "duration")
		assert.Len(t, fields["request_id"], 16, "the request id set earlier in the chain is logged")
	}
}

func TestSentryContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(t *testing.T, withHub bool, setup func(c *gin.Context)) *sentry.Scope {
		t.Helper()
		hub := sentry.NewHub(nil, sentry.NewScope())
		r := gin.New()
		if withHub {
			r.Use(func(c *gin.Context) { c.Set("sentry", hub); c.Next() })
		}
		r.Use(func(c *gin.Context) {
			if setup != nil {
				setup(c)
			}
			c.Next()
		})
		r.Use(SentryContext())
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		rec := serve(r, http.MethodGet, "/", nil)
		require.Equal(t, http.StatusNoContent, rec.Code, "the middleware always lets the request through")
		return hub.Scope()
	}

	t.Run("request id and user id become scope data", func(t *testing.T) {
		scope := run(t, true, func(c *gin.Context) {
			ctx := logging.SetRequestID(c.Request.Context(), "abc123")
			ctx = utils.SetUserID(ctx, "user-7")
			c.Request = c.Request.WithContext(ctx)
		})
		ev := scope.ApplyToEvent(&sentry.Event{}, nil, nil)
		require.NotNil(t, ev)
		assert.Equal(t, "abc123", ev.Tags["request_id"])
		assert.Equal(t, "user-7", ev.User.ID)
	})

	t.Run("anonymous requests add neither", func(t *testing.T) {
		ev := run(t, true, nil).ApplyToEvent(&sentry.Event{}, nil, nil)
		require.NotNil(t, ev)
		assert.NotContains(t, ev.Tags, "request_id")
		assert.Empty(t, ev.User.ID)
	})

	t.Run("without a Sentry hub nothing happens", func(t *testing.T) {
		assert.NotNil(t, run(t, false, nil))
	})
}

type fakeOrderService struct {
	orderApplication.OrderService
	byUser  map[string][]*orderDomain.Order
	byOrder map[string][]*orderDomain.OrderProductRaw
}

func (f fakeOrderService) BatchGetOrdersByUserIDs(context.Context, []string) (map[string][]*orderDomain.Order, error) {
	return f.byUser, nil
}
func (f fakeOrderService) BatchGetOrderProductsByOrderIDs(context.Context, []string) (map[string][]*orderDomain.OrderProductRaw, error) {
	return f.byOrder, nil
}

type fakePaymentService struct {
	paymentApplication.PaymentService
	byOrder map[string][]*paymentDomain.MolliePayment
}

func (f fakePaymentService) BatchGetPaymentsByOrderIDs(context.Context, []string) (map[string][]*paymentDomain.MolliePayment, error) {
	return f.byOrder, nil
}

type fakeProductService struct {
	productApplication.ProductService
	categories map[string][]*productDomain.Category
}

func (f fakeProductService) BatchGetCategoriesByProductIDs(context.Context, []string) (map[string][]*productDomain.Category, error) {
	return f.categories, nil
}

type fakeUserService struct {
	userApplication.UserService
	byOrder map[string][]*userDomain.User
}

func (f fakeUserService) BatchGetUsersByOrderIDs(context.Context, []string) (map[string][]*userDomain.User, error) {
	return f.byOrder, nil
}

func TestDataLoaderMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	uid, oid, pid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	order := &orderDomain.Order{ID: uuid.New()}
	pay := &paymentDomain.MolliePayment{MolliePaymentID: "tr_1"}
	cat := &productDomain.Category{Slug: "sushi"}
	user := &userDomain.User{FirstName: "Ada"}

	r := gin.New()
	r.Use(DataLoaderMiddleware(
		fakeOrderService{byUser: map[string][]*orderDomain.Order{uid: {order}}},
		fakePaymentService{byOrder: map[string][]*paymentDomain.MolliePayment{oid: {pay}}},
		fakeProductService{categories: map[string][]*productDomain.Category{pid: {cat}}},
		fakeUserService{byOrder: map[string][]*userDomain.User{oid: {user}}},
	))
	var got struct {
		orders []*orderDomain.Order
		pays   []*paymentDomain.MolliePayment
		cats   []*productDomain.Category
		users  []*userDomain.User
	}
	r.GET("/", func(c *gin.Context) {
		ctx := c.Request.Context()
		var err error
		got.orders, err = orderApplication.GetUserOrderLoader(ctx).Loader.Load(ctx, uid)
		require.NoError(t, err)
		got.pays, err = paymentApplication.GetOrderPaymentLoader(ctx).Loader.Load(ctx, oid)
		require.NoError(t, err)
		got.cats, err = productApplication.GetProductCategoryLoader(ctx).Loader.Load(ctx, pid)
		require.NoError(t, err)
		got.users, err = userApplication.GetOrderUserLoader(ctx).Loader.Load(ctx, oid)
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
	})

	rec := serve(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []*orderDomain.Order{order}, got.orders)
	assert.Equal(t, []*paymentDomain.MolliePayment{pay}, got.pays)
	assert.Equal(t, []*productDomain.Category{cat}, got.cats)
	assert.Equal(t, []*userDomain.User{user}, got.users)
}
