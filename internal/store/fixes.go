package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Shiva-2103/sophron/internal/fixes"
)

const fixesSchema = `
CREATE TABLE IF NOT EXISTS fix_requests (
  id           INTEGER PRIMARY KEY,
  finding_id   INTEGER NOT NULL,
  finding_key  TEXT NOT NULL,
  title        TEXT NOT NULL,
  severity     TEXT,
  action       TEXT NOT NULL, -- JSON
  mode         TEXT NOT NULL,
  status       TEXT NOT NULL,
  requested_at INTEGER NOT NULL,
  decided_by   TEXT,
  decided_at   INTEGER,
  reason       TEXT,
  applied_at   INTEGER,
  result       TEXT,
  verified_at  INTEGER
);
CREATE INDEX IF NOT EXISTS fix_requests_finding ON fix_requests (finding_id);
CREATE INDEX IF NOT EXISTS fix_requests_status ON fix_requests (status);

-- Append-only: each row's hash covers the previous row's hash.
CREATE TABLE IF NOT EXISTS audit_log (
  id     INTEGER PRIMARY KEY,
  at     INTEGER NOT NULL,
  actor  TEXT NOT NULL,
  event  TEXT NOT NULL,
  target TEXT NOT NULL,
  detail TEXT,
  hash   TEXT NOT NULL
);`

const fixColumns = `id, finding_id, finding_key, title, severity, action, mode, status, requested_at,
  decided_by, decided_at, reason, applied_at, result, verified_at`

// InsertFix stores a new fix request.
func (s *Store) InsertFix(ctx context.Context, r fixes.Request) (int64, error) {
	a, err := json.Marshal(r.Action)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO fix_requests
		(finding_id, finding_key, title, severity, action, mode, status, requested_at, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.FindingID, r.FindingKey, r.Title, r.Severity, string(a), r.Mode, r.Status, r.RequestedAt.UnixMilli(), r.Reason)
	if err != nil {
		return 0, fmt.Errorf("insert fix: %w", err)
	}
	return res.LastInsertId()
}

// UpdateFix saves a request's status and outcome.
func (s *Store) UpdateFix(ctx context.Context, r fixes.Request) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fix_requests SET status = ?, decided_by = ?, decided_at = ?, reason = ?,
		applied_at = ?, result = ?, verified_at = ? WHERE id = ?`,
		r.Status, r.DecidedBy, ms(r.DecidedAt), r.Reason, ms(r.AppliedAt), r.Result, ms(r.VerifiedAt), r.ID)
	return err
}

// ErrNotFound means no row has that id.
var ErrNotFound = errors.New("not found")

// Fix returns one request.
func (s *Store) Fix(ctx context.Context, id int64) (fixes.Request, error) {
	list, err := s.fixes(ctx, "SELECT "+fixColumns+" FROM fix_requests WHERE id = ?", id)
	if err != nil {
		return fixes.Request{}, err
	}
	if len(list) == 0 {
		return fixes.Request{}, ErrNotFound
	}
	return list[0], nil
}

// Fixes returns requests, newest first; status "" means all.
func (s *Store) Fixes(ctx context.Context, status string, limit int) ([]fixes.Request, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if status == "" {
		return s.fixes(ctx, "SELECT "+fixColumns+" FROM fix_requests ORDER BY id DESC LIMIT ?", limit)
	}
	return s.fixes(ctx, "SELECT "+fixColumns+" FROM fix_requests WHERE status = ? ORDER BY id DESC LIMIT ?", status, limit)
}

// FixesForFinding returns a finding's requests.
func (s *Store) FixesForFinding(ctx context.Context, findingID int64) ([]fixes.Request, error) {
	return s.fixes(ctx, "SELECT "+fixColumns+" FROM fix_requests WHERE finding_id = ? ORDER BY id", findingID)
}

func (s *Store) fixes(ctx context.Context, q string, args ...any) ([]fixes.Request, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fixes.Request
	for rows.Next() {
		var (
			r                            fixes.Request
			action                       string
			severity, by, reason, result sql.NullString
			requested                    int64
			decided, applied, verified   sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.FindingID, &r.FindingKey, &r.Title, &severity, &action, &r.Mode, &r.Status,
			&requested, &by, &decided, &reason, &applied, &result, &verified); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(action), &r.Action); err != nil {
			return nil, fmt.Errorf("fix %d action: %w", r.ID, err)
		}
		r.Severity, r.DecidedBy, r.Reason, r.Result = severity.String, by.String, reason.String, result.String
		r.RequestedAt = time.UnixMilli(requested).UTC()
		r.DecidedAt, r.AppliedAt, r.VerifiedAt = fromMS(decided), fromMS(applied), fromMS(verified)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AppendAudit adds a line to the audit log, chained to the previous one.
func (s *Store) AppendAudit(ctx context.Context, e fixes.AuditEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var prev string
	if err := tx.QueryRowContext(ctx, "SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1").Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	e.Hash = auditHash(prev, e)
	if _, err := tx.ExecContext(ctx, "INSERT INTO audit_log (at, actor, event, target, detail, hash) VALUES (?, ?, ?, ?, ?, ?)",
		e.At.UnixMilli(), e.Actor, e.Event, e.Target, e.Detail, e.Hash); err != nil {
		return err
	}
	return tx.Commit()
}

func auditHash(prev string, e fixes.AuditEntry) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%s", prev, e.At.UnixMilli(), e.Actor, e.Event, e.Target, e.Detail)))
	return hex.EncodeToString(h[:])
}

// Audit returns the newest entries first.
func (s *Store) Audit(ctx context.Context, limit int) ([]fixes.AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, at, actor, event, target, detail, hash FROM audit_log ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fixes.AuditEntry
	for rows.Next() {
		var (
			e      fixes.AuditEntry
			at     int64
			detail sql.NullString
		)
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Event, &e.Target, &detail, &e.Hash); err != nil {
			return nil, err
		}
		e.At, e.Detail = time.UnixMilli(at).UTC(), detail.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifyAudit recomputes the chain and returns the first broken entry id, or 0.
func (s *Store) VerifyAudit(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, at, actor, event, target, detail, hash FROM audit_log ORDER BY id")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		var (
			e      fixes.AuditEntry
			at     int64
			detail sql.NullString
		)
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Event, &e.Target, &detail, &e.Hash); err != nil {
			return 0, err
		}
		e.At, e.Detail = time.UnixMilli(at).UTC(), detail.String
		if auditHash(prev, e) != e.Hash {
			return e.ID, nil
		}
		prev = e.Hash
	}
	return 0, rows.Err()
}

// PendingFix returns the fix waiting for a decision on a finding, if any.
func (s *Store) PendingFix(ctx context.Context, findingID int64) (fixes.Request, bool) {
	list, err := s.fixes(ctx, "SELECT "+fixColumns+" FROM fix_requests WHERE finding_id = ? AND status = 'pending' ORDER BY id DESC LIMIT 1", findingID)
	if err != nil || len(list) == 0 {
		return fixes.Request{}, false
	}
	return list[0], true
}
