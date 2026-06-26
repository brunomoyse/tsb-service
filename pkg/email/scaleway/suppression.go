package scaleway

import (
	"context"
	"strings"
	"time"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"go.uber.org/zap"
)

// SuppressionStore records and reports email addresses that must not be emailed
// again (prior hard bounces / unknown mailboxes / blocklist hits). It is injected
// at startup via SetSuppressionStore; when nil, suppression is simply disabled.
type SuppressionStore interface {
	IsSuppressed(ctx context.Context, email string) (bool, error)
	Suppress(ctx context.Context, email, reason string) error
}

var suppressionStore SuppressionStore

// SetSuppressionStore installs the store consulted by dispatch() before sending
// and written to by the bounce poller. Call once during startup.
func SetSuppressionStore(s SuppressionStore) { suppressionStore = s }

// isSuppressed reports whether email is on the suppression list. It fails OPEN:
// if no store is configured or the lookup errors, the address is treated as
// sendable so a suppression-store hiccup never blocks transactional mail.
func isSuppressed(email string) bool {
	if suppressionStore == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ok, err := suppressionStore.IsSuppressed(ctx, normalizeEmail(email))
	if err != nil {
		zap.L().Warn("email suppression lookup failed; sending anyway", zap.String("email", email), zap.Error(err))
		return false
	}
	return ok
}

// filterSuppressedRecipients returns req.To with suppressed addresses removed.
// The result uses a fresh backing array so the caller's slice is never mutated.
// Returns false when every recipient was suppressed (nothing to send).
func filterSuppressedRecipients(req *temv1alpha1.CreateEmailRequest) (kept []*temv1alpha1.CreateEmailRequestAddress, hasRecipients bool) {
	if len(req.To) == 0 {
		return req.To, true
	}
	kept = make([]*temv1alpha1.CreateEmailRequestAddress, 0, len(req.To))
	for _, addr := range req.To {
		if addr != nil && isSuppressed(addr.Email) {
			zap.L().Info("skipping email to suppressed address", zap.String("email", addr.Email))
			continue
		}
		kept = append(kept, addr)
	}
	return kept, len(kept) > 0
}

// normalizeEmail lowercases and trims an address so suppression matching is
// consistent with how recipients are stored elsewhere.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
