// Package changes persists pending (two-step) changes and the audit log of
// every write the MCP server performs, in SQLite (pure Go, no CGO).
package changes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Status of a pending change.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApplied  Status = "applied"
	StatusRejected Status = "rejected"
	StatusExpired  Status = "expired"
	StatusConflict Status = "conflict"
	StatusFailed   Status = "failed"
)

// Audit outcomes.
const (
	OutcomeApplied  = "applied"
	OutcomeRejected = "rejected"
	OutcomeFailed   = "failed"
	OutcomeConflict = "conflict"
	OutcomeExpired  = "expired"
)

// ErrNotFound is returned when a change does not exist.
var ErrNotFound = errors.New("not found")

// Change is a proposed sensitive change waiting for the owner's confirmation.
type Change struct {
	ID             string          `json:"change_id"`
	Tool           string          `json:"tool"`
	Kind           string          `json:"kind"`
	EntityType     string          `json:"entity_type"`
	EntityID       string          `json:"entity_id"`
	Params         json.RawMessage `json:"params"`
	Before         json.RawMessage `json:"before"`
	BeforeHash     string          `json:"-"`
	Blob           []byte          `json:"-"`
	Summary        string          `json:"summary"`
	SummaryZh      string          `json:"summary_zh"`
	RequestContext string          `json:"request_context,omitempty"`
	Status         Status          `json:"status"`
	UndoOf         *int64          `json:"undo_of,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	Error          string          `json:"error,omitempty"`
}

// AuditEntry is one write performed (or refused) by the MCP server.
type AuditEntry struct {
	ID             int64           `json:"id"`
	At             time.Time       `json:"at"`
	Source         string          `json:"source"` // tool name or internal endpoint
	ChangeID       string          `json:"change_id,omitempty"`
	Kind           string          `json:"kind"`
	Risk           string          `json:"risk"`
	EntityType     string          `json:"entity_type"`
	EntityID       string          `json:"entity_id"`
	Params         json.RawMessage `json:"params,omitempty"`
	Before         json.RawMessage `json:"before,omitempty"`
	After          json.RawMessage `json:"after,omitempty"`
	Summary        string          `json:"summary"`
	SummaryZh      string          `json:"summary_zh"`
	RequestContext string          `json:"request_context,omitempty"`
	Outcome        string          `json:"outcome"`
	Error          string          `json:"error,omitempty"`
	UndoOf         *int64          `json:"undo_of,omitempty"`
	UndoneBy       *int64          `json:"undone_by,omitempty"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS changes (
  id              TEXT PRIMARY KEY,
  tool            TEXT NOT NULL,
  kind            TEXT NOT NULL,
  entity_type     TEXT NOT NULL,
  entity_id       TEXT NOT NULL,
  params          TEXT NOT NULL,
  before_state    TEXT NOT NULL,
  before_hash     TEXT NOT NULL,
  blob            BLOB,
  summary         TEXT NOT NULL,
  summary_zh      TEXT NOT NULL DEFAULT '',
  request_context TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL,
  undo_of         INTEGER,
  created_at      TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  decided_at      TEXT,
  error           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS changes_status ON changes(status, expires_at);

CREATE TABLE IF NOT EXISTS audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  at              TEXT NOT NULL,
  source          TEXT NOT NULL,
  change_id       TEXT NOT NULL DEFAULT '',
  kind            TEXT NOT NULL,
  risk            TEXT NOT NULL,
  entity_type     TEXT NOT NULL,
  entity_id       TEXT NOT NULL,
  params          TEXT,
  before_state    TEXT,
  after_state     TEXT,
  summary         TEXT NOT NULL,
  summary_zh      TEXT NOT NULL DEFAULT '',
  request_context TEXT NOT NULL DEFAULT '',
  outcome         TEXT NOT NULL,
  error           TEXT NOT NULL DEFAULT '',
  undo_of         INTEGER,
  undone_by       INTEGER
);
CREATE INDEX IF NOT EXISTS audit_log_at ON audit_log(at);
`

// Open opens (and migrates) the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite allows one writer; a single connection also keeps :memory: shared.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	if err := addColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

// addedColumns are columns added after the first release. CREATE TABLE IF NOT
// EXISTS leaves existing tables unchanged, so they are added here.
var addedColumns = []struct{ table, column, def string }{
	{"changes", "summary_zh", "TEXT NOT NULL DEFAULT ''"},
	{"audit_log", "summary_zh", "TEXT NOT NULL DEFAULT ''"},
}

func addColumns(db *sql.DB) error {
	for _, c := range addedColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.column + " " + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// CreateChange inserts a pending change.
func (s *Store) CreateChange(ctx context.Context, c *Change) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO changes (id, tool, kind, entity_type, entity_id, params, before_state, before_hash, blob,
		                     summary, summary_zh, request_context, status, undo_of, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Tool, c.Kind, c.EntityType, c.EntityID, string(c.Params), string(c.Before), c.BeforeHash, c.Blob,
		c.Summary, c.SummaryZh, c.RequestContext, string(c.Status), c.UndoOf, ts(c.CreatedAt), ts(c.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert change: %w", err)
	}
	return nil
}

// GetChange loads a change by id.
func (s *Store) GetChange(ctx context.Context, id string) (*Change, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tool, kind, entity_type, entity_id, params, before_state, before_hash, blob, summary, summary_zh,
		       request_context, status, undo_of, created_at, expires_at, decided_at, error
		FROM changes WHERE id = ?`, id)
	var (
		c                     Change
		params, before        string
		status, created, exps string
		decided               sql.NullString
		undoOf                sql.NullInt64
	)
	err := row.Scan(&c.ID, &c.Tool, &c.Kind, &c.EntityType, &c.EntityID, &params, &before, &c.BeforeHash, &c.Blob,
		&c.Summary, &c.SummaryZh, &c.RequestContext, &status, &undoOf, &created, &exps, &decided, &c.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get change: %w", err)
	}
	c.Params, c.Before = json.RawMessage(params), json.RawMessage(before)
	c.Status = Status(status)
	c.CreatedAt, c.ExpiresAt = parseTS(created), parseTS(exps)
	if decided.Valid {
		t := parseTS(decided.String)
		c.DecidedAt = &t
	}
	if undoOf.Valid {
		c.UndoOf = &undoOf.Int64
	}
	return &c, nil
}

