package auth

import (
	"encoding/json"
	"strings"

	"go.uber.org/zap"
)

// Frontend-facing error codes returned by auth handlers.
const (
	ErrNoPassword          = "no_password"
	ErrWrongPassword       = "wrong_password"
	ErrWeakPassword        = "weak_password"
	ErrPasswordChangeFailed = "password_change_failed"
	ErrPasswordResetFailed  = "password_reset_failed"
	ErrInvalidCode         = "invalid_code"
	ErrEmailAlreadyExists  = "email_already_exists"
	ErrRegistrationFailed  = "registration_failed"
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

// mapSessionError maps a Zitadel session creation error to a frontend error code.
// Returns the error code and a user-friendly message, or empty strings if unmapped.
func mapSessionError(msg string) (code string, message string) {
	switch {
	case strings.Contains(msg, "not set a password"),
		strings.Contains(msg, "COMMAND-3nJ4t"):
		return ErrNoPassword, "This account uses social login. Please sign in with Google or Apple."
	default:
		return "", ""
	}
}

// mapPasswordError maps a Zitadel password-related error to a frontend error code.
func mapPasswordError(msg string) string {
	switch {
	case strings.Contains(msg, "password invalid"),
		strings.Contains(msg, "COMMAND-3M0fs"):
		return ErrWrongPassword
	case strings.Contains(msg, "complexity"),
		strings.Contains(msg, "COMMAND-oz74F"):
		return ErrWeakPassword
	default:
		zap.L().Warn("unmapped zitadel password error", zap.String("message", msg))
		return ErrPasswordChangeFailed
	}
}

// mapPasswordResetError maps a Zitadel password reset error to a frontend error code.
func mapPasswordResetError(msg string) string {
	switch {
	case strings.Contains(msg, "complexity"),
		strings.Contains(msg, "COMMAND-oz74F"):
		return ErrWeakPassword
	case strings.Contains(msg, "invalid"),
		strings.Contains(msg, "expired"),
		strings.Contains(msg, "COMMAND-3M0fs"):
		return ErrInvalidCode
	default:
		zap.L().Warn("unmapped zitadel password reset error", zap.String("message", msg))
		return ErrPasswordResetFailed
	}
}

// isWeakPasswordError checks if a Zitadel error indicates password complexity failure.
func isWeakPasswordError(msg string) bool {
	return strings.Contains(msg, "complexity") || strings.Contains(msg, "COMMAND-oz74F")
}
