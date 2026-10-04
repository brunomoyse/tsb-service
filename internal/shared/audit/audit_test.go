package audit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"tsb-service/internal/api/graphql/testhelpers"
	. "tsb-service/internal/shared/audit"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

type row struct {
	ID            int64   `db:"id"`
	ActorKind     string  `db:"actor_kind"`
	ActorID       string  `db:"actor_id"`
	ZitadelSub    *string `db:"zitadel_sub"`
	Action        string  `db:"action"`
	OperationName *string `db:"operation_name"`
	Variables     *string `db:"variables"`
	Success       bool    `db:"success"`
	Error         *string `db:"error"`
	RequestID     *string `db:"request_id"`
	IP            *string `db:"ip"`
}

func rows(t *testing.T, db *sqlx.DB) []row {
	t.Helper()
	var out []row
	require.NoError(t, db.SelectContext(t.Context(), &out, `SELECT id, actor_kind, actor_id, zitadel_sub, action, operation_name, variables::text AS variables, success, error, request_id, ip FROM staff_audit_log ORDER BY id`))
	return out
}

func staffCtx(ctx context.Context, kind string) context.Context {
	ctx = utils.SetUserID(ctx, "actor-1")
	switch kind {
	case "admin":
		ctx = utils.SetIsAdmin(ctx, true)
		ctx = utils.SetZitadelSub(ctx, "zit-sub-1")
	case "pos":
		ctx = utils.SetIsPOS(ctx, true)
	}
	ctx = logging.SetRequestID(ctx, "req-1")
	return utils.SetClientIP(ctx, "198.51.100.7")
}

