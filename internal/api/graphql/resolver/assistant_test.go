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
	c := toGQLAssistantConnection(assistantDomain.Connection{State: "expired", Account: "o@im.wechat", Since: &since, EverConnected: true}, true)
	if c.State != model.AssistantConnectionStateExpired || *c.Account != "o@im.wechat" || !c.Enabled || !c.EverConnected {
		t.Errorf("connection %+v", c)
	}
	if c := toGQLAssistantConnection(assistantDomain.Connection{State: "weird"}, true); c.State != model.AssistantConnectionStateUnavailable || c.Account != nil {
		t.Errorf("unknown state %+v", c)
	}
	if l := toGQLAssistantLogin(assistantDomain.Login{ID: "x", Status: "refused_other_account"}); l.Status != model.AssistantLoginStatusRefusedOtherAccount {
		t.Errorf("login %+v", l)
	}
	if l := toGQLAssistantLogin(assistantDomain.Login{Status: "new_thing"}); l.Status != model.AssistantLoginStatusFailed {
		t.Errorf("unknown login status %+v", l)
	}
}

func TestAssistantErrors(t *testing.T) {
	for err, want := range map[error]apperr.Code{
		assistantDomain.ErrDisabled:     apperr.CodeAssistantDisabled,
		assistantDomain.ErrUnknownLogin: apperr.CodeNotFound,
		assistantDomain.ErrUnavailable:  apperr.CodeAssistantUnavailable,
		errors.New("boom"):              apperr.CodeAssistantUnavailable,
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
	if err != nil || c.Enabled || c.State != model.AssistantConnectionStateDisconnected {
		t.Errorf("connection %+v %v", c, err)
	}
	_, err = (&mutationResolver{r}).StartAssistantLogin(ctx, nil)
	if ae, ok := apperr.From(err); !ok || ae.Code != apperr.CodeAssistantDisabled {
		t.Errorf("start login: %v", err)
	}
}
