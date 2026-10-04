package internalapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"tsb-service/internal/mcp/actions"
)

func TestGetUnknownAndExpired(t *testing.T) {
	e := setup(t)
	code, body := e.do("GET", "/internal/changes/chg_missing", token, "")
	if code != 404 || body["code"] != "not_found" || body["error"] != "No pending change with this id." {
		t.Errorf("unknown: %d %v", code, body)
	}
	id := e.propose(500)
	e.advance(10*time.Minute + time.Second)
	// Reading an overdue change reports it as expired.
	code, body = e.do("GET", "/internal/changes/"+id, token, "")
	if code != 200 || body["status"] != "expired" || body["decided_at"] == nil {
		t.Errorf("expired view: %d %v", code, body)
	}
}

func TestViewFields(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	_, body := e.do("GET", "/internal/changes/"+id, token, "")
	if body["change_id"] != id || body["tool"] != "propose_price_change" || body["kind"] != actions.KindProductPrice || body["entity_type"] != "product" || body["entity_id"] != "p-maki-saumon" ||
		body["request_context"] != "maki 5 euros" || body["created_at"] != "2026-10-03T13:00:00+02:00" || body["expires_at"] != "2026-10-03T13:10:00+02:00" || body["is_undo"] != false || body["decided_at"] != nil || body["error"] != nil {
		t.Errorf("pending view: %v", body)
	}
	_, body = e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if body["decided_at"] != "2026-10-03T13:00:00+02:00" {
		t.Errorf("applied view: %v", body)
	}

	// The undo of a sensitive change is itself a pending change flagged as an undo.
	u, err := e.svc.Undo(t.Context(), "")
	if err != nil || u.Proposal == nil {
		t.Fatal(u, err)
	}
	_, body = e.do("GET", "/internal/changes/"+u.Proposal.ChangeID, token, "")
	if body["is_undo"] != true || body["tool"] != "undo_last_change" {
		t.Errorf("undo view: %v", body)
	}
}

func TestResponseHeaders(t *testing.T) {
	e := setup(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/internal/changes/chg_missing", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", resp.Header)
	}
}

func TestAuthRules(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	// Every change endpoint needs the token, reads included.
	for _, c := range []struct{ method, path string }{
		{"GET", "/internal/changes/" + id}, {"POST", "/internal/changes/" + id + "/apply"}, {"POST", "/internal/changes/" + id + "/reject"},
	} {
		code, body := e.do(c.method, c.path, "", "")
		if code != 401 || body["code"] != "unauthorized" || body["change"] != nil {
			t.Errorf("%s %s without token: %d %v", c.method, c.path, code, body)
		}
	}
	// Only the Bearer scheme counts.
	req, _ := http.NewRequest("GET", e.srv.URL+"/internal/changes/"+id, nil)
	req.Header.Set("Authorization", "Basic "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("Basic scheme: %d", resp.StatusCode)
	}
	req.Header.Set("Authorization", token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("bare token: %d", resp.StatusCode)
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("unauthorized calls changed the price")
	}
}

func TestEmptyConfiguredTokenRejectsEverything(t *testing.T) {
	// A server started without a token must not accept an empty bearer.
	e := setupWithToken(t, "")
	id := e.propose(500)
	for _, tok := range []string{"", " "} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/internal/changes/"+id+"/apply", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("bearer %q with an empty configured token: %d", tok, resp.StatusCode)
		}
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("price changed")
	}
}

func TestDecisionBodyIsOptionalAndForgiving(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	// A body that is not JSON is ignored: the decision is still taken.
	code, body := e.do("POST", "/internal/changes/"+id+"/apply", token, "{not json")
	if code != 200 || body["status"] != "applied" {
		t.Fatalf("apply with a broken body: %d %v", code, body)
	}
	audit, _ := e.svc.Store().RecentAudit(t.Context(), 1)
	// The request context falls back to the one given when proposing.
	if audit[0].RequestContext != "maki 5 euros" {
		t.Errorf("request context = %q", audit[0].RequestContext)
	}
	// An oversized body is cut off at 64 KB and treated as unreadable.
	id2 := e.propose(600)
	big := `{"request_context":"` + strings.Repeat("a", 70<<10) + `"}`
	code, _ = e.do("POST", "/internal/changes/"+id2+"/reject", token, big)
	if code != 200 {
		t.Errorf("reject with a big body: %d", code)
	}
}

