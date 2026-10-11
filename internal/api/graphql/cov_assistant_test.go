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

// The Telegram assistant resolvers proxy the agent: they need no database, only the agent's answers.

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
	marie := assistantDomain.Account{ID: "123", Name: "Marie Dupont", Username: "marie", LinkedAt: since}
	agent := &fakeAgent{
		conn: assistantDomain.Connection{State: "ready", BotUsername: "pili_bot", Accounts: []assistantDomain.Account{marie}, MaxAccounts: 3},
		link: assistantDomain.Link{ID: "link-1", Status: "wait", URL: "https://t.me/pili_bot?start=abc", ExpiresAt: since.Add(15 * time.Minute)},
	}
	r := assistantResolver(agent)

	t.Run("the connection is reported with its bot and accounts", func(t *testing.T) {
		got, err := r.Query().AssistantConnection(ctx)
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, model.AssistantConnectionStateReady, got.State)
		require.NotNil(t, got.BotUsername)
		assert.Equal(t, "pili_bot", *got.BotUsername)
		assert.Equal(t, 3, got.MaxAccounts)
		require.Len(t, got.Accounts, 1)
		assert.Equal(t, "123", got.Accounts[0].ID)
		require.NotNil(t, got.Accounts[0].Username)
		assert.Equal(t, "marie", *got.Accounts[0].Username)
	})

	t.Run("an agent that does not answer is reported as unavailable, not as an error", func(t *testing.T) {
		down := assistantResolver(&fakeAgent{connErr: assistantDomain.ErrUnavailable})
		got, err := down.Query().AssistantConnection(ctx)
		require.NoError(t, err)
		assert.Equal(t, model.AssistantConnectionStateUnavailable, got.State)
		assert.True(t, got.Enabled)
		assert.NotNil(t, got.Accounts)
	})

	t.Run("any other agent failure is a typed unavailable error", func(t *testing.T) {
		broken := assistantResolver(&fakeAgent{connErr: errBoom, linkErr: errBoom, rmErr: errBoom})
		_, err := broken.Query().AssistantConnection(ctx)
		requireCode(t, err, apperr.CodeAssistantUnavailable)
		_, err = broken.Mutation().StartAssistantLink(ctx)
		requireCode(t, err, apperr.CodeAssistantUnavailable)
		_, err = broken.Query().AssistantLink(ctx, "x")
		requireCode(t, err, apperr.CodeAssistantUnavailable)
		_, err = broken.Mutation().RemoveAssistantAccount(ctx, "123")
		requireCode(t, err, apperr.CodeAssistantUnavailable)
	})

	t.Run("a link is started and followed, an unknown one is not found, a full one is refused", func(t *testing.T) {
		link, err := r.Mutation().StartAssistantLink(ctx)
		require.NoError(t, err)
		assert.Equal(t, "link-1", link.ID)
		assert.Equal(t, model.AssistantLinkStatusWait, link.Status)
		assert.Equal(t, "https://t.me/pili_bot?start=abc", link.URL)

		agent.link = assistantDomain.Link{ID: "link-1", Status: "linked", Account: &marie}
		status, err := r.Query().AssistantLink(ctx, "link-1")
		require.NoError(t, err)
		assert.Equal(t, model.AssistantLinkStatusLinked, status.Status)
		require.NotNil(t, status.Account)
		assert.Equal(t, "Marie Dupont", status.Account.Name)

		unknown := assistantResolver(&fakeAgent{linkErr: assistantDomain.ErrUnknownLink})
		_, err = unknown.Query().AssistantLink(ctx, "gone")
		requireCode(t, err, apperr.CodeNotFound)

		full := assistantResolver(&fakeAgent{linkErr: assistantDomain.ErrLimitReached})
		_, err = full.Mutation().StartAssistantLink(ctx)
		requireCode(t, err, apperr.CodeAssistantLimitReached)
	})

	t.Run("removing an account returns the connection the agent now reports", func(t *testing.T) {
		agent.conn = assistantDomain.Connection{State: "no_accounts", MaxAccounts: 3}
		got, err := r.Mutation().RemoveAssistantAccount(ctx, "123")
		require.NoError(t, err)
		assert.Equal(t, model.AssistantConnectionStateNoAccounts, got.State)
		assert.Empty(t, got.Accounts)

		missing := assistantResolver(&fakeAgent{rmErr: assistantDomain.ErrUnknownAccount})
		_, err = missing.Mutation().RemoveAssistantAccount(ctx, "999")
		requireCode(t, err, apperr.CodeNotFound)
	})

	t.Run("without an agent the assistant is reported as not configured", func(t *testing.T) {
		off := &resolver.Resolver{Broker: pubsub.NewBroker()}
		got, err := off.Query().AssistantConnection(ctx)
		require.NoError(t, err)
		assert.False(t, got.Enabled)
		assert.Equal(t, model.AssistantConnectionStateUnavailable, got.State)
		assert.Zero(t, got.MaxAccounts)
		_, err = off.Mutation().StartAssistantLink(ctx)
		requireCode(t, err, apperr.CodeAssistantDisabled)
		_, err = off.Mutation().RemoveAssistantAccount(ctx, "123")
		requireCode(t, err, apperr.CodeAssistantDisabled)
		_, err = off.Query().AssistantLink(ctx, "x")
		requireCode(t, err, apperr.CodeAssistantDisabled)
	})
}
