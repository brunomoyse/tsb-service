package scaleway

import (
	"errors"
	"fmt"
	"testing"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

func TestClassifyTemError(t *testing.T) {
	rcpt := &scw.InvalidArgumentsError{Details: []scw.InvalidArgumentsErrorDetail{{
		ArgumentName: "rcpt", Reason: "format", HelpMessage: `Invalid email recipient address: ""`,
	}}}
	if err := classifyTemError(fmt.Errorf("send: %w", rcpt)); !errors.Is(err, ErrInvalidRecipient) {
		t.Fatalf("rcpt rejection = %v, want ErrInvalidRecipient", err)
	}

	subject := &scw.InvalidArgumentsError{Details: []scw.InvalidArgumentsErrorDetail{{ArgumentName: "subject", Reason: "required"}}}
	if err := classifyTemError(subject); errors.Is(err, ErrInvalidRecipient) {
		t.Fatal("a subject error must stay a server fault")
	}
	if err := classifyTemError(nil); err != nil {
		t.Fatalf("nil = %v", err)
	}
}
