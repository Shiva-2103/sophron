// Package store saves snapshots in SQLite.
//
// SQLite is a single file with no server to run, which suits a small agent.
// The driver is pure Go (no cgo), so the container image can be tiny.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver

	"github.com/Shiva-2103/sophron/internal/collector"
)

const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
  id                          INTEGER PRIMARY KEY,
  time                        INTEGER NOT NULL, -- unix milliseconds
  namespace                   TEXT NOT NULL,
  pod                         TEXT NOT NULL,
  server                      TEXT NOT NULL,
  workload_kind               TEXT,
  workload_name               TEXT,
  phase                       TEXT,
  ready                       INTEGER,
  restarts                    INTEGER,
  waiting_reason              TEXT,
  last_termination_reason     TEXT,
  cpu_request_cores           REAL,
  cpu_limit_cores             REAL,
  memory_limit_bytes          REAL,
  gpu_request                 INTEGER,
  startup_seconds             REAL,
  warning_reason              TEXT,
  warning_message             TEXT,
  node                        TEXT,
  node_provider               TEXT,
  node_instance_type          TEXT,
  node_zone                   TEXT,
  node_capacity_type          TEXT,
  node_cpu_cores              REAL,
  node_memory_bytes           REAL,
  node_gpus                   INTEGER,
  generation_tokens_per_second REAL,
  prompt_tokens_per_second    REAL,
  requests_running            REAL,
  requests_waiting            REAL,
  kv_cache_usage_ratio        REAL,
  cpu_cores                   REAL,
  cpu_throttled_ratio         REAL,
  memory_working_set_bytes    REAL,
  gpu_utilization_ratio       REAL,
  gpu_memory_used_bytes       REAL
);
CREATE INDEX IF NOT EXISTS snapshots_pod_time ON snapshots (namespace, pod, time);
CREATE INDEX IF NOT EXISTS snapshots_time ON snapshots (time);
`

// columns in insert order; must match values() below.
var columns = []string{
	"time", "namespace", "pod", "server", "workload_kind", "workload_name", "phase", "ready",
	"restarts", "waiting_reason", "last_termination_reason", "cpu_request_cores", "cpu_limit_cores",
	"memory_limit_bytes", "gpu_request", "startup_seconds", "warning_reason", "warning_message",
	"node", "node_provider", "node_instance_type", "node_zone", "node_capacity_type",
	"node_cpu_cores", "node_memory_bytes", "node_gpus",
	"generation_tokens_per_second", "prompt_tokens_per_second", "requests_running", "requests_waiting",
	"kv_cache_usage_ratio", "cpu_cores", "cpu_throttled_ratio", "memory_working_set_bytes",
	"gpu_utilization_ratio", "gpu_memory_used_bytes",
}

func values(s collector.Snapshot) []any {
	return []any{
		s.Time.UnixMilli(), s.Namespace, s.Pod, s.Server, s.WorkloadKind, s.WorkloadName, s.Phase, s.Ready,
		s.Restarts, s.WaitingReason, s.LastTerminationReason, s.CPURequestCores, s.CPULimitCores,
		s.MemoryLimitBytes, s.GPURequest, s.StartupSeconds, s.WarningReason, s.WarningMessage,
		s.Node, s.NodeProvider, s.NodeInstanceType, s.NodeZone, s.NodeCapacityType,
		s.NodeCPUCores, s.NodeMemoryBytes, s.NodeGPUs,
		s.GenerationTokensPerSecond, s.PromptTokensPerSecond, s.RequestsRunning, s.RequestsWaiting,
		s.KVCacheUsageRatio, s.CPUCores, s.CPUThrottledRatio, s.MemoryWorkingSetBytes,
		s.GPUUtilizationRatio, s.GPUMemoryUsedBytes,
	}
}

func (s *scanner) targets() []any {
	x := &s.snap
	return []any{
		&s.ms, &x.Namespace, &x.Pod, &x.Server, &s.workloadKind, &s.workloadName, &s.phase, &x.Ready,
		&x.Restarts, &s.waiting, &s.lastTerm, &x.CPURequestCores, &x.CPULimitCores,
		&x.MemoryLimitBytes, &x.GPURequest, &x.StartupSeconds, &s.warnReason, &s.warnMessage,
		&s.node, &s.provider, &s.instanceType, &s.zone, &s.capacityType,
		&s.nodeCPU, &s.nodeMem, &s.nodeGPUs,
		&x.GenerationTokensPerSecond, &x.PromptTokensPerSecond, &x.RequestsRunning, &x.RequestsWaiting,
		&x.KVCacheUsageRatio, &x.CPUCores, &x.CPUThrottledRatio, &x.MemoryWorkingSetBytes,
		&x.GPUUtilizationRatio, &x.GPUMemoryUsedBytes,
	}
}

// scanner reads a row; text columns are nullable in older rows, so they
// go through sql.NullString first.
type scanner struct {
	snap                                                        collector.Snapshot
	ms                                                          int64
	workloadKind, workloadName, phase, waiting, lastTerm        sql.NullString
	warnReason, warnMessage, node, provider, instanceType, zone sql.NullString
	capacityType                                                sql.NullString
	nodeCPU, nodeMem                                            sql.NullFloat64
	nodeGPUs                                                    sql.NullInt64
}

func (s *scanner) result() collector.Snapshot {
	x := s.snap
	x.Time = time.UnixMilli(s.ms).UTC()
	x.WorkloadKind, x.WorkloadName, x.Phase = s.workloadKind.String, s.workloadName.String, s.phase.String
	x.WaitingReason, x.LastTerminationReason = s.waiting.String, s.lastTerm.String
	x.WarningReason, x.WarningMessage = s.warnReason.String, s.warnMessage.String
	x.Node, x.NodeProvider, x.NodeInstanceType = s.node.String, s.provider.String, s.instanceType.String
	x.NodeZone, x.NodeCapacityType = s.zone.String, s.capacityType.String
	x.NodeCPUCores, x.NodeMemoryBytes, x.NodeGPUs = s.nodeCPU.Float64, s.nodeMem.Float64, s.nodeGPUs.Int64
	return x
}

// Store is a SQLite snapshot store.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database file and its schema.
func Open(path string) (*Store, error) {
	// WAL lets the API read while the collector writes. busy_timeout waits
	// for a lock instead of failing at once.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite allows one writer; keep it simple
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Save inserts snapshots in one transaction.
func (s *Store) Save(ctx context.Context, snaps []collector.Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	q := fmt.Sprintf("INSERT INTO snapshots (%s) VALUES (%s)",
		strings.Join(columns, ", "), strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", "))
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, snap := range snaps {
		if _, err := stmt.ExecContext(ctx, values(snap)...); err != nil {
			return fmt.Errorf("insert snapshot %s/%s: %w", snap.Namespace, snap.Pod, err)
		}
	}
	return tx.Commit()
}

// Latest returns the newest snapshot of every pod seen within maxAge.
func (s *Store) Latest(ctx context.Context, maxAge time.Duration) ([]collector.Snapshot, error) {
	since := time.Now().Add(-maxAge).UnixMilli()
	q := fmt.Sprintf(`SELECT %s FROM snapshots s
		WHERE time = (SELECT MAX(time) FROM snapshots t WHERE t.namespace = s.namespace AND t.pod = s.pod)
		AND time >= ? ORDER BY namespace, pod`, strings.Join(columns, ", "))
	return s.query(ctx, q, since)
}

// History returns snapshots of one pod since a time, oldest first.
func (s *Store) History(ctx context.Context, namespace, pod string, since time.Time) ([]collector.Snapshot, error) {
	q := fmt.Sprintf(`SELECT %s FROM snapshots WHERE namespace = ? AND pod = ? AND time >= ? ORDER BY time`,
		strings.Join(columns, ", "))
	return s.query(ctx, q, namespace, pod, since.UnixMilli())
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]collector.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []collector.Snapshot
	for rows.Next() {
		var sc scanner
		if err := rows.Scan(sc.targets()...); err != nil {
			return nil, err
		}
		out = append(out, sc.result())
	}
	return out, rows.Err()
}

// Prune deletes snapshots older than the retention period.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE time < ?", time.Now().Add(-retention).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Ping checks the database is usable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
