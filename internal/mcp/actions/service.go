// Package actions holds every write the MCP server can perform: validation,
// before-state capture, execution against tsb-service, and the inverse used by
// undo. Low-risk actions are applied immediately; sensitive ones are stored as
// pending changes and only applied through the internal API.
package actions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

// Risk classifies an action.
type Risk string

const (
	RiskLow       Risk = "low"
	RiskSensitive Risk = "sensitive"
)

// UndoWindow is how far back undo_last_change looks.
const UndoWindow = 30 * time.Minute

// UserError is an error whose message is meant for the owner.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Userf builds a UserError.
func Userf(format string, a ...any) error { return &UserError{Msg: fmt.Sprintf(format, a...)} }

// Sentinel errors for the internal API.
var (
	ErrNotFound    = errors.New("change not found")
	ErrNotPending  = errors.New("change is not pending")
	ErrExpired     = errors.New("change expired")
	ErrConflict    = errors.New("state changed since the proposal")
	ErrNotUndoable = errors.New("not undoable")
)

// ConflictError explains which fields changed upstream.
type ConflictError struct{ Fields []string }

func (e *ConflictError) Error() string {
	return "The item was changed elsewhere since this was proposed (" + strings.Join(e.Fields, ", ") + "). Please propose the change again."
}
func (e *ConflictError) Unwrap() error { return ErrConflict }

// StatusError reports a change that cannot be applied or rejected.
type StatusError struct{ Status changes.Status }

func (e *StatusError) Error() string {
	switch e.Status {
	case changes.StatusExpired:
		return "This change has expired. Please propose it again."
	case changes.StatusApplied:
		return "This change was already applied."
	case changes.StatusRejected:
		return "This change was already rejected."
	default:
		return "This change can no longer be applied (status: " + string(e.Status) + ")."
	}
}
func (e *StatusError) Unwrap() error {
	if e.Status == changes.StatusExpired {
		return ErrExpired
	}
	return ErrNotPending
}

// Env is what handlers need.
type Env struct {
	Up          *upstream.Client
	Loc         *time.Location
	PriceMaxPct int
	Now         func() time.Time
}

// prepared is the result of validating an action against the current state.
type prepared struct {
	EntityType string
	EntityID   string
	Before     any
	Summary    string
	SummaryZh  string
	NoOp       bool
}

// handler is the untyped view of an action kind.
type handler interface {
	kind() string
	risk() Risk
	prepare(ctx context.Context, env *Env, params json.RawMessage) (*prepared, json.RawMessage, error)
	execute(ctx context.Context, env *Env, params json.RawMessage, blob []byte) (any, error)
	inverse(params, before, after json.RawMessage) (string, any, error)
}

// spec is a typed action definition.
type spec[P any, B any] struct {
	Kind    string
	Risk    Risk
	Prepare func(ctx context.Context, env *Env, p *P) (*prepared, error)
	Execute func(ctx context.Context, env *Env, p P, blob []byte) (any, error)
	// Inverse returns the action that restores the before state. Nil means the
	// action cannot be undone.
	Inverse func(p P, before B, after json.RawMessage) (string, any, error)
}

func (s spec[P, B]) kind() string { return s.Kind }
func (s spec[P, B]) risk() Risk   { return s.Risk }

func (s spec[P, B]) prepare(ctx context.Context, env *Env, raw json.RawMessage) (*prepared, json.RawMessage, error) {
	var p P
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil, fmt.Errorf("decode %s params: %w", s.Kind, err)
	}
	pr, err := s.Prepare(ctx, env, &p)
	if err != nil {
		return nil, nil, err
	}
	// Prepare may normalise params (e.g. drop no-op items); persist that form.
	norm, err := json.Marshal(p)
	if err != nil {
		return nil, nil, err
	}
	return pr, norm, nil
}