func TestRejectUnknownAndTwice(t *testing.T) {
	e := setup(t)
	if code, body := e.do("POST", "/internal/changes/chg_missing/reject", token, ""); code != 404 || body["code"] != "not_found" {
		t.Errorf("reject unknown: %d %v", code, body)
	}
	id := e.propose(500)
	e.do("POST", "/internal/changes/"+id+"/reject", token, "")
	code, body := e.do("POST", "/internal/changes/"+id+"/reject", token, "")
	if code != 409 || body["code"] != "already_rejected" || body["error"] != "This change was already rejected." || body["change"].(map[string]any)["status"] != "rejected" {
		t.Errorf("reject twice: %d %v", code, body)
	}
}

func TestFailedAndConflictedChangesCannotBeRetried(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	e.fake.Lock()
	e.fake.FailOps["McpUpdateProduct"] = "BOOM"
	e.fake.Unlock()
	if code, _ := e.do("POST", "/internal/changes/"+id+"/apply", token, ""); code != 502 {
		t.Fatalf("first apply: %d", code)
	}
	e.fake.Lock()
	delete(e.fake.FailOps, "McpUpdateProduct")
	e.fake.Unlock()
	code, body := e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 409 || body["code"] != "already_failed" || !strings.Contains(body["error"].(string), "status: failed") {
		t.Errorf("retry after failure: %d %v", code, body)
	}
	if e.fake.ProductByID("p-maki-saumon").Price != "4.5" {
		t.Error("a failed change must not apply on retry")
	}

	id2 := e.propose(500)
	e.fake.Lock()
	e.fake.Products[0].Price = "4.8"
	e.fake.Unlock()
	e.do("POST", "/internal/changes/"+id2+"/apply", token, "")
	code, body = e.do("POST", "/internal/changes/"+id2+"/apply", token, "")
	if code != 409 || body["code"] != "already_conflict" {
		t.Errorf("retry after conflict: %d %v", code, body)
	}
}

func TestUserErrorWhileApplyingIs422(t *testing.T) {
	e := setup(t)
	// A photo change stored without its photo cannot be executed.
	p, err := e.svc.Propose(t.Context(), "propose_product_image", actions.KindProductImage, actions.ImageParams{ProductID: "p-maki-saumon", Filename: "a.png", ContentType: "image/png", SizeBytes: 10, SHA256: "x"}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	code, body := e.do("POST", "/internal/changes/"+p.ChangeID+"/apply", token, "")
	if code != 422 || body["code"] != "rejected" || !strings.Contains(body["error"].(string), "photo data is missing") || body["change"].(map[string]any)["status"] != "failed" {
		t.Errorf("user error: %d %v", code, body)
	}
}

func TestInternalFailuresDoNotLeakDetails(t *testing.T) {
	e := setup(t)
	id := e.propose(500)
	_ = e.svc.Store().Close()
	code, body := e.do("POST", "/internal/changes/"+id+"/apply", token, "")
	if code != 500 || body["code"] != "internal" || body["error"] != "Something went wrong while applying the change." {
		t.Errorf("apply on a broken store: %d %v", code, body)
	}
	if b, _ := e.rawBody("GET", "/internal/changes/"+id); strings.Contains(strings.ToLower(b), "sql") || strings.Contains(b, "closed") {
		t.Errorf("internal detail leaked: %s", b)
	}
	if code, _ := e.do("GET", "/healthz", "", ""); code != 503 {
		t.Errorf("healthz with a broken store: %d", code)
	}
}

func (e *env) rawBody(method, path string) (string, int) {
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
}
