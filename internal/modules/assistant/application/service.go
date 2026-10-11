// Package application proxies the dashboard's assistant actions to the agent
// and watches the agent's Telegram bot.
package application

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/assistant/domain"
)

// Topic is the pubsub topic of connection changes (a domain.Connection).
const Topic = "assistantConnectionUpdated"

// alertCatchUp: a problem older than this when tsb-service starts is
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
	// alert tells the owner that Telegram refused the bot (email).
	alert   func(ctx context.Context, since time.Time) error
	now     func() time.Time
	started time.Time

	mu       sync.Mutex
	last     *domain.Connection
	alerted  time.Time // ProblemAt of the last alerted incident
	alerting bool      // an alert is being sent
}

// NewService returns the service. client may be nil (assistant not configured).
func NewService(client domain.AgentClient, pub Publisher, alert func(ctx context.Context, since time.Time) error, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{client: client, pub: pub, alert: alert, now: now, started: now()}
}

// Enabled reports whether an agent is configured.
func (s *Service) Enabled() bool { return s != nil && s.client != nil }

// Connection returns the Telegram bot state. An agent that does not
// answer is reported as StateUnavailable, not as an error.
func (s *Service) Connection(ctx context.Context) (domain.Connection, error) {
	if !s.Enabled() {
		return domain.Connection{State: domain.StateUnavailable}, domain.ErrDisabled
	}
	c, err := s.client.Connection(ctx)
	if errors.Is(err, domain.ErrUnavailable) {
		zap.L().Warn("assistant agent unavailable", zap.Error(err))
		return domain.Connection{State: domain.StateUnavailable}, nil
	}
	return c, err
}

// StartLink creates a one-time link on the agent.
func (s *Service) StartLink(ctx context.Context) (domain.Link, error) {
	if !s.Enabled() {
		return domain.Link{}, domain.ErrDisabled
	}
	lk, err := s.client.StartLink(ctx)
	if err == nil {
		s.refresh(ctx)
	}
	return lk, err
}

// LinkStatus returns a link's status. A finished link refreshes the
// connection for every subscriber.
func (s *Service) LinkStatus(ctx context.Context, id string) (domain.Link, error) {
	if !s.Enabled() {
		return domain.Link{}, domain.ErrDisabled
	}
	lk, err := s.client.LinkStatus(ctx, id)
	if err == nil && lk.Status != domain.LinkWait {
		s.refresh(ctx)
	}
	return lk, err
}

// RemoveAccount unlinks a Telegram account from the assistant.
func (s *Service) RemoveAccount(ctx context.Context, id string) (domain.Connection, error) {
	if !s.Enabled() {
		return domain.Connection{}, domain.ErrDisabled
	}
	c, err := s.client.RemoveAccount(ctx, id)
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
	accounts := slices.EqualFunc(a.Accounts, b.Accounts, func(x, y domain.Account) bool {
		return x.ID == y.ID && x.Name == y.Name && x.Username == y.Username && x.LinkedAt.Equal(y.LinkedAt)
	})
	return a.State == b.State && a.BotUsername == b.BotUsername && accounts && eq(a.ProblemAt, b.ProblemAt)
}

// Observe records a connection state: changes are published, and a bot that
// Telegram refuses is alerted once per incident.
func (s *Service) Observe(ctx context.Context, c domain.Connection) {
	s.mu.Lock()
	changed := s.last == nil || !same(*s.last, c)
	first := s.last == nil
	s.last = &c
	var alertAt *time.Time
	if s.alert != nil && c.State == domain.StateTokenRejected && c.ProblemAt != nil && !c.ProblemAt.Equal(s.alerted) && !s.alerting {
		if c.ProblemAt.Before(s.started.Add(-alertCatchUp)) {
			s.alerted = *c.ProblemAt
		} else {
			alertAt, s.alerting = c.ProblemAt, true
		}
	}
	s.mu.Unlock()

	if changed && c.State == domain.StateConflict {
		zap.L().Warn("another process polls the assistant's Telegram bot with the same token")
	}
	if changed && !first && s.pub != nil {
		s.pub.Publish(Topic, c)
	}
	if alertAt == nil {
		return
	}
	zap.L().Warn("Telegram refused the assistant's bot, alerting the owner", zap.Time("problem_at", *alertAt))
	err := s.alert(ctx, *alertAt)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerting = false
	if err != nil {
		// Not marked as alerted: the next poll retries.
		zap.L().Error("failed to send the assistant bot alert", zap.Error(err))
		return
	}
	s.alerted = *alertAt
}
