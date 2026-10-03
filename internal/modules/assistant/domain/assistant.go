// Package domain holds the types of the WeChat assistant connection. The
// assistant itself (tsb-agent) runs as a separate service; tsb-service only
// shows its WeChat connection in the dashboard, runs the QR login through it
// and alerts the owner when the session expires.
package domain

import (
	"context"
	"errors"
	"time"
)

// Connection states. StateUnavailable means the agent did not answer.
const (
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
	StateExpired      = "expired"
	StateUnavailable  = "unavailable"
)

// Login statuses, as reported by the agent.
const (
	LoginWait      = "wait"
	LoginScanned   = "scanned"
	LoginConfirmed = "confirmed"
	LoginExpired   = "expired"
	LoginRefused   = "refused_other_account"
	LoginFailed    = "failed"
	LoginCancelled = "cancelled"
)

// Connection is the state of the assistant's WeChat session.
type Connection struct {
	State           string     `json:"state"`
	Account         string     `json:"account,omitempty"`
	Since           *time.Time `json:"since,omitempty"`
	ExpiredAt       *time.Time `json:"expired_at,omitempty"`
	EverConnected   bool       `json:"ever_connected"`
	LoginInProgress bool       `json:"login_in_progress"`
}

// Login is a QR login in progress. QRContent is the text to encode as a QR
// code (a WeChat URL).
type Login struct {
	ID        string    `json:"login_id"`
	Status    string    `json:"status"`
	QRContent string    `json:"qr_content"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Errors.
var (
	// ErrDisabled: no agent is configured (ASSISTANT_AGENT_URL unset).
	ErrDisabled = errors.New("the WeChat assistant is not configured")
	// ErrUnavailable: the agent did not answer.
	ErrUnavailable = errors.New("the WeChat assistant is unavailable")
	// ErrUnknownLogin: the login id is unknown or was replaced.
	ErrUnknownLogin = errors.New("unknown or replaced login")
)

// AgentClient talks to the agent's admin API.
type AgentClient interface {
	Connection(ctx context.Context) (Connection, error)
	StartLogin(ctx context.Context, replaceOwner bool) (Login, error)
	LoginStatus(ctx context.Context, id string) (Login, error)
	Disconnect(ctx context.Context) (Connection, error)
}
