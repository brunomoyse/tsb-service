package auth

import (
	"encoding/json"
)

// Frontend-facing error codes returned by auth handlers.
const (
	ErrInvalidCode = "invalid_code"
)

// zitadelError represents an error response from the Zitadel API.
type zitadelError struct {
	Message string `json:"message"`
}

// parseZitadelError extracts the error message from a Zitadel API response body.
func parseZitadelError(body []byte) string {
	var zErr zitadelError
	if json.Unmarshal(body, &zErr) != nil {
		return ""
	}
	return zErr.Message
}
