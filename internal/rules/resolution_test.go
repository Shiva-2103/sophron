package rules

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

func TestExplainResolution(t *testing.T) {
	oomFinding := Finding{Detector: OOMKilled, Namespace: "llm", Pod: "old", WorkloadKind: "Deployment",
		WorkloadName: "llm-server", LastSeen: now.Add(-2 * time.Minute)}
	broken := at(2*time.Minute, pod("old"), func(s *collector.Snapshot) {
		s.Revision, s.MemoryLimitBytes, s.Ready, s.Restarts = "bbb", 512<<20, false, 3
	})

	tests := []struct {
		name        string
		f           Finding
		in          Input
		summary     string
		wantChanges []string
	}{
		{
			name: "fixed by a new version with more memory",
			f:    oomFinding, in: input([]collector.Snapshot{pod("new")}, broken),
			summary:     "Fixed by a change",
			wantChanges: []string{"new version rolled out (revision bbb → aaa)", "memory limit 512Mi → 6Gi", "1 of 1 pods Ready now"},
		},
		{
			name: "queue drained on its own",
			f: Finding{Detector: QueueBacklog, Namespace: "llm", Pod: "a", WorkloadKind: "Deployment",
				WorkloadName: "llm-server", LastSeen: now.Add(-2 * time.Minute)},
			in: input([]collector.Snapshot{pod("a", func(s *collector.Snapshot) { s.RequestsWaiting = f(0) })},
				at(2*time.Minute, pod("a"), func(s *collector.Snapshot) { s.RequestsWaiting = f(3) })),
			summary:     "Cleared without a change to the workload: the queue drained",
			wantChanges: []string{"requests waiting 3 → 0"},
		},
		{
			name: "workload deleted",
			f:    oomFinding, in: input(nil, broken),
			summary: "deleted or scaled to zero",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := explainResolution(tt.f, tt.in)
			if !strings.Contains(r.Summary, tt.summary) {
				t.Errorf("summary = %q, want it to contain %q", r.Summary, tt.summary)
			}
			got := strings.Join(r.Changes, "; ")
			for _, w := range tt.wantChanges {
				if !strings.Contains(got, w) {
					t.Errorf("changes = %q, missing %q", got, w)
				}
			}
		})
	}
}

type fakeChanges struct {
	manager string
	at      time.Time
	err     error
}

func (c fakeChanges) LastChange(context.Context, string, string, string) (string, time.Time, error) {
	return c.manager, c.at, c.err
}

func TestEngineResolutionChangedBy(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := Finding{Detector: OOMKilled, Namespace: "llm", WorkloadKind: "Deployment", WorkloadName: "llm-server",
		FirstSeen: now.Add(-10 * time.Minute), LastSeen: now.Add(-2 * time.Minute)}
	in := input([]collector.Snapshot{pod("a")})

	e := &Engine{Log: quiet, Changes: fakeChanges{manager: "kubectl-client-side-apply", at: now.Add(-3 * time.Minute)}}
	if r := e.resolution(context.Background(), f, in); r.ChangedBy != "kubectl apply" || !r.ChangedAt.Equal(now.Add(-3*time.Minute)) {
		t.Errorf("want kubectl apply during the problem, got %+v", r)
	}
	// A change from before the problem started can't be the fix.
	e.Changes = fakeChanges{manager: "helm", at: now.Add(-time.Hour)}
	if r := e.resolution(context.Background(), f, in); r.ChangedBy != "" {
		t.Errorf("old change reported as the fix: %+v", r)
	}
	// Lookup errors are not fatal.
	e.Changes = fakeChanges{err: errors.New("forbidden")}
	if r := e.resolution(context.Background(), f, in); r.Summary == "" || r.ChangedBy != "" {
		t.Errorf("want a resolution without ChangedBy, got %+v", r)
	}
}
