package graphql_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/internal/api/graphql/resolver"
	assistantApplication "tsb-service/internal/modules/assistant/application"
	assistantDomain "tsb-service/internal/modules/assistant/domain"
	"tsb-service/pkg/pubsub"
)

// The WeChat assistant resolvers proxy the agent: they need no database, only the agent's answers.

func assistantResolver(agent *fakeAgent) *resolver.Resolver {
	broker := pubsub.NewBroker()
	return &resolver.Resolver{Broker: broker, AssistantService: assistantApplication.NewService(agent, broker, nil, nil)}
}

func requireCode(t *testing.T, err error, code apperr.Code) {
	t.Helper()
	appErr, ok := apperr.From(err)
	require.True(t, ok, "not a typed error: %v", err)
	assert.Equal(t, code, appErr.Code)
}

func TestAssistantResolvers(t *testing.T) {
	ctx := t.Context()
	since := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	agent := &fakeAgent{
		conn:  assistantDomain.Connection{State: "connected", Account: "owner@im.wechat", Since: &since, EverConnected: true},
		login: assistantDomain.Login{ID: "login-1", Status: "wait", QRContent: "https://wechat.example/qr", ExpiresAt: since.Add(time.Minute)},
	}
	r := assistantResolver(agent)

	t.Run("the connection is reported with its account", func(t *testing.T) {
		got, err := r.Query().AssistantConnection(ctx)
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, model.AssistantConnectionStateConnected, got.State)
		require.NotNil(t, got.Account)
		assert.Equal(t, "owner@im.wechat", *got.Account)
		assert.True(t, got.EverConnected)
	})

	t.Run("an agent that does not answer is reported as unavailable, not as an error", func(t *testing.T) {
		down := assistantResolver(&fakeAgent{connErr: assistantDomain.ErrUnavailable})
		got, err := down.Query().AssistantConnection(ctx)
		require.NoError(t, err)
		assert.Equal(t, model.AssistantConnectionStateUnavailable, got.State)
		assert.True(t, got.Enabled)
	})

	t.Run("any other agent failure is a typed unavailable error", func(t *testing.T) {
		broken := assistantResolver(&fakeAgent{connErr: errBoom, loginErr: errBoom, discErr: errBoom})
		_, err := broken.Query().AssistantConnection(ctx)
		requireCode(t, err, apperr.CodeAssistantUnavailable)
		_, err = broken.Mutation().StartAssistantLogin(ctx, nil)
		requireCode(t, err, apperr.CodeAssistantUnavailable)
		_, err = broken.Query().AssistantLogin(ctx, "x")
		requireCode(t, err, apperr.CodeAssistantUnavailable)
		_, err = broken.Mutation().DisconnectAssistant(ctx)
		requireCode(t, err, apperr.CodeAssistantUnavailable)
	})

	t.Run("a login is started, followed and an unknown one is not found", func(t *testing.T) {
		login, err := r.Mutation().StartAssistantLogin(ctx, new(true))
		require.NoError(t, err)
		assert.Equal(t, "login-1", login.ID)
		assert.Equal(t, model.AssistantLoginStatusWait, login.Status)
		assert.Equal(t, "https://wechat.example/qr", login.QRContent)
		login, err = r.Mutation().StartAssistantLogin(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, "login-1", login.ID)

		status, err := r.Query().AssistantLogin(ctx, "login-1")
		require.NoError(t, err)
		assert.Equal(t, model.AssistantLoginStatusWait, status.Status)

		unknown := assistantResolver(&fakeAgent{loginErr: assistantDomain.ErrUnknownLogin})
		_, err = unknown.Query().AssistantLogin(ctx, "gone")
		requireCode(t, err, apperr.CodeNotFound)
	})

	t.Run("disconnecting returns the connection the agent now reports", func(t *testing.T) {
		agent.conn = assistantDomain.Connection{State: "disconnected"}
		got, err := r.Mutation().DisconnectAssistant(ctx)
		require.NoError(t, err)
		assert.Equal(t, model.AssistantConnectionStateDisconnected, got.State)
		assert.True(t, got.Enabled)
	})

	t.Run("without an agent the assistant is reported as not configured", func(t *testing.T) {
		off := &resolver.Resolver{Broker: pubsub.NewBroker()}
		got, err := off.Query().AssistantConnection(ctx)
		require.NoError(t, err)
		assert.False(t, got.Enabled)
		_, err = off.Mutation().StartAssistantLogin(ctx, nil)
		requireCode(t, err, apperr.CodeAssistantDisabled)
		_, err = off.Mutation().DisconnectAssistant(ctx)
		requireCode(t, err, apperr.CodeAssistantDisabled)
		_, err = off.Query().AssistantLogin(ctx, "x")
		requireCode(t, err, apperr.CodeAssistantDisabled)
	})
}
