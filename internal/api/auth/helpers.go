package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

