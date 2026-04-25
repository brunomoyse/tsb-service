package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// findZitadelUserByEmail searches for a Zitadel user by email.
func findZitadelUserByEmail(email string) (string, error) {
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

// isZitadelEmailVerified checks if a Zitadel user's email is verified.
func isZitadelEmailVerified(userID string) bool {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
	if err != nil || status != http.StatusOK {
		return false // Deny on error (safe default)
	}
	var userResp struct {
		User struct {
			Human struct {
				Email struct {
					IsVerified bool `json:"isVerified"`
				} `json:"email"`
			} `json:"human"`
		} `json:"user"`
	}
	if json.Unmarshal(respBody, &userResp) != nil {
		return false
	}
	return userResp.User.Human.Email.IsVerified
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

