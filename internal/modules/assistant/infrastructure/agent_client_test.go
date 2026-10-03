package infrastructure

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"tsb-service/internal/modules/assistant/domain"
)

func TestAgentClient(t *testing.T) {
	var gotReplace any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /admin/connection":
			_, _ = w.Write([]byte(`{"state":"expired","account":"o@im.wechat","expired_at":"2026-10-03T12:00:00Z","ever_connected":true}`))
		case "POST /admin/login":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotReplace = body["replace_owner"]
			_, _ = w.Write([]byte(`{"login_id":"abc","status":"wait","qr_content":"https://liteapp.weixin.qq.com/q/x","expires_at":"2026-10-03T12:02:00Z"}`))
		case "GET /admin/login/abc":
			_, _ = w.Write([]byte(`{"login_id":"abc","status":"scanned","qr_content":"q","expires_at":"2026-10-03T12:02:00Z"}`))
		case "GET /admin/login/gone":
			w.WriteHeader(http.StatusNotFound)
		case "POST /admin/disconnect":
			_, _ = w.Write([]byte(`{"state":"disconnected","ever_connected":true}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ctx := t.Context()
	c := NewAgentClient(srv.URL+"/", "secret", nil)

	conn, err := c.Connection(ctx)
	if err != nil || conn.State != domain.StateExpired || conn.ExpiredAt == nil || !conn.EverConnected || conn.Account != "o@im.wechat" {
		t.Fatalf("connection %+v %v", conn, err)
	}
	lg, err := c.StartLogin(ctx, true)
	if err != nil || lg.ID != "abc" || lg.QRContent == "" || gotReplace != true {
		t.Fatalf("start %+v %v replace=%v", lg, err, gotReplace)
	}
	if lg, err = c.LoginStatus(ctx, "abc"); err != nil || lg.Status != domain.LoginScanned {
		t.Fatalf("status %+v %v", lg, err)
	}
	if _, err = c.LoginStatus(ctx, "gone"); !errors.Is(err, domain.ErrUnknownLogin) {
		t.Errorf("unknown login: %v", err)
	}
	if conn, err = c.Disconnect(ctx); err != nil || conn.State != domain.StateDisconnected {
		t.Errorf("disconnect %+v %v", conn, err)
	}

	if _, err := NewAgentClient(srv.URL, "wrong", nil).Connection(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("wrong token: %v", err)
	}
	srv.Close()
	if _, err := c.Connection(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("agent down: %v", err)
	}
}
