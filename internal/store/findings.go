package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Shiva-2103/sophron/internal/rules"
)

// One row per finding. A finding is "open" while the problem lasts and
// becomes "resolved" when it goes away; the same problem coming back later
// opens a new row, so the history stays honest.
const findingsSchema = `
CREATE TABLE IF NOT EXISTS findings (
  id            INTEGER PRIMARY KEY,
  key           TEXT NOT NULL,
  detector      TEXT NOT NULL,
  severity      TEXT NOT NULL,
  namespace     TEXT NOT NULL,
  pod           TEXT,
  workload_kind TEXT,
  workload_name TEXT,
  title         TEXT NOT NULL,
  summary       TEXT,
  evidence      TEXT, -- JSON array
  fix           TEXT, -- JSON object
  status        TEXT NOT NULL,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL,
  resolved_at   INTEGER
);
-- At most one open finding per key.
CREATE UNIQUE INDEX IF NOT EXISTS findings_open_key ON findings (key) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS findings_last_seen ON findings (last_seen);
`

const findingColumns = `id, key, detector, severity, namespace, pod, workload_kind, workload_name,
  title, summary, evidence, fix, status, first_seen, last_seen, resolved_at`

// OpenFindings returns every open finding.
func (s *Store) OpenFindings(ctx context.Context) ([]rules.Finding, error) {
	return s.findings(ctx, "SELECT "+findingColumns+" FROM findings WHERE status = 'open' ORDER BY id")
}

// Findings returns findings, newest first. status may be "open", "resolved"
// or "" for both.
func (s *Store) Findings(ctx context.Context, status string, limit int) ([]rules.Finding, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if status == "" {
		return s.findings(ctx, "SELECT "+findingColumns+" FROM findings ORDER BY last_seen DESC, id DESC LIMIT ?", limit)
	}
	return s.findings(ctx, "SELECT "+findingColumns+" FROM findings WHERE status = ? ORDER BY last_seen DESC, id DESC LIMIT ?", status, limit)
}

// InsertFinding stores a new open finding and returns its ID.
func (s *Store) InsertFinding(ctx context.Context, f rules.Finding) (int64, error) {
	ev, fix, err := encode(f)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO findings
		(key, detector, severity, namespace, pod, workload_kind, workload_name, title, summary, evidence, fix, status, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		f.Key, f.Detector, string(f.Severity), f.Namespace, f.Pod, f.WorkloadKind, f.WorkloadName,
		f.Title, f.Summary, ev, fix, f.FirstSeen.UnixMilli(), f.LastSeen.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("insert finding %s: %w", f.Key, err)
	}
	return res.LastInsertId()
}

// UpdateFinding refreshes an open finding with the latest details.
func (s *Store) UpdateFinding(ctx context.Context, f rules.Finding) error {
	ev, fix, err := encode(f)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE findings SET severity = ?, pod = ?, title = ?, summary = ?,
		evidence = ?, fix = ?, last_seen = ? WHERE id = ?`,
		string(f.Severity), f.Pod, f.Title, f.Summary, ev, fix, f.LastSeen.UnixMilli(), f.ID)
	return err
}

// ResolveFinding marks a finding as resolved.
func (s *Store) ResolveFinding(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE findings SET status = 'resolved', resolved_at = ? WHERE id = ?`,
		at.UnixMilli(), id)
	return err
}

// PruneFindings deletes resolved findings older than the retention period.
func (s *Store) PruneFindings(ctx context.Context, retention time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM findings WHERE status = 'resolved' AND resolved_at < ?",
		time.Now().Add(-retention).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func encode(f rules.Finding) (evidence, fix string, err error) {
	ev, err := json.Marshal(f.Evidence)
	if err != nil {
		return "", "", err
	}
	fx, err := json.Marshal(f.Fix)
	if err != nil {
		return "", "", err
	}
	return string(ev), string(fx), nil
}

func (s *Store) findings(ctx context.Context, q string, args ...any) ([]rules.Finding, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rules.Finding
	for rows.Next() {
		var (
			f                                         rules.Finding
			sev                                       string
			pod, kind, name, summary, evidence, fixJS sql.NullString
			first, last                               int64
			resolved                                  sql.NullInt64
		)
		if err := rows.Scan(&f.ID, &f.Key, &f.Detector, &sev, &f.Namespace, &pod, &kind, &name,
			&f.Title, &summary, &evidence, &fixJS, &f.Status, &first, &last, &resolved); err != nil {
			return nil, err
		}
		f.Severity = rules.Severity(sev)
		f.Pod, f.WorkloadKind, f.WorkloadName, f.Summary = pod.String, kind.String, name.String, summary.String
		f.FirstSeen, f.LastSeen, f.ResolvedAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC(), fromMS(resolved)
		if evidence.String != "" {
			if err := json.Unmarshal([]byte(evidence.String), &f.Evidence); err != nil {
				return nil, fmt.Errorf("finding %d evidence: %w", f.ID, err)
			}
		}
		if fixJS.String != "" {
			if err := json.Unmarshal([]byte(fixJS.String), &f.Fix); err != nil {
				return nil, fmt.Errorf("finding %d fix: %w", f.ID, err)
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