func TestRecorderRecord(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	rec := NewRecorder(tdb.DB)

	t.Run("an admin entry stores the actor, request context and variables", func(t *testing.T) {
		rec.Record(staffCtx(t.Context(), "admin"), Entry{
			Action: "graphql:updateOrder", OperationName: "SetStatus", Success: true,
			Variables: map[string]any{"id": "o1", "status": "CONFIRMED"},
		})
		got := rows(t, tdb.DB)
		require.Len(t, got, 1)
		r := got[0]
		assert.Equal(t, "admin", r.ActorKind)
		assert.Equal(t, "actor-1", r.ActorID)
		require.NotNil(t, r.ZitadelSub)
		assert.Equal(t, "zit-sub-1", *r.ZitadelSub)
		assert.Equal(t, "graphql:updateOrder", r.Action)
		assert.Equal(t, "SetStatus", *r.OperationName)
		assert.JSONEq(t, `{"id":"o1","status":"CONFIRMED"}`, *r.Variables)
		assert.True(t, r.Success)
		assert.Nil(t, r.Error)
		assert.Equal(t, "req-1", *r.RequestID)
		assert.Equal(t, "198.51.100.7", *r.IP)
	})

	t.Run("a POS entry is attributed to the pos actor kind, and a failure keeps its message", func(t *testing.T) {
		rec.Record(staffCtx(t.Context(), "pos"), Entry{Action: "graphql:cancelOrder", Success: false, Error: "order already delivered"})
		r := rows(t, tdb.DB)[1]
		assert.Equal(t, "pos", r.ActorKind)
		assert.Nil(t, r.ZitadelSub, "empty values are stored as NULL")
		assert.Nil(t, r.OperationName)
		assert.Nil(t, r.Variables)
		assert.False(t, r.Success)
		assert.Equal(t, "order already delivered", *r.Error)
	})

	t.Run("a POS device that is also flagged admin is recorded as pos", func(t *testing.T) {
		ctx := utils.SetIsAdmin(staffCtx(t.Context(), "pos"), true)
		rec.Record(ctx, Entry{Action: "both", Success: true})
		got := rows(t, tdb.DB)
		assert.Equal(t, "pos", got[len(got)-1].ActorKind)
	})

	t.Run("customers and anonymous callers are never recorded", func(t *testing.T) {
		before := len(rows(t, tdb.DB))
		rec.Record(utils.SetUserID(t.Context(), "customer-1"), Entry{Action: "graphql:createOrder", Success: true})
		rec.Record(t.Context(), Entry{Action: "anon", Success: true})
		assert.Len(t, rows(t, tdb.DB), before)
	})

	t.Run("file uploads are reduced to their metadata, nested values are sanitised", func(t *testing.T) {
		ctx := staffCtx(t.Context(), "admin")
		rec.Record(ctx, Entry{Action: "graphql:uploadImage", Success: true, Variables: map[string]any{
			"file":   graphql.Upload{Filename: "a.png", Size: 1234},
			"ptr":    &graphql.Upload{Filename: "b.png", Size: 55},
			"nilPtr": (*graphql.Upload)(nil),
			"list":   []any{graphql.Upload{Filename: "c.png", Size: 7}, "text", 3},
			"nested": map[string]any{"deep": &graphql.Upload{Filename: "d.png", Size: 9}},
		}})
		got := rows(t, tdb.DB)
		assert.JSONEq(t, `{"file":{"file":"a.png","size":1234},"ptr":{"file":"b.png","size":55},"nilPtr":null,
			"list":[{"file":"c.png","size":7},"text",3],"nested":{"deep":{"file":"d.png","size":9}}}`, *got[len(got)-1].Variables)
	})

	t.Run("oversized variables are replaced by a marker so the log cannot be bloated", func(t *testing.T) {
		rec.Record(staffCtx(t.Context(), "admin"), Entry{Action: "graphql:bulk", Success: true,
			Variables: map[string]any{"blob": strings.Repeat("x", (8<<10)+1)}})
		got := rows(t, tdb.DB)
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(*got[len(got)-1].Variables), &v))
		assert.Equal(t, true, v["_truncated"])
		assert.Greater(t, v["_bytes"], float64(8<<10))
		assert.NotContains(t, v, "blob")
	})

	t.Run("variables that cannot be serialised are replaced by a marker, the action is still recorded", func(t *testing.T) {
		rec.Record(staffCtx(t.Context(), "admin"), Entry{Action: "graphql:weird", Success: true,
			Variables: map[string]any{"fn": func() {}}})
		got := rows(t, tdb.DB)
		assert.Equal(t, "graphql:weird", got[len(got)-1].Action)
		assert.JSONEq(t, `{"_error":"unserializable variables"}`, *got[len(got)-1].Variables)
	})

	t.Run("a cancelled request context does not drop the row", func(t *testing.T) {
		ctx, cancel := context.WithCancel(staffCtx(t.Context(), "admin"))
		cancel()
		before := len(rows(t, tdb.DB))
		rec.Record(ctx, Entry{Action: "graphql:afterDisconnect", Success: true})
		got := rows(t, tdb.DB)
		require.Len(t, got, before+1)
		assert.Equal(t, "graphql:afterDisconnect", got[len(got)-1].Action)
	})

	t.Run("the log is append-only", func(t *testing.T) {
		_, err := tdb.DB.ExecContext(t.Context(), `UPDATE staff_audit_log SET success = NOT success`)
		require.ErrorContains(t, err, "append-only")
		_, err = tdb.DB.ExecContext(t.Context(), `DELETE FROM staff_audit_log`)
		require.ErrorContains(t, err, "append-only")
	})

	t.Run("a write failure is logged and never returned or panicked on", func(t *testing.T) {
		core, logs := observer.New(zapcore.ErrorLevel)
		restore := zap.ReplaceGlobals(zap.New(core))
		defer restore()
		_, err := tdb.DB.ExecContext(t.Context(), `ALTER TABLE staff_audit_log RENAME TO staff_audit_log_gone`)
		require.NoError(t, err)
		defer func() { _, _ = tdb.DB.Exec(`ALTER TABLE staff_audit_log_gone RENAME TO staff_audit_log`) }()

		rec.Record(staffCtx(t.Context(), "admin"), Entry{Action: "graphql:lost", Success: true})

		require.Equal(t, 1, logs.Len())
		entry := logs.All()[0]
		assert.Equal(t, "failed to write staff audit log", entry.Message)
		assert.Equal(t, "graphql:lost", entry.ContextMap()["action"])
	})
}

func TestRecorderIsInertWithoutADatabase(t *testing.T) {
	ctx := staffCtx(t.Context(), "admin")
	assert.NotPanics(t, func() {
		var nilRecorder *Recorder
		nilRecorder.Record(ctx, Entry{Action: "x"})
		NewRecorder(nil).Record(ctx, Entry{Action: "x"})
	})
}

func operation(t *testing.T, query string) *ast.OperationDefinition {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	require.NoError(t, err)
	require.Len(t, doc.Operations, 1)
	return doc.Operations[0]
}

