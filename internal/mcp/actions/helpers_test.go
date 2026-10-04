package actions

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/upstream"
)

// fail makes the fake answer op with a GraphQL error from now on.
func (f *fixture) fail(op string) {
	f.fake.Lock()
	f.fake.FailOps[op] = "BOOM"
	f.fake.Unlock()
}

func (f *fixture) unfail(op string) {
	f.fake.Lock()
	delete(f.fake.FailOps, op)
	f.fake.Unlock()
}

func (f *fixture) handler(t *testing.T, kind string) handler {
	t.Helper()
	h, err := f.svc.handler(kind)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// prepare runs a handler's Prepare directly (no storage, no upstream write).
func (f *fixture) prepare(t *testing.T, kind string, params any) (*prepared, error) {
	t.Helper()
	pr, _, err := f.handler(t, kind).prepare(f.ctx, f.svc.env, mustJSON(t, params))
	return pr, err
}

// execute runs a handler's Execute directly, skipping Prepare.
func (f *fixture) execute(t *testing.T, kind string, params any, blob []byte) (any, error) {
	t.Helper()
	return f.handler(t, kind).execute(f.ctx, f.svc.env, mustJSON(t, params), blob)
}

// inverse runs a handler's Inverse directly.
func (f *fixture) inverse(t *testing.T, kind string, params, before, after any) (string, any, error) {
	t.Helper()
	raw := func(v any) json.RawMessage {
		if s, ok := v.(string); ok {
			return json.RawMessage(s)
		}
		return mustJSON(t, v)
	}
	return f.handler(t, kind).inverse(raw(params), raw(before), raw(after))
}

// propose stores a pending change, failing the test on error.
func (f *fixture) propose(t *testing.T, kind string, params any) *Proposal {
	t.Helper()
	p, err := f.svc.Propose(f.ctx, "test", kind, params, nil, "", nil)
	if err != nil {
		t.Fatalf("propose %s: %v", kind, err)
	}
	return p
}

// applyChange proposes and applies a change, failing the test on error.
func (f *fixture) applyChange(t *testing.T, kind string, params any) *changes.Change {
	t.Helper()
	p := f.propose(t, kind, params)
	if p.NoOp {
		t.Fatalf("%s unexpectedly a no-op: %s", kind, p.Summary)
	}
	c, err := f.svc.ApplyPending(f.ctx, p.ChangeID, "")
	if err != nil {
		t.Fatalf("apply %s: %v", kind, err)
	}
	return c
}

// wantUserErr asserts err is a UserError containing want.
func wantUserErr(t *testing.T, err error, want string) {
	t.Helper()
	ue, ok := errors.AsType[*UserError](err)
	if !ok {
		t.Fatalf("want a user error containing %q, got %T %v", want, err, err)
	}
	if !strings.Contains(ue.Msg, want) {
		t.Errorf("user error %q does not contain %q", ue.Msg, want)
	}
}

// wantUpstreamErr asserts err comes from the upstream client (not a UserError).
func wantUpstreamErr(t *testing.T, err error) {
	t.Helper()
	if _, ok := errors.AsType[*upstream.Error](err); !ok {
		t.Fatalf("want an upstream error, got %T %v", err, err)
	}
	if _, ok := errors.AsType[*UserError](err); ok {
		t.Fatalf("upstream failure must not surface as a user error: %v", err)
	}
}

func contains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, missing %q", what, got, w)
		}
	}
}

func ip(i int) *int       { return &i }
func i64(i int64) *int64  { return &i }
func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }
