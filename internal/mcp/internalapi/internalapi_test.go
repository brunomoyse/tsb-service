package internalapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/fakeupstream"
	"tsb-service/internal/mcp/internalapi"
	"tsb-service/internal/mcp/upstream"
)

const token = "internal-secret"

var brussels, _ = time.LoadLocation("Europe/Brussels")

type env struct {
	t    *testing.T
	fake *fakeupstream.Server
	svc  *actions.Service
	srv  *httptest.Server
	mu   sync.Mutex
	now  time.Time
}

func (e *env) Now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, now: time.Date(2026, 10, 3, 13, 0, 0, 0, brussels)}
	e.fake = fakeupstream.New()
	t.Cleanup(e.fake.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sa := upstream.ServiceAccount{Issuer: e.fake.URL, ClientID: "mcp-client", ClientSecret: "mcp-secret", ProjectID: "1"}
	up := upstream.New(e.fake.URL, sa.TokenSource(context.Background()), log)
	store, err := changes.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.svc = actions.NewService(&actions.Env{Up: up, Loc: brussels, PriceMaxPct: 50, Now: e.Now}, store, 10*time.Minute, log)
	e.srv = httptest.NewServer(internalapi.Handler(e.svc, token, brussels, log))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) propose(cents int64) string {
	e.t.Helper()
	p, err := e.svc.Propose(context.Background(), "propose_price_change", actions.KindProductPrice, actions.PriceParams{ProductID: "p-maki-saumon", NewPriceCents: cents}, nil, "maki 5 euros", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return p.ChangeID
}

func (e *env) do(method, path, tok, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestApply(t *testing.T) {
	e := setup(t)
	id := e.propose(500)

	code, body := e.do("GET", "/internal/changes/"+id, token, "")
	if code != 200 || body["status"] != "pending" || !strings.Contains(body["summary"].(string), "4.50 EUR -> 5.00 EUR") ||
		body["summary_zh"] != "卷「三文鱼卷」价格：4.50 欧元 → 5.00 欧元" {
		t.Fatalf("get: %d %v", code, body)
	}

	code, body = e.do("POST", "/internal/changes/"+id+"/apply", token, `{"request_context":"是的"}`)
	if code != 200 || body["status"] != "applied" {
		t.Fatalf("apply: %d %v", code, body)
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "5" {
		t.Error("price not changed upstream")
	}
	audit, _ := e.svc.Store().RecentAudit(context.Background(), 1)
	if audit[0].Source != actions.SourceApply || audit[0].RequestContext != "是的" || audit[0].Outcome != "applied" {
		t.Errorf("audit: %+v", audit[0])
	}

	code, body = e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 409 || body["code"] != "already_applied" {
		t.Errorf("already applied: %d %v", code, body)
	}
}

func TestReject(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	code, body := e.do("POST", "/internal/changes/"+id+"/reject", token, `{"request_context":"不要"}`)
	if code != 200 || body["status"] != "rejected" {
		t.Fatalf("reject: %d %v", code, body)
	}
	code, body = e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 409 || body["code"] != "already_rejected" {
		t.Errorf("apply after reject: %d %v", code, body)
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("rejected change must not apply")
	}
}

func TestExpired(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	e.advance(11 * time.Minute)
	code, body := e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 410 || body["code"] != "expired" || !strings.Contains(body["error"].(string), "expired") {
		t.Errorf("expired: %d %v", code, body)
	}
	code, body = e.do("POST", "/internal/changes/"+id+"/reject", token, "")
	if code != 410 {
		t.Errorf("reject expired: %d %v", code, body)
	}
}

func TestWrongToken(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	for _, tok := range []string{"", "wrong", token + "x"} {
		code, _ := e.do("POST", "/internal/changes/"+id+"/apply", tok, "")
		if code != 401 {
			t.Errorf("token %q: got %d", tok, code)
		}
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("unauthorized apply changed the price")
	}
	if code, _ := e.do("GET", "/healthz", "", ""); code != 200 {
		t.Errorf("healthz: %d", code)
	}
}

func TestUpstreamChangedInBetween(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	e.fake.Lock()
	e.fake.Products[0].Price = "4.8"
	e.fake.Unlock()
	code, body := e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 409 || body["code"] != "conflict" || body["change"].(map[string]any)["status"] != "conflict" {
		t.Errorf("conflict: %d %v", code, body)
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "4.8" {
		t.Error("conflicting change applied")
	}
}

func TestUnknownAndUpstreamFailure(t *testing.T) {
	e := setup(t)
	if code, _ := e.do("POST", "/internal/changes/chg_missing/apply", token, ""); code != 404 {
		t.Errorf("unknown: %d", code)
	}
	id := e.propose(500)
	e.fake.FailOps["McpUpdateProduct"] = "INTERNAL_SERVER_ERROR"
	code, body := e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 502 || body["change"].(map[string]any)["status"] != "failed" {
		t.Errorf("upstream failure: %d %v", code, body)
	}
	if strings.Contains(body["error"].(string), "injected") {
		t.Errorf("upstream detail leaked: %v", body["error"])
	}
}
