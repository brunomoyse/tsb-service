package application_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/internal/modules/hubrise/infrastructure/tokencrypt"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// otherKeyCodec is the codec of an installation whose HUBRISE_TOKEN_KEY changed since the token was
// stored (the seeded token was sealed with the test key).
func otherKeyCodec(t *testing.T) *tokencrypt.Codec {
	t.Helper()
	c, err := tokencrypt.New(bytes.Repeat([]byte{0x42}, tokencrypt.KeySize))
	require.NoError(t, err)
	return c
}

func withLostKey(t *testing.T) envOption {
	return withClients(func(e *env) application.ClientProvider {
		return application.WithAuthRecording(
			application.NewPooledClients(otherKeyCodec(t), e.srv.APIBaseURL(), e.srv.HTTPClient(), nil), e.mod.Connections, e.clock)
	})
}

func TestAnUnreadableTokenIsShownAndPausesTheWorkersWithoutBurningAttempts(t *testing.T) {
	e := newEnv(t, withLostKey(t))
	id := e.order(orderSpec{})
	e.ready(id)

	before, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	require.Nil(t, before.AuthErrorAt, "the status said connected and healthy")
	require.Nil(t, before.LastError)

	assert.Equal(t, 1, e.drain())
	row := e.outboxRows(id, domain.OutboxOrderCreate)[0]
	assert.Equal(t, domain.OutboxPending, row.State)
	assert.Zero(t, row.Attempts, "no attempt was burnt")
	assert.Empty(t, e.srv.Requests(), "nothing was sent")

	stopped, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	require.NotNil(t, stopped.AuthErrorAt, "the dashboard banner shows")
	require.NotNil(t, stopped.LastError)
	assert.Equal(t, application.UnreadableTokenMessage, *stopped.LastError)
	assert.Contains(t, *stopped.LastError, "reconnect")
	assert.NotContains(t, *stopped.LastError, hubrisetest.Token)
	assert.False(t, stopped.Active(), "the pipeline is paused like after a 401")
	assert.True(t, stopped.Connected())

	// Stamped the first time only: later attempts leave the original moment and message alone.
	e.clock.Advance(time.Hour)
	e.drain()
	again, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	assert.WithinDuration(t, *stopped.AuthErrorAt, *again.AuthErrorAt, time.Millisecond)

	// Paused: a new order queues nothing.
	other := e.order(orderSpec{})
	e.ready(other)
	assert.Empty(t, e.outboxRows(other, domain.OutboxOrderCreate))
}

func TestReconnectingClearsTheUnreadableTokenAndResumes(t *testing.T) {
	e := newEnv(t, withLostKey(t))
	id := e.order(orderSpec{})
	e.ready(id)
	e.drain()
	paused, err := e.mod.Connections.Get(t.Context())
	require.NoError(t, err)
	require.NotNil(t, paused.AuthErrorAt)

	// The admin authorizes again: the new token is sealed with the key now in use.
	sealed, err := otherKeyCodec(t).Encrypt(hubrisetest.Token)
	require.NoError(t, err)
	c, err := e.mod.Connections.Connect(t.Context(), domain.ConnectParams{AccessTokenEnc: sealed, LocationID: hubrisetest.LocationID})
	require.NoError(t, err)
	assert.Nil(t, c.AuthErrorAt)
	assert.Nil(t, c.LastError)

	e.clock.Advance(time.Hour) // past the backoff of the row handed back
	e.drainAll()
	assert.Len(t, e.posts(), 1, "the order that waited reaches HubRise")
	assert.Equal(t, domain.OutboxSent, e.outboxRows(id, domain.OutboxOrderCreate)[0].State)
}

