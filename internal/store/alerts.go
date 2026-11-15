package store

import (
	"context"
	"time"

	"github.com/Shiva-2103/sophron/internal/notify"
)

// Slack cooldown marks, one row per finding key, so a restart doesn't
// forget what was alerted recently.
const alertSchema = `
CREATE TABLE IF NOT EXISTS alert_marks (
  key       TEXT PRIMARY KEY,
  last_sent INTEGER NOT NULL,
  muted     INTEGER NOT NULL DEFAULT 0
);`

// AlertMarks returns every saved cooldown mark.
func (s *Store) AlertMarks(ctx context.Context) (map[string]notify.AlertMark, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, last_sent, muted FROM alert_marks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]notify.AlertMark{}
	for rows.Next() {
		var (
			key   string
			last  int64
			muted bool
		)
		if err := rows.Scan(&key, &last, &muted); err != nil {
			return nil, err
		}
		out[key] = notify.AlertMark{LastSent: time.UnixMilli(last).UTC(), Muted: muted}
	}
	return out, rows.Err()
}

// SaveAlertMark stores one key's mark, or deletes it when there's nothing to remember.
func (s *Store) SaveAlertMark(ctx context.Context, key string, m notify.AlertMark) error {
	if m.LastSent.IsZero() && !m.Muted {
		_, err := s.db.ExecContext(ctx, "DELETE FROM alert_marks WHERE key = ?", key)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_marks (key, last_sent, muted) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET last_sent = excluded.last_sent, muted = excluded.muted`,
		key, m.LastSent.UnixMilli(), m.Muted)
	return err
}

// PruneAlertMarks deletes marks older than the given age.
func (s *Store) PruneAlertMarks(ctx context.Context, olderThan time.Duration) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM alert_marks WHERE last_sent < ?", time.Now().Add(-olderThan).UnixMilli())
	return err
}
