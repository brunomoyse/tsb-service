package resolver

import (
	"errors"
	"testing"
	"time"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	assistantDomain "tsb-service/internal/modules/assistant/domain"
)

func TestAssistantMapping(t *testing.T) {
	since := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	acc := assistantDomain.Account{ID: "123", Name: "Marie", Username: "marie", LinkedAt: since}
	c := toGQLAssistantConnection(assistantDomain.Connection{State: "token_rejected", BotUsername: "pili_bot", Accounts: []assistantDomain.Account{acc}, MaxAccounts: 3, ProblemAt: &since}, true)
	if c.State != model.AssistantConnectionStateTokenRejected || *c.BotUsername != "pili_bot" || !c.Enabled || len(c.Accounts) != 1 || c.MaxAccounts != 3 || c.ProblemSince == nil {
		t.Errorf("connection %+v", c)
	}
	if c := toGQLAssistantConnection(assistantDomain.Connection{State: "weird"}, true); c.State != model.AssistantConnectionStateUnavailable || c.BotUsername != nil || c.Accounts == nil {
		t.Errorf("unknown state %+v", c)
	}
	if a := toGQLAssistantAccount(assistantDomain.Account{ID: "1", Name: "N"}); a.Username != nil {
		t.Errorf("account without a username %+v", a)
	}
	if l := toGQLAssistantLink(assistantDomain.Link{ID: "x", Status: "refused_full", Account: &acc}); l.Status != model.AssistantLinkStatusRefusedFull || l.Account == nil || l.Account.ID != "123" {
		t.Errorf("link %+v", l)
	}
	if l := toGQLAssistantLink(assistantDomain.Link{Status: "new_thing"}); l.Status != model.AssistantLinkStatusExpired || l.Account != nil {
		t.Errorf("unknown link status %+v", l)
	}
}

func TestAssistantErrors(t *testing.T) {
	for err, want := range map[error]apperr.Code{
		assistantDomain.ErrDisabled:       apperr.CodeAssistantDisabled,
		assistantDomain.ErrUnknownLink:    apperr.CodeNotFound,
		assistantDomain.ErrUnknownAccount: apperr.CodeNotFound,
		assistantDomain.ErrLimitReached:   apperr.CodeAssistantLimitReached,
		assistantDomain.ErrBotNotReady:    apperr.CodeAssistantUnavailable,
		assistantDomain.ErrUnavailable:    apperr.CodeAssistantUnavailable,
		errors.New("boom"):                apperr.CodeAssistantUnavailable,
	} {
		ae, ok := apperr.From(assistantError(err))
		if !ok || ae.Code != want {
			t.Errorf("%v: %v", err, ae)
		}
	}
}

func TestAssistantDisabledResolvers(t *testing.T) {
	r := &Resolver{} // no AssistantService configured
	ctx := t.Context()
	c, err := (&queryResolver{r}).AssistantConnection(ctx)
	if err != nil || c.Enabled || c.State != model.AssistantConnectionStateUnavailable {
		t.Errorf("connection %+v %v", c, err)
	}
	_, err = (&mutationResolver{r}).StartAssistantLink(ctx)
	if ae, ok := apperr.From(err); !ok || ae.Code != apperr.CodeAssistantDisabled {
		t.Errorf("start link: %v", err)
	}
}
