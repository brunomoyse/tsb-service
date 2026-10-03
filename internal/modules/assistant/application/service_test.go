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
func (f *fakeClient) StartLogin(context.Context, bool) (domain.Login, error) {
	return domain.Login{ID: "l1", Status: domain.LoginWait}, f.err
}
func (f *fakeClient) LoginStatus(context.Context, string) (domain.Login, error) {
	return domain.Login{ID: "l1", Status: domain.LoginConfirmed}, f.err
}
func (f *fakeClient) Disconnect(context.Context) (domain.Connection, error) {
	return domain.Connection{State: domain.StateDisconnected, EverConnected: true}, f.err
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

func ptr(t time.Time) *time.Time { return &t }

func TestWatchPublishesAndAlertsOncePerIncident(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{conn: domain.Connection{State: domain.StateConnected, Since: ptr(t0), EverConnected: true}}
	pub, al := &recorder{}, &alerts{}
	s := NewService(client, pub, al.send, func() time.Time { return t0 })

	s.refresh(ctx) // first observation: nothing to publish
	s.refresh(ctx) // unchanged
	if len(pub.msgs) != 0 {
		t.Fatalf("published %v", pub.msgs)
	}

	exp := t0.Add(time.Hour)
	client.set(domain.Connection{State: domain.StateExpired, ExpiredAt: &exp, EverConnected: true})
	s.refresh(ctx)
	s.refresh(ctx)
	if len(pub.msgs) != 1 || pub.msgs[0].State != domain.StateExpired {
		t.Fatalf("published %v", pub.msgs)
	}
	if len(al.sent) != 1 || !al.sent[0].Equal(exp) {
		t.Fatalf("alerts %v", al.sent)
	}

	// Reconnected, then expired again: a new incident, a new email.
	client.set(domain.Connection{State: domain.StateConnected, Since: ptr(exp.Add(time.Hour)), EverConnected: true})
	s.refresh(ctx)
	exp2 := exp.Add(48 * time.Hour)
	client.set(domain.Connection{State: domain.StateExpired, ExpiredAt: &exp2, EverConnected: true})
	s.refresh(ctx)
	if len(al.sent) != 2 || len(pub.msgs) != 3 {
		t.Errorf("alerts %v, published %d", al.sent, len(pub.msgs))
	}
}

func TestAlertRetriedAfterFailure(t *testing.T) {
	ctx := context.Background()
	exp := t0.Add(time.Minute)
	client := &fakeClient{conn: domain.Connection{State: domain.StateExpired, ExpiredAt: &exp, EverConnected: true}}
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

func TestOldExpiryNotAlertedAgainAfterRestart(t *testing.T) {
	exp := t0.Add(-time.Hour)
	client := &fakeClient{conn: domain.Connection{State: domain.StateExpired, ExpiredAt: &exp, EverConnected: true}}
	al := &alerts{}
	s := NewService(client, &recorder{}, al.send, func() time.Time { return t0 })
	s.refresh(context.Background())
	if len(al.sent) != 0 {
		t.Errorf("an expiry from before the restart was alerted again: %v", al.sent)
	}
	// A recent expiry (within the catch-up window) is alerted on start.
	exp = t0.Add(-5 * time.Minute)
	client.set(domain.Connection{State: domain.StateExpired, ExpiredAt: &exp, EverConnected: true})
	s2 := NewService(client, &recorder{}, al.send, func() time.Time { return t0 })
	s2.refresh(context.Background())
	if len(al.sent) != 1 {
		t.Errorf("recent expiry not alerted: %v", al.sent)
	}
}

func TestManualDisconnectIsNotAlerted(t *testing.T) {
	client := &fakeClient{conn: domain.Connection{State: domain.StateConnected, Since: ptr(t0), EverConnected: true}}
	pub, al := &recorder{}, &alerts{}
	s := NewService(client, pub, al.send, func() time.Time { return t0 })
	s.refresh(context.Background())
	c, err := s.Disconnect(context.Background())
	if err != nil || c.State != domain.StateDisconnected {
		t.Fatal(c, err)
	}
	if len(al.sent) != 0 || len(pub.msgs) != 1 {
		t.Errorf("alerts %v, published %v", al.sent, pub.msgs)
	}
}

func TestUnavailableAndDisabled(t *testing.T) {
	ctx := context.Background()
	s := NewService(&fakeClient{err: domain.ErrUnavailable}, nil, nil, nil)
	c, err := s.Connection(ctx)
	if err != nil || c.State != domain.StateUnavailable {
		t.Errorf("unavailable: %+v %v", c, err)
	}
	if _, err := s.StartLogin(ctx, false); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("start login on a down agent: %v", err)
	}

	var nilService *Service
	for _, s := range []*Service{NewService(nil, nil, nil, nil), nilService} {
		if s.Enabled() {
			t.Error("enabled without client")
		}
		if _, err := s.Connection(ctx); !errors.Is(err, domain.ErrDisabled) {
			t.Errorf("connection: %v", err)
		}
		if _, err := s.StartLogin(ctx, false); !errors.Is(err, domain.ErrDisabled) {
			t.Errorf("start: %v", err)
		}
		s.Watch(ctx, time.Millisecond) // returns at once
	}
}

func TestConcurrentObservationsSendOneAlert(t *testing.T) {
	exp := t0
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
	c := domain.Connection{State: domain.StateExpired, ExpiredAt: &exp}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { s.Observe(context.Background(), c) })
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n != 1 {
		t.Errorf("alerts sent: %d", n)
	}
}
