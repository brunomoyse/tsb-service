package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests, by method, route, and status.",
		},
		[]string{"method", "route", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency, by method and route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)

	httpInFlightRequests = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_in_flight_requests",
			Help: "Currently in-flight HTTP requests.",
		},
	)
)

// PrometheusMetrics records request count, latency, and in-flight gauge.
// The /metrics endpoint is skipped to avoid self-reporting noise.
// Route label uses c.FullPath() (e.g. /api/v1/orders/:id) so path params
// don't explode label cardinality; unmatched routes are bucketed as "unmatched".
func PrometheusMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/metrics" {
			c.Next()
			return
		}

		httpInFlightRequests.Inc()
		defer httpInFlightRequests.Dec()

		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(c.Writer.Status())

		httpRequestsTotal.WithLabelValues(c.Request.Method, route, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(start).Seconds())
	}
}
