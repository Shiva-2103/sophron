package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
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
