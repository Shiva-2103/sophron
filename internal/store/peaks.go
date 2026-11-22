package store

import (
	"context"
	"time"

	"github.com/Shiva-2103/sophron/internal/cost"
)

// OutputPeaks returns, per workload, the highest output tokens/s seen in one
// cycle (all its pods added up) since a time, and how many cycles had data.
// SQLite does the work, so the API doesn't load days of snapshots.
func (s *Store) OutputPeaks(ctx context.Context, since time.Time) ([]cost.Peak, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT namespace, kind, name, MAX(total), COUNT(*) FROM (
		  SELECT namespace,
		         COALESCE(NULLIF(workload_kind, ''), 'Pod') AS kind,
		         COALESCE(NULLIF(workload_name, ''), pod)   AS name,
		         time, SUM(generation_tokens_per_second)    AS total
		  FROM snapshots
		  WHERE time >= ? AND generation_tokens_per_second IS NOT NULL
		  GROUP BY namespace, kind, name, time)
		GROUP BY namespace, kind, name`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cost.Peak
	for rows.Next() {
		var p cost.Peak
		if err := rows.Scan(&p.Namespace, &p.Kind, &p.Name, &p.Max, &p.Cycles); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
