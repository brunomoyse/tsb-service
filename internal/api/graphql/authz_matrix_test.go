package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	gql "tsb-service/internal/api/graphql"
	"tsb-service/internal/api/graphql/directives"
	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/pkg/utils"
)

// The access rules of the API are the @auth / @admin / @staff directives of the schema. This test
// reads the schema itself, so a new field cannot be added without its protection being checked: for
// every query and mutation that carries a directive it asks as a stranger, a customer, a POS device
// and an admin, with a valid and with an expired token, and compares the outcome with the rule the
// directive declares. The directives are the real ones; only what runs after them is replaced by a
// marker, so no resolver (and no database) is involved.

const authzAllowed = "AUTHZ_PASSED"

type caller struct {
	name                string
	userID              string
	admin, pos          bool
	expired             bool
	wantAuth, wantStaff string // outcome for a field with that directive
	wantAdmin           string
}

const (
	outUnauthenticated = "UNAUTHENTICATED"
	outForbidden       = "FORBIDDEN"
	outAllowed         = authzAllowed
)

var authzCallers = []caller{
	{name: "anonymous", wantAuth: outUnauthenticated, wantAdmin: outUnauthenticated, wantStaff: outUnauthenticated},
	{name: "customer", userID: "11111111-1111-1111-1111-111111111111", wantAuth: outAllowed, wantAdmin: outForbidden, wantStaff: outForbidden},
	{name: "POS device", userID: "22222222-2222-2222-2222-222222222222", pos: true, wantAuth: outAllowed, wantAdmin: outForbidden, wantStaff: outAllowed},
	{name: "admin", userID: "33333333-3333-3333-3333-333333333333", admin: true, wantAuth: outAllowed, wantAdmin: outAllowed, wantStaff: outAllowed},
	{name: "customer with an expired token", userID: "11111111-1111-1111-1111-111111111111", expired: true, wantAuth: outUnauthenticated, wantAdmin: outUnauthenticated, wantStaff: outUnauthenticated},
	{name: "admin with an expired token", userID: "33333333-3333-3333-3333-333333333333", admin: true, expired: true, wantAuth: outUnauthenticated, wantAdmin: outUnauthenticated, wantStaff: outUnauthenticated},
	{name: "POS device with an expired token", userID: "22222222-2222-2222-2222-222222222222", pos: true, expired: true, wantAuth: outUnauthenticated, wantAdmin: outUnauthenticated, wantStaff: outUnauthenticated},
}

// protectedField is a root field with its directive and a ready-to-send operation.
type protectedField struct {
	op, name, directive, doc string
}

