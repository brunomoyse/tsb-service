package resolver

import (
	"errors"
	"strings"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	assistantDomain "tsb-service/internal/modules/assistant/domain"
)

func toGQLAssistantAccount(a assistantDomain.Account) *model.AssistantAccount {
	out := &model.AssistantAccount{ID: a.ID, Name: a.Name, LinkedAt: a.LinkedAt}
	if a.Username != "" {
		out.Username = &a.Username
	}
	return out
}

func toGQLAssistantConnection(c assistantDomain.Connection, enabled bool) *model.AssistantConnection {
	out := &model.AssistantConnection{
		Enabled:      enabled,
		State:        model.AssistantConnectionState(strings.ToUpper(c.State)),
		Accounts:     make([]*model.AssistantAccount, 0, len(c.Accounts)),
		MaxAccounts:  c.MaxAccounts,
		ProblemSince: c.ProblemAt,
	}
	if !out.State.IsValid() {
		out.State = model.AssistantConnectionStateUnavailable
	}
	if c.BotUsername != "" {
		out.BotUsername = &c.BotUsername
	}
	for _, a := range c.Accounts {
		out.Accounts = append(out.Accounts, toGQLAssistantAccount(a))
	}
	return out
}

func toGQLAssistantLink(l assistantDomain.Link) *model.AssistantLink {
	status := model.AssistantLinkStatus(strings.ToUpper(l.Status))
	if !status.IsValid() {
		status = model.AssistantLinkStatusExpired
	}
	out := &model.AssistantLink{ID: l.ID, Status: status, URL: l.URL, ExpiresAt: l.ExpiresAt}
	if l.Account != nil {
		out.Account = toGQLAssistantAccount(*l.Account)
	}
	return out
}

func assistantError(err error) error {
	switch {
	case errors.Is(err, assistantDomain.ErrDisabled):
		return apperr.New(apperr.CodeAssistantDisabled, "the Telegram assistant is not configured")
	case errors.Is(err, assistantDomain.ErrUnknownLink):
		return apperr.New(apperr.CodeNotFound, "unknown link")
	case errors.Is(err, assistantDomain.ErrUnknownAccount):
		return apperr.New(apperr.CodeNotFound, "unknown account")
	case errors.Is(err, assistantDomain.ErrLimitReached):
		return apperr.New(apperr.CodeAssistantLimitReached, "the assistant already has the maximum number of accounts")
	default:
		return apperr.New(apperr.CodeAssistantUnavailable, "the Telegram assistant is unavailable")
	}
}
