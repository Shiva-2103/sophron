package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/notify"
	"github.com/Shiva-2103/sophron/internal/rules"
)

func f(v float64) *float64 { return &v }

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSaveAndLatest(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := collector.Snapshot{Time: now.Add(-time.Minute), Namespace: "llm", Pod: "a", Server: "llamacpp", Phase: "Running", CPUCores: f(1)}
	newer := collector.Snapshot{
		Time: now, Namespace: "llm", Pod: "a", Server: "llamacpp", Phase: "Running", Ready: true, Restarts: 2,
		LastTerminationReason: "OOMKilled", Node: "w1", NodeInstanceType: "g6.xlarge", NodeGPUs: 1,
		GenerationTokensPerSecond: f(48.1), CPUCores: f(5.85), StartupSeconds: f(90),
	}
	other := collector.Snapshot{Time: now, Namespace: "llm", Pod: "b", Server: "vllm", Phase: "Pending"}
	if err := s.Save(ctx, []collector.Snapshot{old, newer, other}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Latest(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want one per pod (2)", len(got))
	}
	a := got[0]
	if !a.Time.Equal(now) || !a.Ready || a.Restarts != 2 || a.LastTerminationReason != "OOMKilled" || a.NodeGPUs != 1 {
		t.Errorf("round trip lost data: %+v", a)
	}
	if a.GenerationTokensPerSecond == nil || *a.GenerationTokensPerSecond != 48.1 {
		t.Errorf("tokens/s = %v", a.GenerationTokensPerSecond)
	}
	if got[1].Pod != "b" || got[1].CPUCores != nil {
		t.Errorf("missing metrics must stay nil: %+v", got[1])
	}
}

func TestHistoryAndPrune(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var snaps []collector.Snapshot
	for i := range 5 {
		snaps = append(snaps, collector.Snapshot{Time: now.Add(-time.Duration(i) * time.Hour), Namespace: "llm", Pod: "a", Server: "x"})
	}
	if err := s.Save(ctx, snaps); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(ctx, "llm", "a", now.Add(-150*time.Minute))
	if err != nil || len(h) != 3 {
		t.Fatalf("history = %d rows, %v; want 3", len(h), err)
	}
	if !h[0].Time.Before(h[2].Time) {
		t.Error("history should be oldest first")
	}
	n, err := s.Prune(ctx, 90*time.Minute)
	if err != nil || n != 3 {
		t.Fatalf("pruned %d, %v; want 3", n, err)
	}
}

func TestMigrationAddsNewColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase2.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an older database by dropping the newer columns.
	for _, c := range []string{"snapshots DROP COLUMN revision", "snapshots DROP COLUMN created_at",
		"snapshots DROP COLUMN started_at", "snapshots DROP COLUMN ready_at", "snapshots DROP COLUMN memory_request_bytes",
		"snapshots DROP COLUMN cost_per_hour", "snapshots DROP COLUMN price_source", "findings DROP COLUMN resolution"} {
		if _, err := s.db.Exec("ALTER TABLE " + c); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen with migration: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err := s.Save(ctx, []collector.Snapshot{{Time: time.Now().UTC(), Namespace: "llm", Pod: "a", Server: "x", Revision: "abc", CreatedAt: created}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Since(ctx, time.Now().Add(-time.Minute))
	if err != nil || len(got) != 1 || got[0].Revision != "abc" || !got[0].CreatedAt.Equal(created) || !got[0].StartedAt.IsZero() {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.Findings(ctx, "", 10); err != nil {
		t.Fatalf("findings after migration: %v", err)
	}
}

func TestFindingsLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	fd := rules.Finding{
		Key: "oom-killed/llm/llm-server", Detector: "oom-killed", Severity: rules.Critical,
		Namespace: "llm", Pod: "p1", WorkloadKind: "Deployment", WorkloadName: "llm-server",
		Title: "killed", Summary: "too much memory", Evidence: []string{"limit 512 MiB"},
		Fix:       rules.Fix{Summary: "more memory", Steps: []string{"raise"}, Snippet: "memory: 768Mi"},
		FirstSeen: now, LastSeen: now,
	}
	id, err := s.InsertFinding(ctx, fd)
	if err != nil {
		t.Fatal(err)
	}
	// Only one open finding per key.
	if _, err := s.InsertFinding(ctx, fd); err == nil {
		t.Error("a second open finding with the same key should be rejected")
	}

	fd.ID, fd.Pod, fd.LastSeen = id, "p2", now.Add(time.Minute)
	if err := s.UpdateFinding(ctx, fd); err != nil {
		t.Fatal(err)
	}
	open, err := s.OpenFindings(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open = %+v, %v", open, err)
	}
	g := open[0]
	if g.Pod != "p2" || g.Fix.Snippet != "memory: 768Mi" || g.Evidence[0] != "limit 512 MiB" || !g.LastSeen.Equal(now.Add(time.Minute)) {
		t.Errorf("round trip lost data: %+v", g)
	}

	res := &rules.Resolution{Summary: "Fixed by a change to the workload.", Changes: []string{"memory limit 512Mi → 6Gi"}, ChangedBy: "kubectl apply"}
	if err := s.ResolveFinding(ctx, id, now.Add(2*time.Minute), res); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenFindings(ctx); len(open) != 0 {
		t.Errorf("want none open, got %d", len(open))
	}
	// The same problem can open again after it resolved.
	if _, err := s.InsertFinding(ctx, fd); err != nil {
		t.Errorf("reopen after resolve: %v", err)
	}
	all, _ := s.Findings(ctx, "", 10)
	resolved, _ := s.Findings(ctx, rules.StatusResolved, 10)
	if len(all) != 2 || len(resolved) != 1 || resolved[0].ResolvedAt.IsZero() {
		t.Fatalf("all=%d resolved=%+v", len(all), resolved)
	}
	if r := resolved[0].Resolution; r == nil || r.ChangedBy != "kubectl apply" || r.Changes[0] != "memory limit 512Mi → 6Gi" {
		t.Errorf("resolution lost: %+v", r)
	}
}

func TestAlertMarks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.SaveAlertMark(ctx, "oom-killed/llm/x", notify.AlertMark{LastSent: at, Muted: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAlertMark(ctx, "oom-killed/llm/x", notify.AlertMark{LastSent: at}); err != nil {
		t.Fatal(err) // upsert
	}
	m, err := s.AlertMarks(ctx)
	if err != nil || len(m) != 1 || !m["oom-killed/llm/x"].LastSent.Equal(at) || m["oom-killed/llm/x"].Muted {
		t.Fatalf("marks = %+v, %v", m, err)
	}
	if err := s.SaveAlertMark(ctx, "oom-killed/llm/x", notify.AlertMark{}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.AlertMarks(ctx); len(m) != 0 {
		t.Errorf("empty mark should delete the row, got %+v", m)
	}
}

func TestOutputPeaks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	tok := func(v float64) *float64 { return &v }
	mk := func(pod string, at time.Time, v float64) collector.Snapshot {
		return collector.Snapshot{Time: at, Namespace: "llm", Pod: pod, Server: "vllm",
			WorkloadKind: "Deployment", WorkloadName: "chat", GenerationTokensPerSecond: tok(v)}
	}
	// Two pods in one cycle add up (30 + 50 = 80); another cycle has 60.
	snaps := []collector.Snapshot{mk("a", now.Add(-time.Minute), 30), mk("b", now.Add(-time.Minute), 50), mk("a", now, 60),
		{Time: now, Namespace: "llm", Pod: "solo", Server: "llamacpp", GenerationTokensPerSecond: tok(5)}}
	if err := s.Save(ctx, snaps); err != nil {
		t.Fatal(err)
	}
	peaks, err := s.OutputPeaks(ctx, now.Add(-time.Hour))
	if err != nil || len(peaks) != 2 {
		t.Fatalf("peaks = %+v, %v", peaks, err)
	}
	for _, p := range peaks {
		switch p.Name {
		case "chat":
			if p.Kind != "Deployment" || p.Max != 80 || p.Cycles != 2 {
				t.Errorf("chat: %+v", p)
			}
		case "solo":
			if p.Kind != "Pod" || p.Max != 5 {
				t.Errorf("a pod without a workload is its own group: %+v", p)
			}
		default:
			t.Errorf("unexpected %+v", p)
		}
	}
}
