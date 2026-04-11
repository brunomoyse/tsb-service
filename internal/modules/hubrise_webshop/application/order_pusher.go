package application

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise_webshop/domain"
	"tsb-service/internal/modules/hubrise_webshop/infrastructure"
	"tsb-service/pkg/alerter"
	"tsb-service/pkg/logging"
)

// MaxRetryAttempts is the cap after which a failing push is given up
// on. With exponential backoff starting at 60s and capping at 30min,
// 10 attempts covers ~55 minutes of HubRise downtime before we alert
// the operator and stop retrying the order.
const MaxRetryAttempts = 10

// OrderPusher pushes a single order to HubRise after payment success.
//
// Every attempt is tracked in the `orders` table via OrderPushRepository
// (hubrise_push_status / hubrise_push_attempts / hubrise_last_push_at),
// which lets the background RetryWorker durably retry failed pushes.
type OrderPusher struct {
	baseURL  string
	connRepo domain.ConnectionRepository
	pushRepo domain.OrderPushRepository
	alerter  alerter.Alerter

	// healthMonitor is an optional Phase C hook used for the
	// circuit-breaker state machine. It may be nil during Phase A/B.
	healthMonitor HealthMonitorHook
}

// HealthMonitorHook is the subset of methods the Phase C circuit
// breaker exposes to OrderPusher. Defined as an interface so Phase A
// code can compile without pulling in the concrete health_monitor.
type HealthMonitorHook interface {
	RecordSuccess(ctx context.Context)
	RecordFailure(ctx context.Context, err error)
}

// OrderLoader is the minimal interface an external order loader must
// implement so we don't couple this module to the order infrastructure.
type OrderLoader interface {
	LoadForHubrisePush(ctx context.Context, orderID uuid.UUID) (
		req *domain.HubriseCreateOrderRequest, err error,
	)
}

// NewOrderPusher returns an OrderPusher wired with the push state
// repository and an alerter. Pass alerter.NoopAlerter{} when alerting
// is disabled (e.g. during unit tests or development).
func NewOrderPusher(
	baseURL string,
	connRepo domain.ConnectionRepository,
	pushRepo domain.OrderPushRepository,
	a alerter.Alerter,
) *OrderPusher {
	if a == nil {
		a = alerter.NoopAlerter{}
	}
	return &OrderPusher{
		baseURL:  baseURL,
		connRepo: connRepo,
		pushRepo: pushRepo,
		alerter:  a,
	}
}

// SetHealthMonitor installs a circuit-breaker hook post-construction.
// Called from main.go after both the pusher and the monitor have been
// instantiated (they reference each other).
func (p *OrderPusher) SetHealthMonitor(m HealthMonitorHook) {
	p.healthMonitor = m
}

// PushOrder loads the order via the provided loader and POSTs it to
// HubRise. It returns the remote order id on success.
//
// Every path through this function updates the order's push state
// so the retry worker can pick up from where we left off. On terminal
// failure (attempts >= MaxRetryAttempts) an alert is fired.
func (p *OrderPusher) PushOrder(
	ctx context.Context,
	loader OrderLoader,
	orderID uuid.UUID,
) (string, error) {
	// Durably mark the push as in-flight before doing any network
	// work. If the process crashes between here and the final update,
	// the retry worker will observe a "stuck pending" row and replay.
	if err := p.pushRepo.MarkPushPending(ctx, orderID); err != nil {
		logging.FromContext(ctx).Warn("mark push pending failed",
			zap.String("order_id", orderID.String()), zap.Error(err))
		// Not fatal — continue and let the retry loop handle it.
	}

	conn, err := p.connRepo.GetByClient(ctx, ClientName)
	if err != nil {
		return "", p.handleFailure(ctx, orderID, fmt.Errorf("load hubrise connection: %w", err))
	}
	if conn == nil {
		// Not configured yet — leave the order in 'pending' state.
		// Once the admin completes the OAuth flow the retry worker
		// picks it up on its next tick. Don't count this as a failure.
		logging.FromContext(ctx).Info("hubrise not connected, skipping order push",
			zap.String("order_id", orderID.String()))
		return "", nil
	}

	req, err := loader.LoadForHubrisePush(ctx, orderID)
	if err != nil {
		return "", p.handleFailure(ctx, orderID, fmt.Errorf("load order for hubrise push: %w", err))
	}

	client := infrastructure.NewHTTPClient(p.baseURL, conn.AccessToken)
	resp, err := client.PostJSON(ctx, fmt.Sprintf("/locations/%s/orders", conn.LocationID), req)
	if err != nil {
		return "", p.handleFailure(ctx, orderID, err)
	}

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return "", p.handleFailure(ctx, orderID, fmt.Errorf("parse hubrise order response: %w", err))
	}

	if err := p.pushRepo.MarkPushed(ctx, orderID, parsed.ID); err != nil {
		// We got a 200 back from HubRise but couldn't update our
		// local state — this is a nearly impossible "half-commit".
		// Log loudly so a human can reconcile.
		logging.FromContext(ctx).Error("mark pushed failed after successful POST",
			zap.String("order_id", orderID.String()),
			zap.String("remote_id", parsed.ID),
			zap.Error(err))
		return parsed.ID, err
	}

	// Phase C hook — the health monitor observes only outcomes from
	// real push attempts, not from the "not connected" short-circuit.
	if p.healthMonitor != nil {
		p.healthMonitor.RecordSuccess(ctx)
	}

	return parsed.ID, nil
}

// handleFailure marks the order failed, increments the attempt count,
// fires a critical alert on the final attempt, and forwards the error
// to the caller unchanged.
func (p *OrderPusher) handleFailure(
	ctx context.Context, orderID uuid.UUID, err error,
) error {
	attempts, markErr := p.pushRepo.MarkPushFailed(ctx, orderID, err.Error())
	if markErr != nil {
		logging.FromContext(ctx).Error("mark push failed failed",
			zap.String("order_id", orderID.String()), zap.Error(markErr))
	}

	if attempts >= MaxRetryAttempts {
		body := fmt.Sprintf(
			"Order %s failed to push to HubRise after %d attempts.\nLast error: %v",
			orderID, attempts, err,
		)
		if alertErr := p.alerter.Alert(ctx, alerter.SeverityCritical,
			"HubRise order push gave up", body); alertErr != nil {
			logging.FromContext(ctx).Warn("alert dispatch failed",
				zap.Error(alertErr))
		}
	}

	if p.healthMonitor != nil {
		p.healthMonitor.RecordFailure(ctx, err)
	}

	return err
}
