package alerter

import (
	"testing"
	"time"
)

func TestEmailAlerterDedupSuppressesDuplicatesWithinTTL(t *testing.T) {
	a := NewEmailAlerter([]string{"ops@example.com"}, 1*time.Second)

	if !a.shouldSend(SeverityWarning, "something bad") {
		t.Fatal("first call should be allowed")
	}
	if a.shouldSend(SeverityWarning, "something bad") {
		t.Fatal("duplicate within TTL should be suppressed")
	}
	// Different title → different key → allowed.
	if !a.shouldSend(SeverityWarning, "other bad thing") {
		t.Fatal("different title should be allowed")
	}
	// Different severity → different key → allowed.
	if !a.shouldSend(SeverityCritical, "something bad") {
		t.Fatal("different severity should be allowed")
	}
}

func TestEmailAlerterDedupAllowsAfterTTL(t *testing.T) {
	a := NewEmailAlerter([]string{"ops@example.com"}, 50*time.Millisecond)

	if !a.shouldSend(SeverityInfo, "heartbeat") {
		t.Fatal("first call should be allowed")
	}
	time.Sleep(80 * time.Millisecond)
	if !a.shouldSend(SeverityInfo, "heartbeat") {
		t.Fatal("second call after TTL should be allowed")
	}
}

func TestEmailAlerterAlertNoOpWhenNoRecipients(t *testing.T) {
	a := NewEmailAlerter(nil, 1*time.Second)
	if err := a.Alert(nil, SeverityInfo, "title", "body"); err != nil {
		t.Fatalf("no-recipients Alert should return nil, got %v", err)
	}
}

func TestEmailAlerterRecipientsTrimmed(t *testing.T) {
	a := NewEmailAlerter([]string{" ops@example.com ", "", "alt@example.com"}, 1*time.Second)
	if len(a.recipients) != 2 {
		t.Fatalf("expected 2 recipients, got %d: %v", len(a.recipients), a.recipients)
	}
	if a.recipients[0] != "ops@example.com" {
		t.Fatalf("expected trimmed first recipient, got %q", a.recipients[0])
	}
}
