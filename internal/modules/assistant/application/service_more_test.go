package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/assistant/domain"
)

// scriptClient serves a connection that changes after each call and a login with a chosen status.
type scriptClient struct {
	fakeClient
	login      domain.Login
	loginErr   error
	connCalls  atomic.Int32
	onConnCall func(n int32)
	conns      []domain.Connection // answered in order, the last one repeats
}

func (c *scriptClient) Connection(context.Context) (domain.Connection, error) {
	n := c.connCalls.Add(1)
	if c.onConnCall != nil {
		c.onConnCall(n)
	}
	if c.err != nil {
		return domain.Connection{}, c.err
	}
	i := min(int(n)-1, len(c.conns)-1)
	return c.conns[i], nil
}
func (c *scriptClient) StartLogin(context.Context, bool) (domain.Login, error) {
	return c.login, c.loginErr
}
func (c *scriptClient) LoginStatus(context.Context, string) (domain.Login, error) {
	return c.login, c.loginErr
}

func TestLoginRefreshesTheConnectionOnlyWhenSomethingChanged(t *testing.T) {
	connected := domain.Connection{State: domain.StateConnected, EverConnected: true}
	disconnected := domain.Connection{State: domain.StateDisconnected}

	t.Run("StartLogin refreshes after a successful start; an unchanged state is not published", func(t *testing.T) {
		client := &scriptClient{login: domain.Login{ID: "l1", Status: domain.LoginWait}, conns: []domain.Connection{disconnected, connected}}
		pub := &recorder{}
		s := NewService(client, pub, nil, nil)
		s.Observe(t.Context(), disconnected) // the watcher's first observation

		lg, err := s.StartLogin(t.Context(), true)
		require.NoError(t, err)
		assert.Equal(t, "l1", lg.ID)
		assert.EqualValues(t, 1, client.connCalls.Load())
		assert.Empty(t, pub.msgs, "an unchanged state is not published")
	})

	t.Run("StartLogin does not refresh when the agent refuses", func(t *testing.T) {
		boom := errors.New("agent says no")
		client := &scriptClient{loginErr: boom, conns: []domain.Connection{connected}}
		_, err := NewService(client, nil, nil, nil).StartLogin(t.Context(), false)
		assert.ErrorIs(t, err, boom)
		assert.Zero(t, client.connCalls.Load())
	})

	for status, wantRefresh := range map[string]bool{
		domain.LoginWait: false, domain.LoginScanned: false,
		domain.LoginConfirmed: true, domain.LoginExpired: true, domain.LoginRefused: true,
		domain.LoginFailed: true, domain.LoginCancelled: true,
	} {
		t.Run("LoginStatus "+status, func(t *testing.T) {
			client := &scriptClient{login: domain.Login{ID: "l1", Status: status}, conns: []domain.Connection{connected}}
			lg, err := NewService(client, nil, nil, nil).LoginStatus(t.Context(), "l1")
			require.NoError(t, err)
			assert.Equal(t, status, lg.Status)
			if wantRefresh {
				assert.EqualValues(t, 1, client.connCalls.Load(), "a finished login refreshes the connection")
			} else {
				assert.Zero(t, client.connCalls.Load(), "a login in progress does not")
			}
		})
	}

	t.Run("LoginStatus with an error does not refresh and returns the error", func(t *testing.T) {
		client := &scriptClient{loginErr: domain.ErrUnknownLogin, conns: []domain.Connection{connected}}
		_, err := NewService(client, nil, nil, nil).LoginStatus(t.Context(), "gone")
		assert.ErrorIs(t, err, domain.ErrUnknownLogin)
		assert.Zero(t, client.connCalls.Load())
	})
}

func TestDisabledServiceRefusesEverything(t *testing.T) {
	s := NewService(nil, nil, nil, nil)
	_, err := s.LoginStatus(t.Context(), "x")
	assert.ErrorIs(t, err, domain.ErrDisabled)
	c, err := s.Disconnect(t.Context())
	assert.ErrorIs(t, err, domain.ErrDisabled)
	assert.Empty(t, c.State)
	c, err = s.Connection(t.Context())
	assert.ErrorIs(t, err, domain.ErrDisabled)
	assert.Equal(t, domain.StateDisconnected, c.State)
}

func TestConnectionAndDisconnectErrors(t *testing.T) {
	boom := errors.New("agent broke")

	t.Run("an error other than unavailable is returned as is", func(t *testing.T) {
		_, err := NewService(&fakeClient{err: boom}, nil, nil, nil).Connection(t.Context())
		assert.ErrorIs(t, err, boom)
	})

	t.Run("a failed disconnect publishes nothing", func(t *testing.T) {
		pub := &recorder{}
		s := NewService(&fakeClient{err: boom}, pub, nil, nil)
		_, err := s.Disconnect(t.Context())
		assert.ErrorIs(t, err, boom)
		assert.Empty(t, pub.msgs)
	})

	t.Run("a refresh that fails leaves the last state alone", func(t *testing.T) {
		pub := &recorder{}
		s := NewService(&fakeClient{err: boom}, pub, nil, nil)
		s.refresh(t.Context())
		assert.Empty(t, pub.msgs)
	})
}

func TestWatchPollsUntilTheContextEnds(t *testing.T) {
	connected := domain.Connection{State: domain.StateConnected, EverConnected: true}
	expired := domain.Connection{State: domain.StateExpired, EverConnected: true}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &scriptClient{conns: []domain.Connection{connected, connected, expired}}
	client.onConnCall = func(n int32) {
		if n == 3 {
			cancel() // the third poll is the last one
		}
	}
	pub := &recorder{}
	s := NewService(client, pub, nil, nil)

	done := make(chan struct{})
	go func() {
		s.Watch(ctx, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not stop with its context")
	}

	assert.GreaterOrEqual(t, client.connCalls.Load(), int32(3), "polled every interval")
	require.NotEmpty(t, pub.msgs)
	assert.Equal(t, domain.StateExpired, pub.msgs[len(pub.msgs)-1].State, "the state change was published")
}
