package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tsb-service/internal/modules/assistant/domain"
)

type fakeClient struct {
	mu   sync.Mutex
	conn domain.Connection
	err  error
}

func (f *fakeClient) set(c domain.Connection) {
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
}

func (f *fakeClient) Connection(context.Context) (domain.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn, f.err
}
func (f *fakeClient) StartLink(context.Context) (domain.Link, error) {
	return domain.Link{ID: "l1", Status: domain.LinkWait}, f.err
}
func (f *fakeClient) LinkStatus(context.Context, string) (domain.Link, error) {
	return domain.Link{ID: "l1", Status: domain.LinkLinked}, f.err
}
func (f *fakeClient) RemoveAccount(context.Context, string) (domain.Connection, error) {
	return domain.Connection{State: domain.StateNoAccounts, MaxAccounts: 3}, f.err
}

type recorder struct {
	mu   sync.Mutex
	msgs []domain.Connection
}

func (r *recorder) Publish(_ string, msg any) {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg.(domain.Connection))
	r.mu.Unlock()
}

type alerts struct {
	mu   sync.Mutex
	sent []time.Time
	fail bool
}

func (a *alerts) send(_ context.Context, at time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return errors.New("smtp down")
	}
	a.sent = append(a.sent, at)
	return nil
}

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

var marie = domain.Account{ID: "123", Name: "Marie", Username: "marie", LinkedAt: t0}

func ready() domain.Connection {
	return domain.Connection{State: domain.StateReady, BotUsername: "pili_bot", Accounts: []domain.Account{marie}, MaxAccounts: 3}
}

func rejected(at time.Time) domain.Connection {
	return domain.Connection{State: domain.StateTokenRejected, BotUsername: "pili_bot", MaxAccounts: 3, ProblemAt: &at}
}

func TestWatchPublishesAndAlertsOncePerIncident(t *testing.T) {
	ctx := t.Context()
	client := &fakeClient{conn: ready()}
	pub, al := &recorder{}, &alerts{}
	s := NewService(client, pub, al.send, func() time.Time { return t0 })

	s.refresh(ctx) // first observation: nothing to publish
	s.refresh(ctx) // unchanged
	if len(pub.msgs) != 0 {
		t.Fatalf("published %v", pub.msgs)
	}

	bad := t0.Add(time.Hour)
	client.set(rejected(bad))
	s.refresh(ctx)
	s.refresh(ctx)
	if len(pub.msgs) != 1 || pub.msgs[0].State != domain.StateTokenRejected {
		t.Fatalf("published %v", pub.msgs)
	}
	if len(al.sent) != 1 || !al.sent[0].Equal(bad) {
		t.Fatalf("alerts %v", al.sent)
	}

	// Fixed, then refused again: a new incident, a new email.
	client.set(ready())
	s.refresh(ctx)
	bad2 := bad.Add(48 * time.Hour)
	client.set(rejected(bad2))
	s.refresh(ctx)
	if len(al.sent) != 2 || len(pub.msgs) != 3 {
		t.Errorf("alerts %v, published %d", al.sent, len(pub.msgs))
	}
}

func TestAccountChangesArePublished(t *testing.T) {
	ctx := t.Context()
	client := &fakeClient{conn: ready()}
	pub := &recorder{}
	s := NewService(client, pub, nil, nil)
	s.refresh(ctx)

	other := ready()
	other.Accounts = append(other.Accounts, domain.Account{ID: "456", Name: "Jean", LinkedAt: t0})
	client.set(other)
	s.refresh(ctx)
	renamed := ready()
	renamed.Accounts = []domain.Account{{ID: "123", Name: "Marie D.", Username: "marie", LinkedAt: t0}}
	client.set(renamed)
	s.refresh(ctx)
	if len(pub.msgs) != 2 {
		t.Errorf("published %d, want 2", len(pub.msgs))
	}
}

