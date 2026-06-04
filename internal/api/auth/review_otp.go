package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

/*
 * Google Play / App Store review access for passwordless (email-OTP) login.
 *
 * App reviewers cannot receive the OTP email — they have no access to the test
 * mailbox. For a dedicated review account we therefore (1) suppress the email,
 * and (2) keep the latest code in memory so it can be fetched over a static URL
 * the reviewer is given in the store listing's "login instructions" field.
 *
 * Both halves are opt-in and default-off:
 *   REVIEW_OTP_LOGINS  comma-separated loginNames treated as review accounts
 *   REVIEW_OTP_KEY     shared secret guarding ReviewLastOtpHandler (unset = 404)
 *
 * Unset in production-for-real → this whole feature is inert.
 */

// reviewOtpTTL bounds how long a stashed review code stays retrievable, so a
// stale code can't be replayed long after the reviewer requested it.
const reviewOtpTTL = 10 * time.Minute

var (
	reviewLogins     map[string]struct{}
	reviewLoginsOnce sync.Once
)

// isReviewOtpLogin reports whether loginName is configured as a store-review
// account via REVIEW_OTP_LOGINS (comma-separated, case-insensitive).
func isReviewOtpLogin(loginName string) bool {
	reviewLoginsOnce.Do(func() {
		raw := os.Getenv("REVIEW_OTP_LOGINS")
		if raw == "" {
			return
		}
		reviewLogins = make(map[string]struct{})
		for _, l := range strings.Split(raw, ",") {
			if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
				reviewLogins[l] = struct{}{}
			}
		}
	})
	if reviewLogins == nil {
		return false
	}
	_, ok := reviewLogins[strings.ToLower(strings.TrimSpace(loginName))]
	return ok
}

type reviewOtpEntry struct {
	code     string
	storedAt time.Time
}

var (
	reviewOtpMu    sync.Mutex
	reviewOtpStore = map[string]reviewOtpEntry{}
)

// stashReviewOtp records the latest OTP code for a review login so it can be
// retrieved without mailbox access. No-op for empty codes and any login not in
// REVIEW_OTP_LOGINS, so it is safe to call unconditionally from the OTP flow.
func stashReviewOtp(loginName, code string) {
	if code == "" || !isReviewOtpLogin(loginName) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(loginName))
	reviewOtpMu.Lock()
	reviewOtpStore[key] = reviewOtpEntry{code: code, storedAt: time.Now()}
	reviewOtpMu.Unlock()
}

// ReviewLastOtpHandler returns the most recent OTP code for a configured store
// review account, letting an app reviewer who can't receive the email complete
// passwordless login.
//
// GET /auth/review/last-otp?login=<email>&key=<secret>
//
// Gated by REVIEW_OTP_KEY: unset → the endpoint behaves as if it does not exist
// (404). The login must also be present in REVIEW_OTP_LOGINS.
func ReviewLastOtpHandler(c *gin.Context) {
	secret := os.Getenv("REVIEW_OTP_KEY")
	if secret == "" {
		// Feature disabled: don't reveal that the route exists.
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	key := c.Query("key")
	if subtle.ConstantTimeCompare([]byte(key), []byte(secret)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	login := strings.ToLower(strings.TrimSpace(c.Query("login")))
	if !isReviewOtpLogin(login) {
		c.JSON(http.StatusForbidden, gin.H{"error": "login not enabled for review"})
		return
	}

	reviewOtpMu.Lock()
	entry, ok := reviewOtpStore[login]
	reviewOtpMu.Unlock()

	if !ok || time.Since(entry.storedAt) > reviewOtpTTL {
		c.JSON(http.StatusOK, gin.H{
			"code":    "",
			"message": "no recent code — request a login code in the app first",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":       entry.code,
		"ageSeconds": int(time.Since(entry.storedAt).Seconds()),
	})
}
