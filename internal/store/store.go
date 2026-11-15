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
  gpu_memory_used_bytes       REAL,
  revision                    TEXT,
  created_at                  INTEGER,
  started_at                  INTEGER,
  ready_at                    INTEGER,
  memory_request_bytes        REAL,
  node_region                 TEXT,
  cost_per_hour               REAL,
  node_cost_per_hour          REAL,
  price_source                TEXT,
  container                   TEXT
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
	"revision", "created_at", "started_at", "ready_at",
	"memory_request_bytes", "node_region", "cost_per_hour", "node_cost_per_hour", "price_source", "container",
}

// migrations add columns introduced after the first release, so databases
// created by an older collector keep working. Each entry is column -> type.
type column struct{ column, typ string }

var migrations = []column{
	{"revision", "TEXT"},
	{"created_at", "INTEGER"},
	{"started_at", "INTEGER"},
	{"ready_at", "INTEGER"},
	{"memory_request_bytes", "REAL"},
	{"node_region", "TEXT"},
	{"cost_per_hour", "REAL"},
	{"node_cost_per_hour", "REAL"},
	{"price_source", "TEXT"},
	{"container", "TEXT"},
}

// ms turns a time into unix milliseconds, or NULL when unset.
func ms(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func fromMS(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
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
		s.Revision, ms(s.CreatedAt), ms(s.StartedAt), ms(s.ReadyAt),
		s.MemoryRequestBytes, s.NodeRegion, s.CostPerHour, s.NodeCostPerHour, s.PriceSource, s.Container,
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
		&s.revision, &s.createdAt, &s.startedAt, &s.readyAt,
		&s.memRequest, &s.region, &x.CostPerHour, &x.NodeCostPerHour, &s.priceSource, &s.container,
	}
}

// scanner reads a row; text columns are nullable in older rows, so they
// go through sql.NullString first.
type scanner struct {
	snap                                                        collector.Snapshot
	ms                                                          int64
	workloadKind, workloadName, phase, waiting, lastTerm        sql.NullString
	warnReason, warnMessage, node, provider, instanceType, zone sql.NullString
	capacityType, revision, region, priceSource, container      sql.NullString
	memRequest                                                  sql.NullFloat64
	createdAt, startedAt, readyAt                               sql.NullInt64
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
	x.Revision, x.CreatedAt = s.revision.String, fromMS(s.createdAt)
	x.StartedAt, x.ReadyAt = fromMS(s.startedAt), fromMS(s.readyAt)
	x.MemoryRequestBytes, x.NodeRegion, x.PriceSource = s.memRequest.Float64, s.region.String, s.priceSource.String
	x.Container = s.container.String
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
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if err := migrate(db, "snapshots", migrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if _, err := db.Exec(findingsSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create findings schema: %w", err)
	}
	if _, err := db.Exec(fixesSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create fixes schema: %w", err)
	}
	if _, err := db.Exec(alertSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create alert schema: %w", err)
	}
	if err := migrate(db, "findings", findingMigrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate findings: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate adds any missing columns to a table. Table and column names come
// from constants in this package, never from input.
func migrate(db *sql.DB, table string, cols []column) error {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		have[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, m := range cols {
		if have[m.column] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + m.column + " " + m.typ); err != nil { //nolint:gosec // constant names, see above
			return err
		}
	}
	return nil
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
	//nolint:gosec // G201: only the constant column list is formatted in; values use placeholders
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
	//nolint:gosec // G201: only the constant column list is formatted in; values use placeholders
	q := fmt.Sprintf(`SELECT %s FROM snapshots s
		WHERE time = (SELECT MAX(time) FROM snapshots t WHERE t.namespace = s.namespace AND t.pod = s.pod)
		AND time >= ? ORDER BY namespace, pod`, strings.Join(columns, ", "))
	return s.query(ctx, q, since)
}

// Since returns every snapshot since a time, oldest first. The rules engine
// uses it to look at the recent history of all model pods in one query.
func (s *Store) Since(ctx context.Context, since time.Time) ([]collector.Snapshot, error) {
	//nolint:gosec // G201: only the constant column list is formatted in; values use placeholders
	q := fmt.Sprintf(`SELECT %s FROM snapshots WHERE time >= ? ORDER BY time`, strings.Join(columns, ", "))
	return s.query(ctx, q, since.UnixMilli())
}

// History returns snapshots of one pod since a time, oldest first.
func (s *Store) History(ctx context.Context, namespace, pod string, since time.Time) ([]collector.Snapshot, error) {
	//nolint:gosec // G201: only the constant column list is formatted in; values use placeholders
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