func TestGraphQLExtension(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	ext := GraphQLExtension{Recorder: NewRecorder(tdb.DB)}

	assert.Equal(t, "StaffAudit", ext.ExtensionName())
	assert.NoError(t, ext.Validate(nil))

	run := func(ctx context.Context, op *ast.OperationDefinition, opName string, vars map[string]any, resp *graphql.Response) (*graphql.Response, int) {
		if op != nil || opName != "" {
			ctx = graphql.WithOperationContext(ctx, &graphql.OperationContext{Operation: op, OperationName: opName, Variables: vars})
		}
		before := len(rows(t, tdb.DB))
		called := 0
		got := ext.InterceptResponse(ctx, func(context.Context) *graphql.Response { called++; return resp })
		assert.Equal(t, 1, called, "the operation always runs exactly once")
		return got, len(rows(t, tdb.DB)) - before
	}

	t.Run("a staff mutation records one row per top-level field, in order", func(t *testing.T) {
		op := operation(t, `mutation SetPrep($id: ID!) { updateOrder(id: $id) { id } cancelOrder(id: $id) { id } }`)
		resp := &graphql.Response{}
		got, added := run(staffCtx(t.Context(), "admin"), op, "SetPrep", map[string]any{"id": "o1"}, resp)
		assert.Same(t, resp, got)
		require.Equal(t, 2, added)
		all := rows(t, tdb.DB)
		a, b := all[len(all)-2], all[len(all)-1]
		assert.Equal(t, "graphql:updateOrder", a.Action)
		assert.Equal(t, "graphql:cancelOrder", b.Action)
		assert.Equal(t, "SetPrep", *a.OperationName)
		assert.True(t, a.Success)
		assert.JSONEq(t, `{"id":"o1"}`, *a.Variables)
		assert.Equal(t, "admin", a.ActorKind)
	})

	t.Run("the operation name falls back to the one written in the document", func(t *testing.T) {
		op := operation(t, `mutation Named { ping }`)
		_, added := run(staffCtx(t.Context(), "pos"), op, "", nil, &graphql.Response{})
		require.Equal(t, 1, added)
		all := rows(t, tdb.DB)
		assert.Equal(t, "Named", *all[len(all)-1].OperationName)
		assert.Equal(t, "pos", all[len(all)-1].ActorKind)
	})

	t.Run("response errors mark the rows as failed and keep the message", func(t *testing.T) {
		op := operation(t, `mutation { updateOrder(id: "x") { id } }`)
		resp := &graphql.Response{Errors: gqlerror.List{gqlerror.Errorf("order not found")}}
		_, added := run(staffCtx(t.Context(), "admin"), op, "", nil, resp)
		require.Equal(t, 1, added)
		all := rows(t, tdb.DB)
		last := all[len(all)-1]
		assert.False(t, last.Success)
		assert.Contains(t, *last.Error, "order not found")
	})

	t.Run("a nil response is treated as success", func(t *testing.T) {
		op := operation(t, `mutation { ping }`)
		got, added := run(staffCtx(t.Context(), "admin"), op, "", nil, nil)
		assert.Nil(t, got)
		assert.Equal(t, 1, added)
		all := rows(t, tdb.DB)
		assert.True(t, all[len(all)-1].Success)
	})

	t.Run("fragments at the top level are skipped, fields around them recorded", func(t *testing.T) {
		op := operation(t, `mutation { ...Frag ping }  fragment Frag on Mutation { other }`)
		_, added := run(staffCtx(t.Context(), "admin"), op, "", nil, &graphql.Response{})
		assert.Equal(t, 1, added)
	})

	t.Run("queries and subscriptions are not audited", func(t *testing.T) {
		for _, q := range []string{`query { orders { id } }`, `subscription { orderUpdated { id } }`} {
			_, added := run(staffCtx(t.Context(), "admin"), operation(t, q), "", nil, &graphql.Response{})
			assert.Zero(t, added, q)
		}
	})

	t.Run("customer mutations are not audited", func(t *testing.T) {
		_, added := run(utils.SetUserID(t.Context(), "customer"), operation(t, `mutation { createOrder }`), "", nil, &graphql.Response{})
		assert.Zero(t, added)
	})

	t.Run("a request without an operation context just runs", func(t *testing.T) {
		called := false
		resp := &graphql.Response{}
		got := ext.InterceptResponse(staffCtx(t.Context(), "admin"), func(context.Context) *graphql.Response { called = true; return resp })
		assert.True(t, called)
		assert.Same(t, resp, got)
	})

	t.Run("an operation context without a parsed operation just runs", func(t *testing.T) {
		_, added := run(staffCtx(t.Context(), "admin"), nil, "orphan", nil, &graphql.Response{})
		assert.Zero(t, added)
	})
}
