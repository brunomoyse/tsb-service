package scaleway

import (
	"context"
	"testing"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
)

type fakeStore struct {
	suppressed map[string]bool
}

func (f *fakeStore) IsSuppressed(_ context.Context, email string) (bool, error) {
	return f.suppressed[normalizeEmail(email)], nil
}
func (f *fakeStore) Suppress(_ context.Context, email, _ string) error {
	f.suppressed[normalizeEmail(email)] = true
	return nil
}

func TestHasHardBounceFlag(t *testing.T) {
	cases := []struct {
		name  string
		flags []temv1alpha1.EmailFlag
		want  bool
	}{
		{"hard bounce", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}, true},
		{"mailbox not found", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagMailboxNotFound}, true},
		{"blocklisted", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagBlocklisted}, true},
		{"soft bounce is transient", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSoftBounce}, false},
		{"greylisted is transient", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagGreylisted}, false},
		{"mailbox full is transient", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagMailboxFull}, false},
		{"spam is not a hard bounce", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSpam}, false},
		{"no flags", nil, false},
		{"mixed picks hard", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSoftBounce, temv1alpha1.EmailFlagHardBounce}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasHardBounceFlag(tc.flags); got != tc.want {
				t.Fatalf("hasHardBounceFlag(%v) = %v, want %v", tc.flags, got, tc.want)
			}
		})
	}
}

func TestHardBounceReason(t *testing.T) {
	got := hardBounceReason([]temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSoftBounce, temv1alpha1.EmailFlagMailboxNotFound})
	if got != string(temv1alpha1.EmailFlagMailboxNotFound) {
		t.Fatalf("hardBounceReason = %q, want %q", got, temv1alpha1.EmailFlagMailboxNotFound)
	}
}

func TestFilterSuppressedRecipients(t *testing.T) {
	prev := suppressionStore
	t.Cleanup(func() { suppressionStore = prev })

	SetSuppressionStore(&fakeStore{suppressed: map[string]bool{"bounced@example.com": true}})

	addr := func(e string) *temv1alpha1.CreateEmailRequestAddress {
		return &temv1alpha1.CreateEmailRequestAddress{Email: e}
	}

	t.Run("suppressed recipient skips the whole single-recipient send", func(t *testing.T) {
		req := &temv1alpha1.CreateEmailRequest{To: []*temv1alpha1.CreateEmailRequestAddress{addr("Bounced@example.com")}}
		kept, has := filterSuppressedRecipients(req)
		if has || len(kept) != 0 {
			t.Fatalf("expected no recipients, got has=%v kept=%d", has, len(kept))
		}
	})

	t.Run("good recipient is kept", func(t *testing.T) {
		req := &temv1alpha1.CreateEmailRequest{To: []*temv1alpha1.CreateEmailRequestAddress{addr("ok@example.com")}}
		kept, has := filterSuppressedRecipients(req)
		if !has || len(kept) != 1 {
			t.Fatalf("expected 1 recipient kept, got has=%v kept=%d", has, len(kept))
		}
	})

	t.Run("mixed list drops only the suppressed one and does not mutate input", func(t *testing.T) {
		in := []*temv1alpha1.CreateEmailRequestAddress{addr("ok@example.com"), addr("bounced@example.com")}
		req := &temv1alpha1.CreateEmailRequest{To: in}
		kept, has := filterSuppressedRecipients(req)
		if !has || len(kept) != 1 || kept[0].Email != "ok@example.com" {
			t.Fatalf("expected only ok kept, got has=%v kept=%v", has, kept)
		}
		if len(in) != 2 {
			t.Fatalf("input slice was mutated: len=%d", len(in))
		}
	})
}

func TestFilterSuppressedRecipientsNoStoreFailsOpen(t *testing.T) {
	prev := suppressionStore
	t.Cleanup(func() { suppressionStore = prev })
	suppressionStore = nil // no store configured

	req := &temv1alpha1.CreateEmailRequest{To: []*temv1alpha1.CreateEmailRequestAddress{{Email: "anyone@example.com"}}}
	kept, has := filterSuppressedRecipients(req)
	if !has || len(kept) != 1 {
		t.Fatalf("with no store, recipient must be kept (fail-open); got has=%v kept=%d", has, len(kept))
	}
}