func (s spec[P, B]) execute(ctx context.Context, env *Env, raw json.RawMessage, blob []byte) (any, error) {
	var p P
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode %s params: %w", s.Kind, err)
	}
	return s.Execute(ctx, env, p, blob)
}

func (s spec[P, B]) inverse(rawP, rawB, after json.RawMessage) (string, any, error) {
	if s.Inverse == nil {
		return "", nil, ErrNotUndoable
	}
	var p P
	var b B
	if err := json.Unmarshal(rawP, &p); err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(rawB, &b); err != nil {
		return "", nil, err
	}
	return s.Inverse(p, b, after)
}

// Service runs actions and owns the pending-change lifecycle.
type Service struct {
	env      *Env
	store    *changes.Store
	ttl      time.Duration
	log      *slog.Logger
	mu       sync.Mutex
	handlers map[string]handler
}

// NewService wires the action registry.
func NewService(env *Env, store *changes.Store, ttl time.Duration, log *slog.Logger) *Service {
	if env.Now == nil {
		env.Now = time.Now
	}
	s := &Service{env: env, store: store, ttl: ttl, log: log, handlers: map[string]handler{}}
	for _, h := range registry() {
		s.handlers[h.kind()] = h
	}
	return s
}

// Env exposes the environment to the tools layer.
func (s *Service) Env() *Env { return s.env }

// Store exposes the store to the tools layer (reads only).
func (s *Service) Store() *changes.Store { return s.store }

// RiskOf returns the risk class of a kind.
func (s *Service) RiskOf(kind string) Risk { return s.handlers[kind].risk() }

// Proposal is returned by propose_* tools.
type Proposal struct {
	ChangeID  string `json:"change_id,omitempty"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	SummaryZh string `json:"summary_zh"`
	ExpiresAt string `json:"expires_at,omitempty"`
	NoOp      bool   `json:"no_op,omitempty"`
}

// Result is returned by actions applied immediately.
type Result struct {
	Applied    bool            `json:"applied"`
	NoOp       bool            `json:"no_op"`
	Summary    string          `json:"summary"`
	SummaryZh  string          `json:"summary_zh"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	AuditID    int64           `json:"audit_id,omitempty"`
}

func proposalOf(c *changes.Change, loc *time.Location) *Proposal {
	return &Proposal{ChangeID: c.ID, Status: string(c.Status), Summary: c.Summary, SummaryZh: c.SummaryZh, ExpiresAt: c.ExpiresAt.In(loc).Format(time.RFC3339)}
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "chg_" + hex.EncodeToString(b[:])
}

func hashOf(v json.RawMessage) string {
	sum := sha256.Sum256(v)
	return hex.EncodeToString(sum[:])
}

func (s *Service) handler(kind string) (handler, error) {
	h, ok := s.handlers[kind]
	if !ok {
		return nil, fmt.Errorf("unknown action kind %q", kind)
	}
	return h, nil
}

func marshal(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	b, err := json.Marshal(v)
	return b, err
}

