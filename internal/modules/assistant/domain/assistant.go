// Package domain holds the types of the Telegram assistant connection. The
// assistant itself (tsb-agent) runs as a separate service; tsb-service only
// shows its Telegram bot and linked accounts in the dashboard, links new
// accounts through it and alerts the owner when Telegram refuses the bot.
package domain

import (
	"context"
	"errors"
	"time"
)

// Connection states. StateUnavailable means the agent did not answer.
const (
	StateReady         = "ready"
	StateNoAccounts    = "no_accounts"
	StateTokenRejected = "token_rejected"
	StateConflict      = "conflict"
	StateUnavailable   = "unavailable"
)

// Link statuses, as reported by the agent.
const (
	LinkWait        = "wait"
	LinkLinked      = "linked"
	LinkExpired     = "expired"
	LinkRefusedFull = "refused_full"
)

// Account is a Telegram account linked to the assistant.
type Account struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Username string    `json:"username,omitempty"`
	LinkedAt time.Time `json:"linked_at"`
}

// Connection is the state of the assistant's Telegram bot.
type Connection struct {
	State          string     `json:"state"`
	BotUsername    string     `json:"bot_username,omitempty"`
	Accounts       []Account  `json:"accounts"`
	MaxAccounts    int        `json:"max_accounts"`
	Since          *time.Time `json:"since,omitempty"`
	ProblemAt      *time.Time `json:"problem_at,omitempty"`
	LinkInProgress bool       `json:"link_in_progress"`
}

// Link is a one-time link that binds a Telegram account to the assistant.
// URL is a t.me deep link, meant to be shown as a QR code or a button.
type Link struct {
	ID        string    `json:"link_id"`
	Status    string    `json:"status"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Account   *Account  `json:"account,omitempty"`
}

// Errors.
var (
	// ErrDisabled: no agent is configured (ASSISTANT_AGENT_URL unset).
	ErrDisabled = errors.New("the Telegram assistant is not configured")
	// ErrUnavailable: the agent did not answer.
	ErrUnavailable = errors.New("the Telegram assistant is unavailable")
	// ErrUnknownLink: the link id is unknown or was purged.
	ErrUnknownLink = errors.New("unknown link")
	// ErrUnknownAccount: the account is not linked.
	ErrUnknownAccount = errors.New("unknown account")
	// ErrLimitReached: the maximum number of accounts is already linked.
	ErrLimitReached = errors.New("the assistant already has the maximum number of accounts")
	// ErrBotNotReady: the agent has no working Telegram bot yet.
	ErrBotNotReady = errors.New("the Telegram bot is not ready")
)

// AgentClient talks to the agent's admin API.
type AgentClient interface {
	Connection(ctx context.Context) (Connection, error)
	StartLink(ctx context.Context) (Link, error)
	LinkStatus(ctx context.Context, id string) (Link, error)
	RemoveAccount(ctx context.Context, id string) (Connection, error)
}
