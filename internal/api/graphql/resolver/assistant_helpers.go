package resolver

import (
	"errors"
	"strings"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	assistantDomain "tsb-service/internal/modules/assistant/domain"
)

func toGQLAssistantConnection(c assistantDomain.Connection, enabled bool) *model.AssistantConnection {
	out := &model.AssistantConnection{
		Enabled:         enabled,
		State:           model.AssistantConnectionState(strings.ToUpper(c.State)),
		Since:           c.Since,
		ExpiredAt:       c.ExpiredAt,
		EverConnected:   c.EverConnected,
		LoginInProgress: c.LoginInProgress,
	}
	if !out.State.IsValid() {
		out.State = model.AssistantConnectionStateUnavailable
	}
	if c.Account != "" {
		out.Account = &c.Account
	}
	return out
}

func toGQLAssistantLogin(l assistantDomain.Login) *model.AssistantLogin {
	status := model.AssistantLoginStatus(strings.ToUpper(l.Status))
	if !status.IsValid() {
		status = model.AssistantLoginStatusFailed
	}
	return &model.AssistantLogin{ID: l.ID, Status: status, QRContent: l.QRContent, ExpiresAt: l.ExpiresAt}
}

func assistantError(err error) error {
	switch {
	case errors.Is(err, assistantDomain.ErrDisabled):
		return apperr.New(apperr.CodeAssistantDisabled, "the WeChat assistant is not configured")
	case errors.Is(err, assistantDomain.ErrUnknownLogin):
		return apperr.New(apperr.CodeNotFound, "unknown or replaced login")
	default:
		return apperr.New(apperr.CodeAssistantUnavailable, "the WeChat assistant is unavailable")
	}
}
