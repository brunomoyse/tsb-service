// Package application proxies the dashboard's assistant actions to the agent
// and watches the agent's WeChat session.
package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/assistant/domain"
)

// Topic is the pubsub topic of connection changes (a domain.Connection).
const Topic = "assistantConnectionUpdated"

// alertCatchUp: an expiry older than this when tsb-service starts is
// assumed to have been alerted by the previous process.
const alertCatchUp = 10 * time.Minute

// Publisher fans out connection changes to GraphQL subscribers.
type Publisher interface {
	Publish(topic string, msg any)
}

// Service is the assistant connection service. A nil client disables it.
type Service struct {
	client domain.AgentClient
	pub    Publisher
	// alert tells the owner that the WeChat session expired (email).
	alert   func(ctx context.Context, expiredAt time.Time) error
	now     func() time.Time
	started time.Time

	mu       sync.Mutex
	last     *domain.Connection
	alerted  time.Time // ExpiredAt of the last alerted expiry
	alerting bool      // an alert is being sent
}

// NewService returns the service. client may be nil (assistant not configured).
func NewService(client domain.AgentClient, pub Publisher, alert func(ctx context.Context, expiredAt time.Time) error, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{client: client, pub: pub, alert: alert, now: now, started: now()}
}

// Enabled reports whether an agent is configured.
func (s *Service) Enabled() bool { return s != nil && s.client != nil }

// Connection returns the WeChat session state. An agent that does not
// answer is reported as StateUnavailable, not as an error.
func (s *Service) Connection(ctx context.Context) (domain.Connection, error) {
	if !s.Enabled() {
		return domain.Connection{State: domain.StateDisconnected}, domain.ErrDisabled
	}
	c, err := s.client.Connection(ctx)
	if errors.Is(err, domain.ErrUnavailable) {
		zap.L().Warn("assistant agent unavailable", zap.Error(err))
		return domain.Connection{State: domain.StateUnavailable}, nil
	}
	return c, err
}

// StartLogin starts a QR login on the agent.
func (s *Service) StartLogin(ctx context.Context, replaceOwner bool) (domain.Login, error) {
	if !s.Enabled() {
		return domain.Login{}, domain.ErrDisabled
	}
	lg, err := s.client.StartLogin(ctx, replaceOwner)
	if err == nil {
		s.refresh(ctx)
	}
	return lg, err
}

// LoginStatus returns a login's status. A finished login refreshes the
// connection for every subscriber.
func (s *Service) LoginStatus(ctx context.Context, id string) (domain.Login, error) {
	if !s.Enabled() {
		return domain.Login{}, domain.ErrDisabled
	}
	lg, err := s.client.LoginStatus(ctx, id)
	if err == nil && lg.Status != domain.LoginWait && lg.Status != domain.LoginScanned {
		s.refresh(ctx)
	}
	return lg, err
}

// Disconnect makes the agent forget its WeChat session.
func (s *Service) Disconnect(ctx context.Context) (domain.Connection, error) {
	if !s.Enabled() {
		return domain.Connection{}, domain.ErrDisabled
	}
	c, err := s.client.Disconnect(ctx)
	if err == nil {
		s.Observe(ctx, c)
	}
	return c, err
}

func (s *Service) refresh(ctx context.Context) {
	if c, err := s.Connection(ctx); err == nil {
		s.Observe(ctx, c)
	}
}

// Watch polls the agent every interval until ctx ends.
func (s *Service) Watch(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func same(a, b domain.Connection) bool {
	eq := func(x, y *time.Time) bool { return (x == nil && y == nil) || (x != nil && y != nil && x.Equal(*y)) }
	return a.State == b.State && a.Account == b.Account && a.EverConnected == b.EverConnected &&
		a.LoginInProgress == b.LoginInProgress && eq(a.Since, b.Since) && eq(a.ExpiredAt, b.ExpiredAt)
}

// Observe records a connection state: changes are published, and an
// expired session is alerted once per incident.
func (s *Service) Observe(ctx context.Context, c domain.Connection) {
	s.mu.Lock()
	changed := s.last == nil || !same(*s.last, c)
	first := s.last == nil
	s.last = &c
	var alertAt *time.Time
	if s.alert != nil && c.State == domain.StateExpired && c.ExpiredAt != nil && !c.ExpiredAt.Equal(s.alerted) && !s.alerting {
		if c.ExpiredAt.Before(s.started.Add(-alertCatchUp)) {
			s.alerted = *c.ExpiredAt
		} else {
			alertAt, s.alerting = c.ExpiredAt, true
		}
	}
	s.mu.Unlock()

	if changed && !first && s.pub != nil {
		s.pub.Publish(Topic, c)
	}
	if alertAt == nil {
		return
	}
	zap.L().Warn("WeChat assistant session expired, alerting the owner", zap.Time("expired_at", *alertAt))
	err := s.alert(ctx, *alertAt)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerting = false
	if err != nil {
		// Not marked as alerted: the next poll retries.
		zap.L().Error("failed to send the assistant expiry alert", zap.Error(err))
		return
	}
	s.alerted = *alertAt
}