func TestConflictIsPublishedButNotAlerted(t *testing.T) {
	client := &fakeClient{conn: ready()}
	pub, al := &recorder{}, &alerts{}
	s := NewService(client, pub, al.send, func() time.Time { return t0 })
	s.refresh(t.Context())
	at := t0.Add(time.Minute)
	client.set(domain.Connection{State: domain.StateConflict, ProblemAt: &at})
	s.refresh(t.Context())
	if len(pub.msgs) != 1 || len(al.sent) != 0 {
		t.Errorf("published %v, alerts %v", pub.msgs, al.sent)
	}
}

func TestAlertRetriedAfterFailure(t *testing.T) {
	ctx := t.Context()
	client := &fakeClient{conn: rejected(t0.Add(time.Minute))}
	al := &alerts{fail: true}
	s := NewService(client, &recorder{}, al.send, func() time.Time { return t0 })
	s.refresh(ctx)
	al.fail = false
	s.refresh(ctx)
	s.refresh(ctx)
	if len(al.sent) != 1 {
		t.Errorf("alerts %v", al.sent)
	}
}

func TestOldProblemNotAlertedAgainAfterRestart(t *testing.T) {
	client := &fakeClient{conn: rejected(t0.Add(-time.Hour))}
	al := &alerts{}
	s := NewService(client, &recorder{}, al.send, func() time.Time { return t0 })
	s.refresh(t.Context())
	if len(al.sent) != 0 {
		t.Errorf("a problem from before the restart was alerted again: %v", al.sent)
	}
	// A recent problem (within the catch-up window) is alerted on start.
	client.set(rejected(t0.Add(-5 * time.Minute)))
	s2 := NewService(client, &recorder{}, al.send, func() time.Time { return t0 })
	s2.refresh(t.Context())
	if len(al.sent) != 1 {
		t.Errorf("recent problem not alerted: %v", al.sent)
	}
}

func TestRemovingAnAccountIsNotAlerted(t *testing.T) {
	client := &fakeClient{conn: ready()}
	pub, al := &recorder{}, &alerts{}
	s := NewService(client, pub, al.send, func() time.Time { return t0 })
	s.refresh(t.Context())
	c, err := s.RemoveAccount(t.Context(), "123")
	if err != nil || c.State != domain.StateNoAccounts {
		t.Fatal(c, err)
	}
	if len(al.sent) != 0 || len(pub.msgs) != 1 {
		t.Errorf("alerts %v, published %v", al.sent, pub.msgs)
	}
}

func TestUnavailableAndDisabled(t *testing.T) {
	ctx := t.Context()
	s := NewService(&fakeClient{err: domain.ErrUnavailable}, nil, nil, nil)
	c, err := s.Connection(ctx)
	if err != nil || c.State != domain.StateUnavailable {
		t.Errorf("unavailable: %+v %v", c, err)
	}
	if _, err := s.StartLink(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("start link on a down agent: %v", err)
	}

	var nilService *Service
	for _, s := range []*Service{NewService(nil, nil, nil, nil), nilService} {
		if s.Enabled() {
			t.Error("enabled without client")
		}
		if _, err := s.Connection(ctx); !errors.Is(err, domain.ErrDisabled) {
			t.Errorf("connection: %v", err)
		}
		if _, err := s.StartLink(ctx); !errors.Is(err, domain.ErrDisabled) {
			t.Errorf("start: %v", err)
		}
		s.Watch(ctx, time.Millisecond) // returns at once
	}
}

func TestConcurrentObservationsSendOneAlert(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	n := 0
	s := NewService(&fakeClient{}, nil, func(context.Context, time.Time) error {
		mu.Lock()
		n++
		mu.Unlock()
		<-release
		return nil
	}, func() time.Time { return t0 })
	c := rejected(t0)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { s.Observe(t.Context(), c) })
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n != 1 {
		t.Errorf("alerts sent: %d", n)
	}
}