// TransitionChange moves a change from one status to another atomically. It
// returns false when the change was not in the expected status.
func (s *Store) TransitionChange(ctx context.Context, id string, from, to Status, errMsg string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE changes SET status = ?, decided_at = ?, error = ? WHERE id = ? AND status = ?`,
		string(to), ts(at), errMsg, id, string(from))
	if err != nil {
		return false, fmt.Errorf("transition change: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AppendAudit writes an audit entry and returns its id.
func (s *Store) AppendAudit(ctx context.Context, e *AuditEntry) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (at, source, change_id, kind, risk, entity_type, entity_id, params, before_state,
		                       after_state, summary, summary_zh, request_context, outcome, error, undo_of)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ts(e.At), e.Source, e.ChangeID, e.Kind, e.Risk, e.EntityType, e.EntityID, nullJSON(e.Params), nullJSON(e.Before),
		nullJSON(e.After), e.Summary, e.SummaryZh, e.RequestContext, e.Outcome, e.Error, e.UndoOf)
	if err != nil {
		return 0, fmt.Errorf("append audit: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("append audit: %w", err)
	}
	e.ID = id
	return id, nil
}

// MarkUndone links an audit entry to the entry that reverted it.
func (s *Store) MarkUndone(ctx context.Context, auditID, undoneBy int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE audit_log SET undone_by = ? WHERE id = ? AND undone_by IS NULL`, undoneBy, auditID)
	if err != nil {
		return fmt.Errorf("mark undone: %w", err)
	}
	return nil
}

const auditColumns = `id, at, source, change_id, kind, risk, entity_type, entity_id, params, before_state, after_state,
	summary, summary_zh, request_context, outcome, error, undo_of, undone_by`

func scanAudit(rows interface{ Scan(...any) error }) (*AuditEntry, error) {
	var (
		e                     AuditEntry
		at                    string
		params, before, after sql.NullString
		undoOf, undoneBy      sql.NullInt64
	)
	if err := rows.Scan(&e.ID, &at, &e.Source, &e.ChangeID, &e.Kind, &e.Risk, &e.EntityType, &e.EntityID, &params, &before, &after,
		&e.Summary, &e.SummaryZh, &e.RequestContext, &e.Outcome, &e.Error, &undoOf, &undoneBy); err != nil {
		return nil, err
	}
	e.At = parseTS(at)
	if params.Valid {
		e.Params = json.RawMessage(params.String)
	}
	if before.Valid {
		e.Before = json.RawMessage(before.String)
	}
	if after.Valid {
		e.After = json.RawMessage(after.String)
	}
	if undoOf.Valid {
		e.UndoOf = &undoOf.Int64
	}
	if undoneBy.Valid {
		e.UndoneBy = &undoneBy.Int64
	}
	return &e, nil
}

func (s *Store) queryAudit(ctx context.Context, q string, args ...any) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// RecentAudit returns the n most recent audit entries, newest first.
func (s *Store) RecentAudit(ctx context.Context, n int) ([]AuditEntry, error) {
	return s.queryAudit(ctx, `SELECT `+auditColumns+` FROM audit_log ORDER BY id DESC LIMIT ?`, n)
}

// GetAudit loads one audit entry.
func (s *Store) GetAudit(ctx context.Context, id int64) (*AuditEntry, error) {
	es, err := s.queryAudit(ctx, `SELECT `+auditColumns+` FROM audit_log WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return nil, ErrNotFound
	}
	return &es[0], nil
}

// LastUndoable returns the most recent applied change at or after since that
// has not been undone and is not itself an undo. Repeated undos therefore walk
// back through history instead of redoing.
func (s *Store) LastUndoable(ctx context.Context, since time.Time) (*AuditEntry, error) {
	es, err := s.queryAudit(ctx, `SELECT `+auditColumns+` FROM audit_log
		WHERE outcome = ? AND undone_by IS NULL AND undo_of IS NULL AND at >= ?
		ORDER BY id DESC LIMIT 1`, OutcomeApplied, ts(since))
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return nil, ErrNotFound
	}
	return &es[0], nil
}

// AppliedSince returns applied entries of the given kind at or after since,
// newest first.
func (s *Store) AppliedSince(ctx context.Context, kind string, since time.Time) ([]AuditEntry, error) {
	return s.queryAudit(ctx, `SELECT `+auditColumns+` FROM audit_log
		WHERE outcome = ? AND kind = ? AND at >= ? ORDER BY id DESC`, OutcomeApplied, kind, ts(since))
}

// PendingUndoFor reports whether a pending undo already exists for auditID.
func (s *Store) PendingUndoFor(ctx context.Context, auditID int64, now time.Time) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM changes WHERE undo_of = ? AND status = ? AND expires_at > ? LIMIT 1`,
		auditID, string(StatusPending), ts(now)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
