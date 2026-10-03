// Package internalapi serves the endpoints the agent service calls when the
// owner confirms or refuses a pending change. It runs on its own listener with
// its own secret and is never exposed as MCP tools or resources.
package internalapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

// ChangeView is the JSON representation of a pending change.
type ChangeView struct {
	ChangeID       string `json:"change_id"`
	Tool           string `json:"tool"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	Summary        string `json:"summary"`
	SummaryZh      string `json:"summary_zh"`
	EntityType     string `json:"entity_type"`
	EntityID       string `json:"entity_id"`
	RequestContext string `json:"request_context,omitempty"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	DecidedAt      string `json:"decided_at,omitempty"`
	Error          string `json:"error,omitempty"`
	IsUndo         bool   `json:"is_undo"`
}

type errorBody struct {
	Error  string      `json:"error"`
	Code   string      `json:"code"`
	Change *ChangeView `json:"change,omitempty"`
}

type decisionBody struct {
	RequestContext string `json:"request_context"`
}

// Handler returns the internal API.
func Handler(svc *actions.Service, token string, loc *time.Location, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, token: []byte(token), loc: loc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.Store().Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("GET /internal/changes/{id}", h.auth(h.get))
	mux.Handle("POST /internal/changes/{id}/apply", h.auth(h.apply))
	mux.Handle("POST /internal/changes/{id}/reject", h.auth(h.reject))
	return mux
}

type handler struct {
	svc   *actions.Service
	token []byte
	loc   *time.Location
	log   *slog.Logger
}

func (h *handler) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(h.token) == 0 || subtle.ConstantTimeCompare([]byte(got), h.token) != 1 {
			h.log.Warn("internal api: rejected token", "path", r.URL.Path, "remote", r.RemoteAddr)
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "invalid or missing token", Code: "unauthorized"})
			return
		}
		next(w, r)
	})
}

func (h *handler) view(c *changes.Change) *ChangeView {
	if c == nil {
		return nil
	}
	v := &ChangeView{ChangeID: c.ID, Tool: c.Tool, Kind: c.Kind, Status: string(c.Status), Summary: c.Summary, SummaryZh: c.SummaryZh, EntityType: c.EntityType, EntityID: c.EntityID,
		RequestContext: c.RequestContext, CreatedAt: c.CreatedAt.In(h.loc).Format(time.RFC3339), ExpiresAt: c.ExpiresAt.In(h.loc).Format(time.RFC3339),
		Error: c.Error, IsUndo: c.UndoOf != nil}
	if c.DecidedAt != nil {
		v.DecidedAt = c.DecidedAt.In(h.loc).Format(time.RFC3339)
	}
	return v
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.GetChange(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(c))
}

func decodeDecision(r *http.Request) decisionBody {
	var b decisionBody
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b)
	}
	return b
}

func (h *handler) apply(w http.ResponseWriter, r *http.Request) {
	b := decodeDecision(r)
	c, err := h.svc.ApplyPending(r.Context(), r.PathValue("id"), b.RequestContext)
	if err != nil {
		h.fail(w, r, c, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(c))
}

func (h *handler) reject(w http.ResponseWriter, r *http.Request) {
	b := decodeDecision(r)
	c, err := h.svc.Reject(r.Context(), r.PathValue("id"), b.RequestContext)
	if err != nil {
		h.fail(w, r, c, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(c))
}

// fail maps errors to HTTP statuses with an owner-friendly message.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, c *changes.Change, err error) {
	var (
		ce *actions.ConflictError
		se *actions.StatusError
		ue *actions.UserError
		up *upstream.Error
	)
	body := errorBody{Change: h.view(c)}
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, actions.ErrNotFound):
		status, body.Code, body.Error = http.StatusNotFound, "not_found", "No pending change with this id."
	case errors.As(err, &se) && errors.Is(err, actions.ErrExpired):
		status, body.Code, body.Error = http.StatusGone, "expired", se.Error()
	case errors.As(err, &se):
		status, body.Code, body.Error = http.StatusConflict, "already_"+string(se.Status), se.Error()
	case errors.As(err, &ce):
		status, body.Code, body.Error = http.StatusConflict, "conflict", ce.Error()
	case errors.As(err, &ue):
		status, body.Code, body.Error = http.StatusUnprocessableEntity, "rejected", ue.Msg
	case errors.As(err, &up):
		status, body.Code, body.Error = http.StatusBadGateway, "upstream", up.Error()
	default:
		body.Code, body.Error = "internal", "Something went wrong while applying the change."
	}
	h.log.Warn("internal api: request failed", "path", r.URL.Path, "status", status, "code", body.Code, "error", err)
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