// Propose validates a change and stores it as pending. It never touches
// upstream state.
func (s *Service) Propose(ctx context.Context, tool, kind string, params any, blob []byte, requestContext string, undoOf *int64) (*Proposal, error) {
	h, err := s.handler(kind)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	pr, norm, err := h.prepare(ctx, s.env, raw)
	if err != nil {
		return nil, err
	}
	if pr.NoOp {
		return &Proposal{Status: "no_op", Summary: pr.Summary, SummaryZh: pr.SummaryZh, NoOp: true}, nil
	}
	before, err := marshal(pr.Before)
	if err != nil {
		return nil, err
	}
	now := s.env.Now().UTC()
	c := &changes.Change{
		ID: newID(), Tool: tool, Kind: kind, EntityType: pr.EntityType, EntityID: pr.EntityID,
		Params: norm, Before: before, BeforeHash: hashOf(before), Blob: blob, Summary: pr.Summary, SummaryZh: pr.SummaryZh,
		RequestContext: requestContext, Status: changes.StatusPending, UndoOf: undoOf,
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	if err := s.store.CreateChange(ctx, c); err != nil {
		return nil, err
	}
	s.log.Info("change proposed", "change_id", c.ID, "kind", kind, "entity_id", c.EntityID)
	return proposalOf(c, s.env.Loc), nil
}

// ApplyNow applies a low-risk action immediately (or an undo of a low-risk
// change, whatever the inverse kind). Identical repeated calls are no-ops.
func (s *Service) ApplyNow(ctx context.Context, source, kind string, params any, requestContext string, undoOf *int64) (*Result, error) {
	h, err := s.handler(kind)
	if err != nil {
		return nil, err
	}
	if h.risk() != RiskLow && undoOf == nil {
		return nil, fmt.Errorf("action %s is sensitive and must be proposed", kind)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pr, norm, err := h.prepare(ctx, s.env, raw)
	if err != nil {
		return nil, err
	}
	before, err := marshal(pr.Before)
	if err != nil {
		return nil, err
	}
	res := &Result{Summary: pr.Summary, SummaryZh: pr.SummaryZh, EntityType: pr.EntityType, EntityID: pr.EntityID, Before: before}
	if pr.NoOp {
		res.NoOp = true
		return res, nil
	}

	risk := h.risk()
	if undoOf != nil {
		risk = RiskLow
	}
	afterV, execErr := h.execute(ctx, s.env, norm, nil)
	after, _ := marshal(afterV)
	entry := &changes.AuditEntry{
		At: s.env.Now().UTC(), Source: source, Kind: kind, Risk: string(risk), EntityType: pr.EntityType, EntityID: pr.EntityID,
		Params: norm, Before: before, After: after, Summary: pr.Summary, SummaryZh: pr.SummaryZh, RequestContext: requestContext, Outcome: changes.OutcomeApplied, UndoOf: undoOf,
	}
	if execErr != nil {
		entry.Outcome, entry.Error, entry.After = changes.OutcomeFailed, execErr.Error(), nil
	}
	id, err := s.store.AppendAudit(ctx, entry)
	if err != nil {
		s.log.Error("audit write failed", "error", err, "kind", kind)
	}
	if execErr != nil {
		return nil, execErr
	}
	if undoOf != nil {
		if err := s.store.MarkUndone(ctx, *undoOf, id); err != nil {
			s.log.Error("mark undone failed", "error", err)
		}
	}
	res.Applied, res.After, res.AuditID = true, after, id
	s.log.Info("change applied", "kind", kind, "entity_id", pr.EntityID, "audit_id", id)
	return res, nil
}

// GetChange loads a change, marking it expired when its TTL passed.
func (s *Service) GetChange(ctx context.Context, id string) (*changes.Change, error) {
	c, err := s.store.GetChange(ctx, id)
	if errors.Is(err, changes.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	now := s.env.Now().UTC()
	if c.Status == changes.StatusPending && !now.Before(c.ExpiresAt) {
		if ok, _ := s.store.TransitionChange(ctx, id, changes.StatusPending, changes.StatusExpired, "", now); ok {
			c.Status, c.DecidedAt = changes.StatusExpired, &now
		}
	}
	return c, nil
}

func (s *Service) auditDecision(ctx context.Context, source string, c *changes.Change, outcome, errMsg string, after json.RawMessage, requestContext string) int64 {
	rc := requestContext
	if rc == "" {
		rc = c.RequestContext
	}
	id, err := s.store.AppendAudit(ctx, &changes.AuditEntry{
		At: s.env.Now().UTC(), Source: source, ChangeID: c.ID, Kind: c.Kind, Risk: string(s.RiskOf(c.Kind)),
		EntityType: c.EntityType, EntityID: c.EntityID, Params: c.Params, Before: c.Before, After: after,
		Summary: c.Summary, SummaryZh: c.SummaryZh, RequestContext: rc, Outcome: outcome, Error: errMsg, UndoOf: c.UndoOf,
	})
	if err != nil {
		s.log.Error("audit write failed", "error", err, "change_id", c.ID)
	}
	return id
}

// Endpoint names used as audit sources.
const (
	SourceApply  = "POST /internal/changes/{id}/apply"
	SourceReject = "POST /internal/changes/{id}/reject"
)

// ApplyPending applies a pending change after re-checking the upstream state.
func (s *Service) ApplyPending(ctx context.Context, id, requestContext string) (*changes.Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.GetChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != changes.StatusPending {
		if c.Status == changes.StatusExpired {
			s.auditDecision(ctx, SourceApply, c, changes.OutcomeExpired, "expired", nil, requestContext)
		}
		return c, &StatusError{Status: c.Status}
	}
	h, err := s.handler(c.Kind)
	if err != nil {
		return nil, err
	}
	now := s.env.Now().UTC()

	pr, _, err := h.prepare(ctx, s.env, c.Params)
	var conflict *ConflictError
	if err == nil {
		current, _ := marshal(pr.Before)
		if hashOf(current) != c.BeforeHash {
			conflict = &ConflictError{Fields: diffFields(c.Before, current)}
		}
	} else {
		var ue *UserError
		if errors.As(err, &ue) || upstream.IsNotFound(err) {
			conflict = &ConflictError{Fields: []string{err.Error()}}
		} else {
			return c, err // upstream unavailable: leave pending so it can be retried
		}
	}
	if conflict != nil {
		_, _ = s.store.TransitionChange(ctx, id, changes.StatusPending, changes.StatusConflict, conflict.Error(), now)
		s.auditDecision(ctx, SourceApply, c, changes.OutcomeConflict, conflict.Error(), nil, requestContext)
		c.Status, c.Error = changes.StatusConflict, conflict.Error()
		return c, conflict
	}

	afterV, execErr := h.execute(ctx, s.env, c.Params, c.Blob)
	if execErr != nil {
		_, _ = s.store.TransitionChange(ctx, id, changes.StatusPending, changes.StatusFailed, execErr.Error(), now)
		s.auditDecision(ctx, SourceApply, c, changes.OutcomeFailed, execErr.Error(), nil, requestContext)
		c.Status, c.Error = changes.StatusFailed, execErr.Error()
		return c, execErr
	}
	after, _ := marshal(afterV)
	if ok, err := s.store.TransitionChange(ctx, id, changes.StatusPending, changes.StatusApplied, "", now); err != nil || !ok {
		s.log.Error("change applied upstream but status update failed", "change_id", id, "error", err)
	}
	auditID := s.auditDecision(ctx, SourceApply, c, changes.OutcomeApplied, "", after, requestContext)
	if c.UndoOf != nil {
		if err := s.store.MarkUndone(ctx, *c.UndoOf, auditID); err != nil {
			s.log.Error("mark undone failed", "error", err)
		}
	}
	c.Status, c.DecidedAt = changes.StatusApplied, &now
	s.log.Info("pending change applied", "change_id", id, "kind", c.Kind)
	return c, nil
}

// Reject marks a pending change as rejected.
func (s *Service) Reject(ctx context.Context, id, requestContext string) (*changes.Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.GetChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != changes.StatusPending {
		return c, &StatusError{Status: c.Status}
	}
	now := s.env.Now().UTC()
	if ok, err := s.store.TransitionChange(ctx, id, changes.StatusPending, changes.StatusRejected, "", now); err != nil {
		return nil, err
	} else if !ok {
		c, _ = s.GetChange(ctx, id)
		return c, &StatusError{Status: c.Status}
	}
	s.auditDecision(ctx, SourceReject, c, changes.OutcomeRejected, "", nil, requestContext)
	c.Status, c.DecidedAt = changes.StatusRejected, &now
	return c, nil
}

// UndoResult is returned by undo_last_change.
type UndoResult struct {
	Mode       string    `json:"mode"` // "applied", "pending" or "no_op"
	Undone     string    `json:"undone_summary"`
	UndoneZh   string    `json:"undone_summary_zh"`
	UndoneAt   string    `json:"undone_change_at"`
	Summary    string    `json:"summary"`
	SummaryZh  string    `json:"summary_zh"`
	Applied    *Result   `json:"applied,omitempty"`
	Proposal   *Proposal `json:"proposal,omitempty"`
	UndoneRisk string    `json:"undone_risk"`
}

// Undo reverts the most recent applied change younger than UndoWindow.
func (s *Service) Undo(ctx context.Context, requestContext string) (*UndoResult, error) {
	now := s.env.Now().UTC()
	entry, err := s.store.LastUndoable(ctx, now.Add(-UndoWindow))
	if errors.Is(err, changes.ErrNotFound) {
		return nil, Userf("There is no change from the last %d minutes to undo.", int(UndoWindow.Minutes()))
	}
	if err != nil {
		return nil, err
	}
	h, err := s.handler(entry.Kind)
	if err != nil {
		return nil, err
	}
	invKind, invParams, err := h.inverse(entry.Params, entry.Before, entry.After)
	if errors.Is(err, ErrNotUndoable) {
		return nil, Userf("The last change (%s) cannot be undone automatically.", entry.Summary)
	}
	if err != nil {
		var ue *UserError
		if errors.As(err, &ue) {
			return nil, err
		}
		return nil, fmt.Errorf("compute undo: %w", err)
	}
	out := &UndoResult{Undone: entry.Summary, UndoneZh: entry.SummaryZh, UndoneAt: entry.At.In(s.env.Loc).Format(time.RFC3339), UndoneRisk: entry.Risk}

	if Risk(entry.Risk) == RiskLow {
		res, err := s.ApplyNow(ctx, "undo_last_change", invKind, invParams, requestContext, &entry.ID)
		if err != nil {
			return nil, err
		}
		if res.NoOp {
			out.Mode, out.Summary, out.SummaryZh = "no_op", "Nothing to undo: "+res.Summary, "无需撤销："+res.SummaryZh
			return out, nil
		}
		out.Mode, out.Applied, out.Summary, out.SummaryZh = "applied", res, "Undone: "+res.Summary, "已撤销："+res.SummaryZh
		return out, nil
	}

	if existing, err := s.store.PendingUndoFor(ctx, entry.ID, now); err == nil && existing != "" {
		c, err := s.GetChange(ctx, existing)
		if err == nil {
			out.Mode = "pending"
			out.Proposal = proposalOf(c, s.env.Loc)
			out.Summary = "An undo is already waiting for confirmation: " + c.Summary
			out.SummaryZh = "已有撤销操作等待确认：" + c.SummaryZh
			return out, nil
		}
	}
	p, err := s.Propose(ctx, "undo_last_change", invKind, invParams, nil, requestContext, &entry.ID)
	if err != nil {
		return nil, err
	}
	if p.NoOp {
		out.Mode, out.Summary, out.SummaryZh = "no_op", "Nothing to undo: "+p.Summary, "无需撤销："+p.SummaryZh
		return out, nil
	}
	out.Mode, out.Proposal, out.Summary, out.SummaryZh = "pending", p, "Undo needs confirmation: "+p.Summary, "撤销需要确认："+p.SummaryZh
	return out, nil
}

// diffFields lists the top-level keys whose values differ between two JSON
// objects (or "state" when they are not objects).
func diffFields(a, b json.RawMessage) []string {
	var ma, mb map[string]json.RawMessage
	if json.Unmarshal(a, &ma) != nil || json.Unmarshal(b, &mb) != nil {
		return []string{"state"}
	}
	seen := map[string]bool{}
	var out []string
	for k, v := range ma {
		seen[k] = true
		if string(mb[k]) != string(v) {
			out = append(out, k)
		}
	}
	for k := range mb {
		if !seen[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = []string{"state"}
	}
	return out
}
