// Package audit records privileged (staff) actions in the staff_audit_log
// table: every GraphQL mutation run by an admin or a POS device, plus staff
// security changes such as MFA enrollment. The log is append-only (enforced by
// a trigger) and recording never fails the action it describes.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/jmoiron/sqlx"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// maxVariablesBytes caps the stored variables payload so a bulk mutation
// cannot bloat the log.
const maxVariablesBytes = 8 << 10

// Entry is one audit record.
type Entry struct {
	Action        string // e.g. "graphql:updateOrder", "mfa.totp.enable"
	OperationName string
	Variables     map[string]any
	Success       bool
	Error         string
}

// Recorder writes audit entries.
type Recorder struct {
	db *sqlx.DB
}

// NewRecorder returns a Recorder writing to db (use the admin pool).
func NewRecorder(db *sqlx.DB) *Recorder {
	return &Recorder{db: db}
}

// Record stores an entry for the staff principal in ctx. Non-staff callers
// are ignored. Errors are logged, never returned.
func (r *Recorder) Record(ctx context.Context, e Entry) {
	if r == nil || r.db == nil {
		return
	}
	actorKind := ""
	switch {
	case utils.GetIsPOS(ctx):
		actorKind = "pos"
	case utils.GetIsAdmin(ctx):
		actorKind = "admin"
	default:
		return
	}

	var vars []byte
	if len(e.Variables) > 0 {
		b, err := json.Marshal(sanitize(e.Variables))
		switch {
		case err != nil:
			vars, _ = json.Marshal(map[string]string{"_error": "unserializable variables"})
		case len(b) > maxVariablesBytes:
			vars, _ = json.Marshal(map[string]any{"_truncated": true, "_bytes": len(b)})
		default:
			vars = b
		}
	}

	// Detached from the request so a client disconnect cannot drop the row.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, err := r.db.ExecContext(writeCtx, `
		INSERT INTO staff_audit_log
			(actor_kind, actor_id, zitadel_sub, action, operation_name, variables, success, error, request_id, ip)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''))`,
		actorKind, utils.GetUserID(ctx), utils.GetZitadelSub(ctx), e.Action, e.OperationName,
		nullableJSON(vars), e.Success, e.Error, logging.GetRequestID(ctx), utils.GetClientIP(ctx))
	if err != nil {
		logging.FromContext(ctx).Error("failed to write staff audit log", zap.String("action", e.Action), zap.Error(err))
	}
}

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// sanitize replaces file uploads with their metadata so variables serialize.
func sanitize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = sanitize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitize(val)
		}
		return out
	case graphql.Upload:
		return map[string]any{"file": t.Filename, "size": t.Size}
	case *graphql.Upload:
		if t == nil {
			return nil
		}
		return map[string]any{"file": t.Filename, "size": t.Size}
	default:
		return v
	}
}

// GraphQLExtension is a gqlgen handler extension that audits every mutation
// executed by a staff principal.
type GraphQLExtension struct {
	Recorder *Recorder
}

var (
	_ graphql.HandlerExtension    = GraphQLExtension{}
	_ graphql.ResponseInterceptor = GraphQLExtension{}
)

func (GraphQLExtension) ExtensionName() string { return "StaffAudit" }

func (GraphQLExtension) Validate(graphql.ExecutableSchema) error { return nil }

func (x GraphQLExtension) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	if !graphql.HasOperationContext(ctx) || !utils.GetIsStaff(ctx) {
		return next(ctx)
	}
	oc := graphql.GetOperationContext(ctx)
	if oc.Operation == nil || oc.Operation.Operation != ast.Mutation {
		return next(ctx)
	}

	resp := next(ctx)

	var errMsg string
	if resp != nil && len(resp.Errors) > 0 {
		errMsg = resp.Errors.Error()
	}
	// OperationName is only set when the client sends operationName; fall back
	// to the name written in the document (mutation SetPrep { ... }).
	opName := oc.OperationName
	if opName == "" {
		opName = oc.Operation.Name
	}
	for _, sel := range oc.Operation.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		x.Recorder.Record(ctx, Entry{
			Action:        "graphql:" + field.Name,
			OperationName: opName,
			Variables:     oc.Variables,
			Success:       errMsg == "",
			Error:         errMsg,
		})
	}
	return resp
}
