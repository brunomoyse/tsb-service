package resolver

import (
	"errors"
	"testing"
)

// Errors dispatched before any operation exists (an unreadable or malformed
// request body) reach the presenter without an operation context. It used to
// panic there, turning a client error into a 500.
func TestErrorPresenterWithoutOperationContext(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ErrorPresenter panicked: %v", r)
		}
	}()
	got := ErrorPresenter(t.Context(), errors.New("could not read request body: http: request body too large"))
	if got == nil || got.Message == "" {
		t.Fatalf("ErrorPresenter returned %v", got)
	}
}
