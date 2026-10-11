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

// scriptClient serves a connection that changes after each call and a link with a chosen status.
type scriptClient struct {
	fakeClient
	link       domain.Link
	linkErr    error
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
func (c *scriptClient) StartLink(context.Context) (domain.Link, error) {
	return c.link, c.linkErr
}
func (c *scriptClient) LinkStatus(context.Context, string) (domain.Link, error) {
	return c.link, c.linkErr
}

func TestLinkRefreshesTheConnectionOnlyWhenSomethingChanged(t *testing.T) {
	noAccounts := domain.Connection{State: domain.StateNoAccounts, MaxAccounts: 3}
	withMarie := ready()

	t.Run("StartLink refreshes after a successful start; an unchanged state is not published", func(t *testing.T) {
		client := &scriptClient{link: domain.Link{ID: "l1", Status: domain.LinkWait}, conns: []domain.Connection{noAccounts, withMarie}}
		pub := &recorder{}
		s := NewService(client, pub, nil, nil)
		s.Observe(t.Context(), noAccounts) // the watcher's first observation

		lk, err := s.StartLink(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "l1", lk.ID)
		assert.EqualValues(t, 1, client.connCalls.Load())
		assert.Empty(t, pub.msgs, "an unchanged state is not published")
	})

	t.Run("StartLink does not refresh when the agent refuses", func(t *testing.T) {
		client := &scriptClient{linkErr: domain.ErrLimitReached, conns: []domain.Connection{withMarie}}
		_, err := NewService(client, nil, nil, nil).StartLink(t.Context())
		assert.ErrorIs(t, err, domain.ErrLimitReached)
		assert.Zero(t, client.connCalls.Load())
	})

	for status, wantRefresh := range map[string]bool{
		domain.LinkWait: false, domain.LinkLinked: true, domain.LinkExpired: true, domain.LinkRefusedFull: true,
	} {
		t.Run("LinkStatus "+status, func(t *testing.T) {
			client := &scriptClient{link: domain.Link{ID: "l1", Status: status}, conns: []domain.Connection{withMarie}}
			lk, err := NewService(client, nil, nil, nil).LinkStatus(t.Context(), "l1")
			require.NoError(t, err)
			assert.Equal(t, status, lk.Status)
			if wantRefresh {
				assert.EqualValues(t, 1, client.connCalls.Load(), "a finished link refreshes the connection")
			} else {
				assert.Zero(t, client.connCalls.Load(), "a link in progress does not")
			}
		})
	}

	t.Run("LinkStatus with an error does not refresh and returns the error", func(t *testing.T) {
		client := &scriptClient{linkErr: domain.ErrUnknownLink, conns: []domain.Connection{withMarie}}
		_, err := NewService(client, nil, nil, nil).LinkStatus(t.Context(), "gone")
		assert.ErrorIs(t, err, domain.ErrUnknownLink)
		assert.Zero(t, client.connCalls.Load())
	})
}

func TestDisabledServiceRefusesEverything(t *testing.T) {
	s := NewService(nil, nil, nil, nil)
	_, err := s.LinkStatus(t.Context(), "x")
	assert.ErrorIs(t, err, domain.ErrDisabled)
	c, err := s.RemoveAccount(t.Context(), "123")
	assert.ErrorIs(t, err, domain.ErrDisabled)
	assert.Empty(t, c.State)
	c, err = s.Connection(t.Context())
	assert.ErrorIs(t, err, domain.ErrDisabled)
	assert.Equal(t, domain.StateUnavailable, c.State)
}

func TestConnectionAndRemoveErrors(t *testing.T) {
	boom := errors.New("agent broke")

	t.Run("an error other than unavailable is returned as is", func(t *testing.T) {
		_, err := NewService(&fakeClient{err: boom}, nil, nil, nil).Connection(t.Context())
		assert.ErrorIs(t, err, boom)
	})

	t.Run("a failed removal publishes nothing", func(t *testing.T) {
		pub := &recorder{}
		s := NewService(&fakeClient{err: boom}, pub, nil, nil)
		_, err := s.RemoveAccount(t.Context(), "123")
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
	connected := ready()
	expired := rejected(t0)
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
	assert.Equal(t, domain.StateTokenRejected, pub.msgs[len(pub.msgs)-1].State, "the state change was published")
}