func zeroLiteral(schema *ast.Schema, t *ast.Type) string {
	if t.Elem != nil {
		return "[" + zeroLiteral(schema, t.Elem) + "]"
	}
	def := schema.Types[t.NamedType]
	switch def.Kind {
	case ast.Enum:
		return def.EnumValues[0].Name
	case ast.InputObject:
		var parts []string
		for _, f := range def.Fields {
			if f.Type.NonNull && f.DefaultValue == nil {
				parts = append(parts, f.Name+": "+zeroLiteral(schema, f.Type))
			}
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	switch t.NamedType {
	case "ID":
		return `"44444444-4444-4444-4444-444444444444"`
	case "Int":
		return "1"
	case "Float":
		return "1.5"
	case "Boolean":
		return "true"
	case "DateTime", "Time":
		return `"2026-01-01T00:00:00Z"`
	case "JSON":
		return "{}"
	default:
		return `"x"`
	}
}

// publicFields is the explicit allowlist of root fields that are meant to be reachable without a
// directive (anonymous callers included). A new Query, Mutation or Subscription field with neither
// @auth, @admin or @staff nor an entry here fails TestEveryRootFieldIsProtectedOrExplicitlyPublic.
// Adding a name here is a conscious decision that the field is public: say why in the commit.
var publicFields = map[string]string{
	"query autocompleteAddresses":           "address search of the checkout form, rate limited, no personal data",
	"query product":                         "public catalog",
	"query productCategories":               "public catalog",
	"query productCategory":                 "public catalog",
	"query productCategoryBySlug":           "public catalog",
	"query products":                        "public catalog",
	"query quoteOrder":                      "price quote before login; the resolver rejects an expired token itself (order_quote.go)",
	"query resolveAddress":                  "address lookup of the checkout form, rate limited, no personal data",
	"query restaurantConfig":                "opening hours and status shown on the public site",
	"subscription productUpdated":           "live public catalog",
	"subscription restaurantConfigUpdated":  "live public opening hours and status",
	"subscription scheduleOverridesUpdated": "live public schedule overrides",
}

func rootFields(schema *ast.Schema) map[string]*ast.Definition {
	roots := map[string]*ast.Definition{"query": schema.Query, "mutation": schema.Mutation}
	if schema.Subscription != nil {
		roots["subscription"] = schema.Subscription
	}
	return roots
}

func fieldDirective(f *ast.FieldDefinition) string {
	var directive string
	for _, d := range f.Directives {
		switch d.Name {
		case "auth", "admin", "staff":
			directive = d.Name
		}
	}
	return directive
}

func protectedFields(t *testing.T, schema *ast.Schema) []protectedField {
	t.Helper()
	var out []protectedField
	for opName, root := range rootFields(schema) {
		for _, f := range root.Fields {
			if strings.HasPrefix(f.Name, "__") {
				continue
			}
			directive := fieldDirective(f)
			if directive == "" {
				continue
			}
			var args []string
			for _, a := range f.Arguments {
				if a.Type.NonNull && a.DefaultValue == nil {
					args = append(args, a.Name+": "+zeroLiteral(schema, a.Type))
				}
			}
			call := f.Name
			if len(args) > 0 {
				call += "(" + strings.Join(args, ", ") + ")"
			}
			if schema.Types[f.Type.Name()].Kind == ast.Object {
				call += " { __typename }"
			}
			out = append(out, protectedField{op: opName, name: f.Name, directive: directive, doc: opName + " { " + call + " }"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// authzServer serves the real schema with the real directives, each followed by a marker instead of
// the resolver. The caller's identity comes from headers, standing in for the auth middleware.
func authzServer() (http.Handler, *ast.Schema) {
	allowed := func(ctx context.Context) (any, error) {
		return nil, &gqlerror.Error{Message: authzAllowed, Extensions: map[string]any{"code": authzAllowed}}
	}
	wrap := func(d func(context.Context, any, graphql.Resolver) (any, error)) func(context.Context, any, graphql.Resolver) (any, error) {
		return func(ctx context.Context, obj any, _ graphql.Resolver) (any, error) { return d(ctx, obj, allowed) }
	}
	cfg := gql.Config{Resolvers: &resolver.Resolver{}}
	cfg.Directives.Auth = wrap(directives.Auth)
	cfg.Directives.Admin = wrap(directives.Admin)
	cfg.Directives.Staff = wrap(directives.Staff)
	exec := gql.NewExecutableSchema(cfg)
	srv := handler.NewDefaultServer(exec)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if id := r.Header.Get("X-Test-User"); id != "" {
			ctx = utils.SetUserID(ctx, id)
			ctx = utils.SetIsAdmin(ctx, r.Header.Get("X-Test-Admin") == "1")
			ctx = utils.SetIsPOS(ctx, r.Header.Get("X-Test-POS") == "1")
		}
		if r.Header.Get("X-Test-Expired") == "1" {
			ctx = utils.SetTokenExpiry(ctx, time.Now().Add(-time.Minute))
		} else if r.Header.Get("X-Test-User") != "" {
			ctx = utils.SetTokenExpiry(ctx, time.Now().Add(time.Hour))
		}
		srv.ServeHTTP(w, r.WithContext(ctx))
	}), exec.Schema()
}

// applyCaller sets the headers that stand in for the auth middleware for one caller.
func (c caller) applyTo(h http.Header) {
	if c.userID != "" {
		h.Set("X-Test-User", c.userID)
	}
	if c.admin {
		h.Set("X-Test-Admin", "1")
	}
	if c.pos {
		h.Set("X-Test-POS", "1")
	}
	if c.expired {
		h.Set("X-Test-Expired", "1")
	}
}

func (f protectedField) wantFor(c caller) string {
	return map[string]string{"auth": c.wantAuth, "admin": c.wantAdmin, "staff": c.wantStaff}[f.directive]
}

type authzErrors struct {
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func TestEveryRootFieldIsProtectedOrExplicitlyPublic(t *testing.T) {
	_, schema := authzServer()
	seen := map[string]bool{}
	var unprotected []string
	for opName, root := range rootFields(schema) {
		for _, f := range root.Fields {
			if strings.HasPrefix(f.Name, "__") {
				continue
			}
			key := opName + " " + f.Name
			if fieldDirective(f) != "" {
				continue
			}
			seen[key] = true
			if _, ok := publicFields[key]; !ok {
				unprotected = append(unprotected, key)
			}
		}
	}
	sort.Strings(unprotected)
	assert.Empty(t, unprotected, "these root fields have no @auth/@admin/@staff and are not in publicFields: protect them or list them as public on purpose")
	for key, why := range publicFields {
		assert.True(t, seen[key], "publicFields lists %q, which is not an unprotected root field of the schema (renamed, removed or protected since?)", key)
		assert.NotEmpty(t, why, "say why %q is public", key)
	}
}

func TestSchemaDirectivesAuthorizeEveryProtectedField(t *testing.T) {
	h, schema := authzServer()
	fields := protectedFields(t, schema)
	require.Greater(t, len(fields), 40, "the schema must expose its protected fields")

	counts := map[string]int{}
	for _, f := range fields {
		counts[f.op+" "+f.directive]++
	}
	require.NotZero(t, counts["query auth"])
	require.NotZero(t, counts["query admin"])
	require.NotZero(t, counts["mutation staff"])
	require.NotZero(t, counts["subscription staff"], "subscriptions are part of the matrix")
	require.NotZero(t, counts["subscription admin"])
	require.NotZero(t, counts["subscription auth"])

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, f := range fields {
		for _, c := range authzCallers {
			t.Run(fmt.Sprintf("%s %s as %s", f.op, f.name, c.name), func(t *testing.T) {
				var resp authzErrors
				if f.op == "subscription" {
					resp = askOverWebSocket(t, srv, c, f.doc)
				} else {
					body, err := json.Marshal(map[string]string{"query": f.doc})
					require.NoError(t, err)
					req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					c.applyTo(req.Header)
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
				}
				require.Len(t, resp.Errors, 1, "operation %q", f.doc)
				assert.Equal(t, f.wantFor(c), resp.Errors[0].Extensions["code"], "@%s on %s: %s", f.directive, f.name, resp.Errors[0].Message)
			})
		}
	}
}

// askOverWebSocket runs one subscription document as the caller and returns the errors of the
// first frame that carries any (the directive refusal, or the marker that replaces the resolver).
func askOverWebSocket(t *testing.T, srv *httptest.Server, c caller, doc string) authzErrors {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	header := http.Header{}
	c.applyTo(header)
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &coderws.DialOptions{
		Subprotocols: []string{"graphql-transport-ws"}, HTTPHeader: header,
	})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	write := func(m wsMessage) {
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, conn.Write(ctx, coderws.MessageText, raw))
	}
	write(wsMessage{Type: "connection_init"})
	sub, _ := json.Marshal(map[string]string{"query": doc})
	write(wsMessage{ID: "1", Type: "subscribe", Payload: sub})
	for {
		_, raw, err := conn.Read(ctx)
		require.NoError(t, err)
		var m wsMessage
		require.NoError(t, json.Unmarshal(raw, &m), string(raw))
		switch m.Type {
		case "connection_ack", "ping", "pong":
			continue
		case "next":
			var resp authzErrors
			require.NoError(t, json.Unmarshal(m.Payload, &resp))
			return resp
		case "error":
			var errs []struct {
				Message    string         `json:"message"`
				Extensions map[string]any `json:"extensions"`
			}
			require.NoError(t, json.Unmarshal(m.Payload, &errs), string(m.Payload))
			return authzErrors{Errors: errs}
		default:
			require.Failf(t, "unexpected frame", "%s %s", m.Type, m.Payload)
		}
	}
}
