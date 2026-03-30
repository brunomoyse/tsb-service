package auth

import (
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

// hasZitadelPassword checks if a Zitadel user has a password set.
func hasZitadelPassword(userID string) bool {
	respBody, status, err := zitadelRequest("GET", "/v2/users/"+userID, nil)
	if err != nil || status != http.StatusOK {
		return true // Assume has password on error (safe default)
	}
	var userResp struct {
		User struct {
			Human struct {
				PasswordChanged string `json:"passwordChanged"`
			} `json:"human"`
		} `json:"user"`
	}
	if json.Unmarshal(respBody, &userResp) != nil {
		return true
	}
	return userResp.User.Human.PasswordChanged != "" &&
		userResp.User.Human.PasswordChanged != "0001-01-01T00:00:00Z"
}
