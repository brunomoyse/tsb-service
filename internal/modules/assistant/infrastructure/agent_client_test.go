package infrastructure

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tsb-service/internal/modules/assistant/domain"
)

func TestAgentClient(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /admin/connection":
			_, _ = w.Write([]byte(`{"state":"ready","bot_username":"pili_bot","accounts":[{"id":"123","name":"Marie Dupont","username":"marie","linked_at":"2026-10-03T12:00:00Z"}],"max_accounts":3,"since":"2026-10-03T11:00:00Z","link_in_progress":false}`))
		case "POST /admin/links":
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(`{"link_id":"abc","status":"wait","url":"https://t.me/pili_bot?start=xyz","expires_at":"2026-10-03T12:15:00Z"}`))
		case "GET /admin/links/abc":
			_, _ = w.Write([]byte(`{"link_id":"abc","status":"linked","url":"u","expires_at":"2026-10-03T12:15:00Z","account":{"id":"123","name":"Marie Dupont","linked_at":"2026-10-03T12:01:00Z"}}`))
		case "GET /admin/links/gone", "DELETE /admin/accounts/999":
			w.WriteHeader(http.StatusNotFound)
		case "DELETE /admin/accounts/123":
			_, _ = w.Write([]byte(`{"state":"no_accounts","accounts":[],"max_accounts":3}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ctx := t.Context()
	c := NewAgentClient(srv.URL+"/", "secret", nil)

	conn, err := c.Connection(ctx)
	if err != nil || conn.State != domain.StateReady || conn.BotUsername != "pili_bot" || conn.MaxAccounts != 3 || len(conn.Accounts) != 1 || conn.Accounts[0].Username != "marie" || conn.Since == nil || conn.ProblemAt != nil {
		t.Fatalf("connection %+v %v", conn, err)
	}
	lk, err := c.StartLink(ctx)
	if err != nil || lk.ID != "abc" || lk.URL == "" || gotBody != "{}" {
		t.Fatalf("start %+v %v body=%q", lk, err, gotBody)
	}
	if lk, err = c.LinkStatus(ctx, "abc"); err != nil || lk.Status != domain.LinkLinked || lk.Account == nil || lk.Account.ID != "123" {
		t.Fatalf("status %+v %v", lk, err)
	}
	if _, err = c.LinkStatus(ctx, "gone"); !errors.Is(err, domain.ErrUnknownLink) {
		t.Errorf("unknown link: %v", err)
	}
	if conn, err = c.RemoveAccount(ctx, "123"); err != nil || conn.State != domain.StateNoAccounts {
		t.Errorf("remove %+v %v", conn, err)
	}
	if _, err = c.RemoveAccount(ctx, "999"); !errors.Is(err, domain.ErrUnknownAccount) {
		t.Errorf("unknown account: %v", err)
	}

	if _, err := NewAgentClient(srv.URL, "wrong", nil).Connection(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("wrong token: %v", err)
	}
	srv.Close()
	if _, err := c.Connection(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("agent down: %v", err)
	}
}

func TestAgentClientProblems(t *testing.T) {
	serve := func(h http.HandlerFunc) *httptest.Server {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("a 5xx answer is unavailable and names the request", func(t *testing.T) {
		srv := serve(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		_, err := NewAgentClient(srv.URL, "t", nil).Connection(t.Context())
		if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "HTTP 502") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an undecodable body is unavailable, not a half-filled struct", func(t *testing.T) {
		srv := serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
		conn, err := NewAgentClient(srv.URL, "t", nil).Connection(t.Context())
		if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "decode agent response") {
			t.Fatalf("err = %v", err)
		}
		if conn.State != "" {
			t.Fatalf("conn = %+v", conn)
		}
	})

	t.Run("a body that breaks off mid-way is unavailable", func(t *testing.T) {
		srv := serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte(`{"state":`))
		})
		_, err := NewAgentClient(srv.URL, "t", nil).Connection(t.Context())
		if !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an invalid base URL is an error before any request", func(t *testing.T) {
		_, err := NewAgentClient("http://bad host", "t", nil).Connection(t.Context())
		if err == nil || errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("err = %v, want a request-construction error", err)
		}
	})

	t.Run("a start refused for the limit or a bot that is not ready has its own error", func(t *testing.T) {
		for body, want := range map[string]error{
			`{"error":"limit_reached"}`: domain.ErrLimitReached,
			`{"error":"bot_not_ready"}`: domain.ErrBotNotReady,
		} {
			status := http.StatusConflict
			if want == domain.ErrBotNotReady {
				status = http.StatusServiceUnavailable
			}
			srv := serve(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			})
			if _, err := NewAgentClient(srv.URL, "t", nil).StartLink(t.Context()); !errors.Is(err, want) {
				t.Errorf("%s: err = %v", body, err)
			}
		}
		// Any other 409 or 503 is just an unavailable agent.
		srv := serve(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`oops`))
		})
		if _, err := NewAgentClient(srv.URL, "t", nil).StartLink(t.Context()); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("other 409: err = %v", err)
		}
	})

	t.Run("a 404 on the connection is unavailable, the ids are escaped into one path segment and the body is JSON only when present", func(t *testing.T) {
		var gotPath, gotCT string
		srv := serve(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotCT = r.URL.EscapedPath(), r.Header.Get("Content-Type")
			_, _ = w.Write([]byte(`{}`))
		})
		c := NewAgentClient(srv.URL, "t", nil)
		_, _ = c.LinkStatus(t.Context(), "../accounts?x")
		if gotPath != "/admin/links/..%2Faccounts%3Fx" || gotCT != "" {
			t.Errorf("path=%q content-type=%q", gotPath, gotCT)
		}
		_, _ = c.RemoveAccount(t.Context(), "a/b")
		if gotPath != "/admin/accounts/a%2Fb" {
			t.Errorf("path=%q", gotPath)
		}
		_, _ = c.StartLink(t.Context())
		if gotCT != "application/json" {
			t.Errorf("content-type = %q", gotCT)
		}

		missing := serve(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
		if _, err := NewAgentClient(missing.URL, "t", nil).Connection(t.Context()); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("404 on connection: %v", err)
		}
	})
}
