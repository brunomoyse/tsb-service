package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// findZitadelUserByEmail searches for a Zitadel user by email. The lookup
// uses TEXT_QUERY_METHOD_EQUALS which is case-sensitive, so the input is
// normalized to lowercase to match how placeholder users are written.
func findZitadelUserByEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	body := map[string]any{
		"queries": []map[string]any{
			{
				"emailQuery": map[string]any{
					"emailAddress": email,
					"method":       "TEXT_QUERY_METHOD_EQUALS",
				},
			},
		},
	}

	respBody, status, err := zitadelRequest("POST", "/v2/users", body)
	if err != nil {
		return "", fmt.Errorf("search users: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("search users returned status %d", status)
	}

	var searchResp struct {
		Result []struct {
			UserID string `json:"userId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &searchResp); err != nil {
		return "", fmt.Errorf("parse search response: %w", err)
	}

	if len(searchResp.Result) > 0 {
		return searchResp.Result[0].UserID, nil
	}
	return "", fmt.Errorf("no user found")
}

// ensureZitadelOtpEmail makes sure the user has the OTP Email factor enrolled
// so that Zitadel will accept an `otpEmail` session challenge.
//
// Zitadel's `POST /v2/users/{userId}/otp_email` is idempotent in spirit: it
// returns 2xx on first enrollment and a 409 / "already exists" error if the
// factor is already there. Both shapes are treated as success. Any other
// failure is returned so the caller can decide whether to abort.
func ensureZitadelOtpEmail(userID string) error {
	respBody, status, err := zitadelRequest("POST", "/v2/users/"+userID+"/otp_email", map[string]any{})
	if err != nil {
		return fmt.Errorf("enroll otp email: %w", err)
	}
	if status >= 200 && status < 300 {
		return nil
	}
	// Zitadel returns 409 when the factor is already configured. Treat as success.
	if status == http.StatusConflict {
		return nil
	}
	// Some Zitadel versions return 400 with an "already exists"-style message
	// instead of 409. Inspect the parsed message rather than the raw body so
	// we don't depend on a specific JSON shape.
	if msg := parseZitadelError(respBody); msg != "" && containsAny(msg, "already", "AlreadyExists") {
		return nil
	}
	return fmt.Errorf("enroll otp email returned status %d: %s", status, parseZitadelError(respBody))
}

// containsAny reports whether s contains any of the provided substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// GetZitadelUserInfo fetches a Zitadel user's profile (email, given name, family name)
// by their Zitadel user ID. Used by the OIDC middleware to enrich JIT provisioning
// when JWT access tokens don't include profile claims (which is the case for
// locally-validated Zitadel JWTs — see zitadel-go SDK oauth.WithJWT).
func GetZitadelUserInfo(_ context.Context, userID string) (email, givenName, familyName string, err error) {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("fetch user: %w", err)
	}
	if status != http.StatusOK {
		return "", "", "", fmt.Errorf("fetch user returned status %d", status)
	}

	var userResp struct {
		User struct {
			Human struct {
				Profile struct {
					GivenName  string `json:"givenName"`
					FamilyName string `json:"familyName"`
				} `json:"profile"`
				Email struct {
					Email string `json:"email"`
				} `json:"email"`
			} `json:"human"`
		} `json:"user"`
	}
	if err := json.Unmarshal(respBody, &userResp); err != nil {
		return "", "", "", fmt.Errorf("parse user response: %w", err)
	}

	return userResp.User.Human.Email.Email,
		userResp.User.Human.Profile.GivenName,
		userResp.User.Human.Profile.FamilyName,
		nil
}

// DeleteZitadelUser permanently removes a user from Zitadel by their Zitadel
// user ID. Used by account deletion (GDPR / App Store guideline 5.1.1) so the
// identity can no longer authenticate. Uses the admin PAT (user management
// permission). A 404 is treated as success, so the call is idempotent and safe
// to retry after a partial failure.
func DeleteZitadelUser(_ context.Context, userID string) error {
	respBody, status, err := zitadelAdminRequest("DELETE", "/v2/users/"+userID, nil)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return fmt.Errorf("delete user returned status %d: %s", status, respBody)
	}
	return nil
}

// placeholderProfileMarker is the sentinel value stored in givenName/familyName
// when a Zitadel user is provisioned without a real name (Pattern B identifier-
// first signup). Pure "-" is short and unlikely to collide with a legitimate
// human name. The verify handler uses this marker to tell the frontend that
// the user must complete their profile before OIDC finalize.
const placeholderProfileMarker = "-"

// createPlaceholderZitadelUser provisions a Zitadel human user without a real
// name, used by the OTP request handler when an unknown email tries to log in.
// Email is marked verified because completing the OTP itself proves the user
// controls the address.
//
// Returns the new Zitadel user ID. Errors out if Zitadel rejects the create
// (e.g. quota / instance misconfiguration); the caller should treat that as a
// silent failure and return the enumeration-resistant empty session response.
func createPlaceholderZitadelUser(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	body := map[string]any{
		"userName": email,
		"profile": map[string]any{
			"givenName":  placeholderProfileMarker,
			"familyName": placeholderProfileMarker,
		},
		"email": map[string]any{
			"email":      email,
			"isVerified": true,
		},
	}

	respBody, status, err := zitadelAdminRequest("POST", "/v2/users/human", body)
	if err != nil {
		return "", fmt.Errorf("create placeholder user: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", fmt.Errorf("create placeholder user returned status %d: %s", status, parseZitadelError(respBody))
	}

	var resp struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse placeholder user response: %w", err)
	}
	if resp.UserID == "" {
		return "", fmt.Errorf("placeholder user response missing userId")
	}
	return resp.UserID, nil
}

// waitForZitadelUserProjection blocks until a freshly-created Zitadel user is
// visible to the query side, absorbing Zitadel's CQRS read-after-write lag.
//
// Zitadel commits user creation to the event store and returns the new userId
// immediately, but the `users` query projection that POST /v2/sessions reads to
// validate `checks.user.userId` updates asynchronously. For a brand-new user the
// session call can therefore race the projection and fail with a spurious
// NotFound (HTTP 404, QUERY-Dfbg2 "User could not be found"), which surfaces to
// the client as "Authentication failed". Polling GET /v2/users/{id} until it
// returns 200 confirms the exact projection the session check depends on has
// caught up — observed lag is tens of milliseconds, so a short bounded poll is
// enough.
//
// Best-effort: on timeout it returns an error, but the caller should still
// proceed — the session attempt is then no worse off than without the wait.
//
// Cadence is held in package vars (not consts) so tests can shrink the delay
// and attempt count; production keeps 10 × 150ms ≈ 1.35s, comfortably above the
// tens-of-ms lag observed in prod.
var (
	userProjectionPollAttempts = 10
	userProjectionPollDelay    = 150 * time.Millisecond
)

func waitForZitadelUserProjection(userID string) error {
	for i := 0; i < userProjectionPollAttempts; i++ {
		if i > 0 {
			time.Sleep(userProjectionPollDelay)
		}
		_, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
		if err == nil && status == http.StatusOK {
			return nil
		}
	}
	return fmt.Errorf("user %s not visible in query projection after %d attempts", userID, userProjectionPollAttempts)
}

// userNeedsProfileCompletion reports whether the Zitadel user still has the
// placeholder name marker, meaning the OTP request created the account on the
// fly and the user has not yet supplied their real first/last name.
func userNeedsProfileCompletion(userID string) (bool, error) {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
	if err != nil {
		return false, fmt.Errorf("fetch user: %w", err)
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("fetch user returned status %d", status)
	}

	var resp struct {
		User struct {
			Human struct {
				Profile struct {
					GivenName string `json:"givenName"`
				} `json:"profile"`
			} `json:"human"`
		} `json:"user"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return false, fmt.Errorf("parse user response: %w", err)
	}

	return resp.User.Human.Profile.GivenName == placeholderProfileMarker, nil
}

// updateZitadelUserProfile sets a human user's first and last name. Used by
// the complete-profile endpoint to replace the placeholder values stored at
// account creation.
func updateZitadelUserProfile(userID, firstName, lastName string) error {
	body := map[string]any{
		"profile": map[string]any{
			"givenName":  firstName,
			"familyName": lastName,
		},
	}

	respBody, status, err := zitadelAdminRequest("PUT", "/v2/users/human/"+userID, body)
	if err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("update profile returned status %d: %s", status, parseZitadelError(respBody))
	}
	return nil
}

// lookupSessionUserID resolves a Zitadel session to its associated user ID.
// Used by the verify and complete-profile handlers to map a session back to
// the user whose profile they need to inspect or update.
func lookupSessionUserID(sessionID string) (string, error) {
	respBody, status, err := zitadelRequest("GET", "/v2/sessions/"+sessionID, nil)
	if err != nil {
		return "", fmt.Errorf("fetch session: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("fetch session returned status %d", status)
	}

	var resp struct {
		Session struct {
			Factors struct {
				User struct {
					ID string `json:"id"`
				} `json:"user"`
			} `json:"factors"`
		} `json:"session"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse session response: %w", err)
	}
	if resp.Session.Factors.User.ID == "" {
		return "", fmt.Errorf("session response missing user id")
	}
	return resp.Session.Factors.User.ID, nil
}

