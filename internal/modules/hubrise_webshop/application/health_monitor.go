package application

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	restaurantApp "tsb-service/internal/modules/restaurant/application"
	"tsb-service/pkg/alerter"
	"tsb-service/pkg/pubsub"
)

// Circuit breaker tuning — all confirmed by the user.
const (
	// CircuitOpenThreshold is the number of consecutive push
	// failures after which we disable ordering on tsb-core.
	CircuitOpenThreshold = 5

	// CircuitProbeInterval is how often the ProbeLoop pings HubRise
	// while the circuit is open.
	CircuitProbeInterval = 30 * time.Second

	// CircuitAutoCloseWindow is the minimum time the circuit stays
	// open before a probe success is allowed to close it. Prevents
	// rapid flapping on transient network blips.
	CircuitAutoCloseWindow = 5 * time.Minute
)

// HealthMonitor is the circuit-breaker state machine for HubRise
// order pushes. OrderPusher calls RecordSuccess/RecordFailure on
// every push attempt; this type aggregates the outcomes, opens the
// circuit after `CircuitOpenThreshold` consecutive failures, and
// calls restaurantSvc.SetOrderingEnabledBySystem(false, reason) to
// proactively stop the webshop from accepting new orders.
//
// A separate ProbeLoop goroutine runs a lightweight GET /v1/location
// call every 30 seconds while the circuit is open. When the probe
// succeeds AND the 5-minute hold window has elapsed, the circuit
// closes and ordering is re-enabled.
//
// HealthMonitor implements application.HealthMonitorHook so it can
// be passed to OrderPusher.SetHealthMonitor without introducing a
// circular import.
type HealthMonitor struct {
	mu                  sync.Mutex
	consecutiveFailures int
	circuitOpen         bool
	openedAt            time.Time

	baseURL       string
	connRepo      domain.ConnectionRepository
	restaurantSvc restaurantApp.RestaurantService
	alerter       alerter.Alerter
	broker        *pubsub.Broker
	httpClient    *http.Client
}

// NewHealthMonitor constructs a HealthMonitor wired with its
// dependencies. The http client uses a 5s timeout since the probe
// is a cheap GET /location call.
func NewHealthMonitor(
	baseURL string,
	connRepo domain.ConnectionRepository,
	restaurantSvc restaurantApp.RestaurantService,
	a alerter.Alerter,
	broker *pubsub.Broker,
) *HealthMonitor {
	if a == nil {
		a = alerter.NoopAlerter{}
	}
	return &HealthMonitor{
		baseURL:       baseURL,
		connRepo:      connRepo,
		restaurantSvc: restaurantSvc,
		alerter:       a,
		broker:        broker,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
	}
}

// RecordSuccess resets the failure counter. If the circuit was open
// AND the 5-minute hold window has elapsed, it closes the circuit
// and re-enables ordering on tsb-core.
func (m *HealthMonitor) RecordSuccess(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.consecutiveFailures = 0

	if !m.circuitOpen {
		return
	}
	if time.Since(m.openedAt) < CircuitAutoCloseWindow {
		// Hold the circuit open for the minimum window even on success.
		// Prevents flapping on intermittent network failures.
		return
	}

	m.circuitOpen = false
	if _, err := m.restaurantSvc.SetOrderingEnabledBySystem(ctx, true, ""); err != nil {
		zap.L().Error("failed to re-enable ordering after circuit close", zap.Error(err))
		return
	}
	if m.broker != nil {
		m.broker.Publish("restaurantConfigUpdated", nil)
	}
	_ = m.alerter.Alert(ctx, alerter.SeverityInfo,
		"HubRise recovered",
		"Circuit breaker closed. Ordering has been re-enabled on the webshop.")
	zap.L().Info("hubrise circuit breaker closed")
}

// RecordFailure increments the consecutive-failures counter. Once
// it reaches `CircuitOpenThreshold`, the circuit opens: ordering is
// disabled on tsb-core with a system_disable_reason recording why,
// and a critical alert fires.
func (m *HealthMonitor) RecordFailure(ctx context.Context, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.consecutiveFailures++

	if m.consecutiveFailures < CircuitOpenThreshold || m.circuitOpen {
		return
	}

	m.circuitOpen = true
	m.openedAt = time.Now()
	reason := fmt.Sprintf("HubRise unreachable (%d consecutive push failures)", m.consecutiveFailures)
	if _, svcErr := m.restaurantSvc.SetOrderingEnabledBySystem(ctx, false, reason); svcErr != nil {
		zap.L().Error("failed to disable ordering on circuit open", zap.Error(svcErr))
	}
	if m.broker != nil {
		m.broker.Publish("restaurantConfigUpdated", nil)
	}
	_ = m.alerter.Alert(ctx, alerter.SeverityCritical,
		"Circuit breaker opened",
		fmt.Sprintf("HubRise is unreachable. Ordering has been disabled on the webshop.\nLast error: %v", err))
	zap.L().Warn("hubrise circuit breaker opened",
		zap.Int("consecutive_failures", m.consecutiveFailures),
		zap.Error(err))
}

// ProbeLoop runs while the app is alive. Every CircuitProbeInterval
// it checks whether the circuit is open and, if so, attempts a
// lightweight GET /v1/location. Success → RecordSuccess which may
// close the circuit.
func (m *HealthMonitor) ProbeLoop(ctx context.Context) {
	logger := zap.L().With(zap.String("worker", "hubrise_probe"))
	logger.Info("probe loop started", zap.Duration("interval", CircuitProbeInterval))

	ticker := time.NewTicker(CircuitProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("probe loop stopped")
			return
		case <-ticker.C:
			m.mu.Lock()
			open := m.circuitOpen
			m.mu.Unlock()
			if !open {
				continue
			}
			if err := m.probe(ctx); err != nil {
				logger.Debug("probe failed", zap.Error(err))
				continue
			}
			// Probe succeeded — attempt to close the circuit.
			m.RecordSuccess(ctx)
		}
	}
}

func (m *HealthMonitor) probe(ctx context.Context) error {
	conn, err := m.connRepo.GetByClient(ctx, ClientName)
	if err != nil {
		return err
	}
	if conn == nil {
		return fmt.Errorf("no hubrise connection")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/location", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Access-Token", conn.AccessToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("probe got status %d", resp.StatusCode)
	}
	return nil
}
