package resolver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	gqlgraphql "github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"

	"tsb-service/internal/api/graphql/apperr"
)

// executingCtx is a context in which a resolver is running (an operation was resolved).
func executingCtx() context.Context {
	return gqlgraphql.WithOperationContext(context.Background(), &gqlgraphql.OperationContext{
		OperationName: "Test",
		Operation:     &ast.OperationDefinition{Name: "Test"},
	})
}

func TestErrorPresenter_ServerFaultsGetAGenericMessage(t *testing.T) {
	raw := `pq: password authentication failed for user "tsb" (sql: no rows in result set)`
	cases := map[string]error{
		"code-less":              fmt.Errorf("failed to load orders: %w", errors.New(raw)),
		"unexpected coded error": apperr.Newf(apperr.CodeOrderCreateFailed, "failed to create order: %w", errors.New(raw)),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := ErrorPresenter(executingCtx(), in)
			if got.Message != internalErrorMessage {
				t.Errorf("message = %q, want the generic text", got.Message)
			}
		})
	}

	// The code and parameters of a coded fault survive, so clients can still map it.
	got := ErrorPresenter(executingCtx(), apperr.New(apperr.CodeCouponCheckFailed, "boom").With("k", "v"))
	if got.Extensions["code"] != "COUPON_CHECK_FAILED" || got.Extensions["k"] != "v" {
		t.Errorf("extensions = %v", got.Extensions)
	}
}

func TestErrorPresenter_ExpectedErrorsKeepTheirMessage(t *testing.T) {
	got := ErrorPresenter(executingCtx(), apperr.New(apperr.CodeNotFound, "order not found"))
	if got.Message != "order not found" {
		t.Errorf("message = %q", got.Message)
	}
}

func TestErrorPresenter_RequestRejectionsKeepTheirMessage(t *testing.T) {
	// Before any operation is resolved (parse / validation failures): useful to the developer, never ours.
	ctx := gqlgraphql.WithOperationContext(context.Background(), &gqlgraphql.OperationContext{})
	got := ErrorPresenter(ctx, errors.New(`Cannot query field "x" on type "Query".`))
	if got.Message == internalErrorMessage {
		t.Error("a request rejection must keep its message")
	}
}

func TestChoiceLoadError_NoRowsIsACleanSelectionInvalid(t *testing.T) {
	err := choiceLoadError(fmt.Errorf("find: %w", sql.ErrNoRows), uuid.New(), uuid.New())
	appErr, ok := apperr.From(err)
	if !ok || appErr.Code != apperr.CodeSelectionInvalid {
		t.Fatalf("err = %v", err)
	}
	if got := ErrorPresenter(executingCtx(), err).Message; got == "" || errors.Is(err, sql.ErrNoRows) || strings.Contains(got, "sql") || strings.Contains(got, "no rows") {
		t.Errorf("message leaks the driver text: %q (is sql.ErrNoRows: %v)", got, errors.Is(err, sql.ErrNoRows))
	}
}
