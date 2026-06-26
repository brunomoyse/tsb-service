package scaleway

import (
	"context"
	"fmt"
	"time"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"go.uber.org/zap"
)

// hardBounceFlags are the Scaleway TEM flags that indicate a PERMANENT delivery
// failure. Soft bounces, greylisting, and full mailboxes are transient and are
// intentionally excluded so we don't suppress addresses that may recover.
var hardBounceFlags = map[temv1alpha1.EmailFlag]struct{}{
	temv1alpha1.EmailFlagHardBounce:      {},
	temv1alpha1.EmailFlagMailboxNotFound: {},
	temv1alpha1.EmailFlagBlocklisted:     {},
}

const bouncePollPageSize = 100

// PollHardBounces lists failed emails from Scaleway TEM created since `since` and
// records every permanently-undeliverable recipient in the suppression store, so
// future dispatch() calls skip them. It is a no-op for the SMTP backend or when
// no suppression store is configured. Safe to call repeatedly; Suppress upserts.
func PollHardBounces(ctx context.Context, since time.Time) error {
	if temClient == nil || suppressionStore == nil {
		return nil
	}

	projectID := baseReq.ProjectID
	suppressed := 0
	page := int32(1)
	for {
		resp, err := temClient.ListEmails(&temv1alpha1.ListEmailsRequest{
			Region:    baseReq.Region,
			ProjectID: &projectID,
			Since:     &since,
			Statuses:  []temv1alpha1.EmailStatus{temv1alpha1.EmailStatusFailed},
			Page:      &page,
			PageSize:  scw.Uint32Ptr(bouncePollPageSize),
		}, scw.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("list bounced emails: %w", err)
		}

		for _, e := range resp.Emails {
			if e == nil || !hasHardBounceFlag(e.Flags) {
				continue
			}
			if e.MailRcpt == "" {
				continue
			}
			rcpt := e.MailRcpt
			reason := hardBounceReason(e.Flags)
			if err := suppressionStore.Suppress(ctx, normalizeEmail(rcpt), reason); err != nil {
				zap.L().Warn("failed to record email suppression", zap.String("email", rcpt), zap.Error(err))
				continue
			}
			suppressed++
		}

		if len(resp.Emails) < bouncePollPageSize {
			break
		}
		page++
	}

	if suppressed > 0 {
		zap.L().Info("recorded email suppressions from hard bounces", zap.Int("count", suppressed), zap.Time("since", since))
	}
	return nil
}

func hasHardBounceFlag(flags []temv1alpha1.EmailFlag) bool {
	for _, f := range flags {
		if _, ok := hardBounceFlags[f]; ok {
			return true
		}
	}
	return false
}

func hardBounceReason(flags []temv1alpha1.EmailFlag) string {
	for _, f := range flags {
		if _, ok := hardBounceFlags[f]; ok {
			return string(f)
		}
	}
	return string(temv1alpha1.EmailFlagHardBounce)
}