func TestAnUnreadableTokenNeverOverwritesWhatIsAlreadyTrue(t *testing.T) {
	t.Run("an authorization error already recorded stays", func(t *testing.T) {
		e := newEnv(t, withLostKey(t))
		at := e.clock.Now().Add(-time.Minute)
		require.NoError(t, e.mod.Connections.RecordAuthError(t.Context(), at, "HTTP 401"))
		conn, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)

		_, err = e.opts.Clients.Client(t.Context(), conn)
		require.ErrorIs(t, err, domain.ErrTokenUnreadable)
		got, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.WithinDuration(t, at, *got.AuthErrorAt, time.Millisecond)
		assert.Equal(t, "HTTP 401", *got.LastError)
	})

	t.Run("a token replaced since the connection was loaded is not condemned", func(t *testing.T) {
		e := newEnv(t, withLostKey(t))
		stale, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		sealed, err := otherKeyCodec(t).Encrypt(hubrisetest.Token)
		require.NoError(t, err)
		_, err = e.mod.Connections.Connect(t.Context(), domain.ConnectParams{AccessTokenEnc: sealed, LocationID: hubrisetest.LocationID})
		require.NoError(t, err)

		_, err = e.opts.Clients.Client(t.Context(), stale)
		require.ErrorIs(t, err, domain.ErrTokenUnreadable)
		got, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.Nil(t, got.AuthErrorAt, "the new token is fine")
	})

	t.Run("no connection row, nothing to record", func(t *testing.T) {
		e := newEnv(t, withLostKey(t), withoutConnection())
		_, err := e.opts.Clients.Client(t.Context(), &domain.Connection{AccessTokenEnc: []byte("garbage")})
		require.ErrorIs(t, err, domain.ErrTokenUnreadable)
		_, err = e.mod.Connections.Get(t.Context())
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})

	t.Run("other client errors are not an unreadable token", func(t *testing.T) {
		e := newEnv(t, withClients(func(e *env) application.ClientProvider {
			return application.WithAuthRecording(application.NewPooledClients(e.mod.Codec, "not a url", nil, nil), e.mod.Connections, e.clock)
		}))
		conn, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		_, err = e.opts.Clients.Client(t.Context(), conn)
		require.Error(t, err)
		require.NotErrorIs(t, err, domain.ErrTokenUnreadable)
		got, err := e.mod.Connections.Get(t.Context())
		require.NoError(t, err)
		assert.Nil(t, got.AuthErrorAt)
	})
}

// downConnections is a connection store that fails the two calls the recording makes.
type downConnections struct {
	domain.ConnectionRepository
	get, record error
}

func (d downConnections) Get(ctx context.Context) (*domain.Connection, error) {
	if d.get != nil {
		return nil, d.get
	}
	return d.ConnectionRepository.Get(ctx)
}

func (d downConnections) RecordAuthError(ctx context.Context, at time.Time, msg string) error {
	if d.record != nil {
		return d.record
	}
	return d.ConnectionRepository.RecordAuthError(ctx, at, msg)
}

func TestAStoreThatIsDownDoesNotHideTheUnreadableToken(t *testing.T) {
	down := errors.New("store down")
	for name, conns := range map[string]func(*env) downConnections{
		"reading the row": func(e *env) downConnections {
			return downConnections{ConnectionRepository: e.mod.Connections, get: down}
		},
		"recording the error": func(e *env) downConnections {
			return downConnections{ConnectionRepository: e.mod.Connections, record: down}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			conn, err := e.mod.Connections.Get(t.Context())
			require.NoError(t, err)
			clients := application.WithAuthRecording(
				application.NewPooledClients(otherKeyCodec(t), e.srv.APIBaseURL(), e.srv.HTTPClient(), nil), conns(e), e.clock)

			_, err = clients.Client(t.Context(), conn)
			require.ErrorIs(t, err, domain.ErrTokenUnreadable, "the caller still hears about it")
			got, err := e.mod.Connections.Get(t.Context())
			require.NoError(t, err)
			assert.Nil(t, got.AuthErrorAt, "and the next attempt will try to record it again")
		})
	}
}
