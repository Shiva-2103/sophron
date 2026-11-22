package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

// pod builds a healthy, Ready llama.cpp pod of the llm/llm-server Deployment.
func pod(name string, mods ...func(*collector.Snapshot)) collector.Snapshot {
	s := collector.Snapshot{
		Time: now, Namespace: "llm", Pod: name, Server: "llamacpp",
		WorkloadKind: "Deployment", WorkloadName: "llm-server", Revision: "aaa",
		Phase: "Running", Ready: true, Node: "worker-1",
		CPURequestCores: 4, CPULimitCores: 6, MemoryLimitBytes: 6 << 30,
		CreatedAt: now.Add(-time.Hour), StartedAt: now.Add(-time.Hour), ReadyAt: now.Add(-59 * time.Minute),
	}
	for _, m := range mods {
		m(&s)
	}
	return s
}

// input builds an Input from the latest snapshots plus optional extra history.
func input(latest []collector.Snapshot, history ...collector.Snapshot) Input {
	in := Input{Now: now, Latest: latest, History: map[PodKey][]collector.Snapshot{}, Config: DefaultConfig()}
	for _, h := range append(history, latest...) {
		k := PodKey{h.Namespace, h.Pod}
		in.History[k] = append(in.History[k], h)
	}
	return in
}

func at(ago time.Duration, s collector.Snapshot, mods ...func(*collector.Snapshot)) collector.Snapshot {
	s.Time = now.Add(-ago)
	for _, m := range mods {
		m(&s)
	}
	return s
}

func unschedulable(msg string, created time.Duration) func(*collector.Snapshot) {
	return func(s *collector.Snapshot) {
		s.Phase, s.Ready, s.Node = "Pending", false, ""
		s.StartedAt, s.ReadyAt = time.Time{}, time.Time{}
		s.CreatedAt = now.Add(-created)
		s.WarningReason, s.WarningMessage = "FailedScheduling", msg
	}
}

const gpuMsg = "0/3 nodes are available: 1 Insufficient nvidia.com/gpu, 2 node(s) didn't match Pod's node affinity/selector."
const cpuMsg = "0/3 nodes are available: 1 Insufficient cpu, 2 node(s) didn't match Pod's node affinity/selector."

func one(t *testing.T, got []Finding) Finding {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
	return got[0]
}

func none(t *testing.T, got []Finding) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("want no findings, got %+v", got)
	}
}

func TestPending(t *testing.T) {
	t.Run("gpu request, nothing else serving", func(t *testing.T) {
		got := one(t, detectPending(input([]collector.Snapshot{pod("p1", unschedulable(gpuMsg, 3*time.Minute))})))
		if got.Severity != Critical || !strings.Contains(got.Title, "needs a GPU") {
			t.Errorf("got %s %q", got.Severity, got.Title)
		}
		if got.Key != "pending-unschedulable/llm/llm-server" {
			t.Errorf("key = %s", got.Key)
		}
	})
	t.Run("cpu request suggests lower request", func(t *testing.T) {
		got := one(t, detectPending(input([]collector.Snapshot{pod("p1", unschedulable(cpuMsg, 3*time.Minute))})))
		if !strings.Contains(got.Fix.Snippet, "--requests=cpu=2") {
			t.Errorf("snippet = %q", got.Fix.Snippet)
		}
	})
	t.Run("too new", func(t *testing.T) {
		none(t, detectPending(input([]collector.Snapshot{pod("p1", unschedulable(gpuMsg, time.Minute))})))
	})
	t.Run("another replica serves: warning", func(t *testing.T) {
		got := one(t, detectPending(input([]collector.Snapshot{pod("p0"), pod("p1", unschedulable(cpuMsg, 3*time.Minute))})))
		if got.Severity != Warning {
			t.Errorf("severity = %s", got.Severity)
		}
	})
	t.Run("new revision during rollout belongs to rollout-stuck", func(t *testing.T) {
		newPod := pod("p1", unschedulable(cpuMsg, 5*time.Minute), func(s *collector.Snapshot) { s.Revision = "bbb" })
		none(t, detectPending(input([]collector.Snapshot{pod("p0"), newPod})))
	})
}

func TestRolloutStuck(t *testing.T) {
	newPod := func(created time.Duration) collector.Snapshot {
		return pod("p1", unschedulable(cpuMsg, created), func(s *collector.Snapshot) { s.Revision = "bbb" })
	}
	t.Run("new version can't fit next to old", func(t *testing.T) {
		got := one(t, detectRolloutStuck(input([]collector.Snapshot{pod("p0"), newPod(4 * time.Minute)})))
		if !strings.Contains(got.Fix.Snippet, "maxSurge: 0") || !strings.Contains(got.Title, "can't be scheduled") {
			t.Errorf("got %q / %q", got.Title, got.Fix.Snippet)
		}
	})
	t.Run("too new", func(t *testing.T) {
		none(t, detectRolloutStuck(input([]collector.Snapshot{pod("p0"), newPod(time.Minute)})))
	})
	t.Run("same revision is scaling, not a rollout", func(t *testing.T) {
		p := pod("p1", unschedulable(cpuMsg, 5*time.Minute))
		none(t, detectRolloutStuck(input([]collector.Snapshot{pod("p0"), p})))
	})
	t.Run("no old version serving", func(t *testing.T) {
		none(t, detectRolloutStuck(input([]collector.Snapshot{newPod(5 * time.Minute)})))
	})
}

func oomPod(name string) collector.Snapshot {
	return pod(name, func(s *collector.Snapshot) {
		s.Ready, s.Restarts, s.WaitingReason, s.LastTerminationReason = false, 3, "CrashLoopBackOff", "OOMKilled"
		s.MemoryLimitBytes = 512 << 20
	})
}

func TestOOM(t *testing.T) {
	t.Run("recent OOM loop", func(t *testing.T) {
		latest := oomPod("p1")
		earlier := at(5*time.Minute, latest, func(s *collector.Snapshot) { s.Restarts = 1; s.MemoryWorkingSetBytes = f(500 << 20) })
		got := one(t, detectOOM(input([]collector.Snapshot{latest}, earlier)))
		if got.Severity != Critical {
			t.Errorf("severity = %s", got.Severity)
		}
		// max(512Mi*1.5, 500Mi*1.3) = 768Mi
		if !strings.Contains(got.Fix.Snippet, "memory: 768Mi") {
			t.Errorf("snippet = %q", got.Fix.Snippet)
		}
		if !strings.Contains(strings.Join(got.Fix.Steps, " "), "--ctx-size") {
			t.Error("llama.cpp advice should mention --ctx-size")
		}
	})
	t.Run("old OOM, healthy since", func(t *testing.T) {
		p := pod("p1", func(s *collector.Snapshot) { s.Restarts, s.LastTerminationReason = 1, "OOMKilled" })
		none(t, detectOOM(input([]collector.Snapshot{p}, at(10*time.Minute, p))))
	})
}

func TestContainerFailing(t *testing.T) {
	crash := pod("p1", func(s *collector.Snapshot) {
		s.Ready, s.Restarts, s.WaitingReason, s.LastTerminationReason = false, 4, "CrashLoopBackOff", "Error"
	})
	got := one(t, detectContainerFailing(input([]collector.Snapshot{crash}, at(5*time.Minute, crash, func(s *collector.Snapshot) { s.Restarts = 1 }))))
	if !strings.Contains(got.Title, "keeps crashing (3 restarts") || !strings.Contains(got.Fix.Snippet, "--previous") {
		t.Errorf("got %q / %q", got.Title, got.Fix.Snippet)
	}

	pull := pod("p1", func(s *collector.Snapshot) { s.Ready, s.Phase, s.WaitingReason = false, "Pending", "ImagePullBackOff" })
	if got := one(t, detectContainerFailing(input([]collector.Snapshot{pull}))); !strings.Contains(got.Title, "can't pull") {
		t.Errorf("title = %q", got.Title)
	}

	// Found on the lab: a brand-new pod that crashed before our first
	// snapshot of it must not say "0 restarts".
	young := pod("p1", func(s *collector.Snapshot) {
		s.Ready, s.Restarts, s.WaitingReason, s.LastTerminationReason = false, 2, "CrashLoopBackOff", "Error"
		s.CreatedAt = now.Add(-40 * time.Second)
	})
	if got := one(t, detectContainerFailing(input([]collector.Snapshot{young}))); !strings.Contains(got.Title, "(2 restarts in 15m)") {
		t.Errorf("young pod title = %q", got.Title)
	}

	none(t, detectContainerFailing(input([]collector.Snapshot{oomPod("p1")})))
	none(t, detectContainerFailing(input([]collector.Snapshot{pod("p1")})))
}

func TestSlowStart(t *testing.T) {
	starting := pod("p1", func(s *collector.Snapshot) {
		s.Phase, s.Ready, s.WaitingReason = "Pending", false, "PodInitializing"
		s.CreatedAt, s.StartedAt, s.ReadyAt = now.Add(-3*time.Minute), now.Add(-3*time.Minute), time.Time{}
	})
	if got := one(t, detectSlowStart(input([]collector.Snapshot{starting}))); got.Severity != Warning || !strings.Contains(got.Title, "still starting after 3m") {
		t.Errorf("got %s %q", got.Severity, got.Title)
	}

	finished := pod("p1", func(s *collector.Snapshot) {
		s.StartupSeconds, s.ReadyAt = f(155), now.Add(-5*time.Minute)
	})
	if got := one(t, detectSlowStart(input([]collector.Snapshot{finished}))); got.Severity != Info || !strings.Contains(got.Title, "took 2m35s") {
		t.Errorf("got %s %q", got.Severity, got.Title)
	}

	longAgo := pod("p1", func(s *collector.Snapshot) { s.StartupSeconds, s.ReadyAt = f(155), now.Add(-40*time.Minute) })
	none(t, detectSlowStart(input([]collector.Snapshot{longAgo})))

	fast := pod("p1", func(s *collector.Snapshot) { s.StartupSeconds, s.ReadyAt = f(20), now.Add(-time.Minute) })
	none(t, detectSlowStart(input([]collector.Snapshot{fast})))

	crashing := starting
	crashing.Restarts = 2
	none(t, detectSlowStart(input([]collector.Snapshot{crashing})))
}

func TestQueueBacklog(t *testing.T) {
	busy := func(cpu float64) func(*collector.Snapshot) {
		return func(s *collector.Snapshot) { s.RequestsWaiting, s.RequestsRunning, s.CPUCores = f(3), f(1), f(cpu) }
	}
	latest := pod("p1", busy(2))
	hist := []collector.Snapshot{at(time.Minute, latest), at(30*time.Second, latest)}

	got := one(t, detectQueueBacklog(input([]collector.Snapshot{latest}, hist...)))
	if !strings.Contains(strings.Join(got.Fix.Steps, " "), "Raise --parallel") {
		t.Errorf("steps = %v", got.Fix.Steps)
	}

	hot := pod("p1", busy(5.9))
	got = one(t, detectQueueBacklog(input([]collector.Snapshot{hot}, at(time.Minute, hot), at(30*time.Second, hot))))
	if !strings.Contains(got.Fix.Summary, "Add capacity") || !strings.Contains(strings.Join(got.Evidence, " "), "CPU is at its limit") {
		t.Errorf("saturated: %q %v", got.Fix.Summary, got.Evidence)
	}

	// Only 2 snapshots in a row: a short burst, not a backlog.
	none(t, detectQueueBacklog(input([]collector.Snapshot{latest}, at(30*time.Second, latest))))
	// The streak was broken.
	idle := at(time.Minute, latest, func(s *collector.Snapshot) { s.RequestsWaiting = f(0) })
	none(t, detectQueueBacklog(input([]collector.Snapshot{latest}, at(90*time.Second, latest), idle, at(30*time.Second, latest))))
}

func TestHelpers(t *testing.T) {
	for in, want := range map[float64]string{512 << 20: "512Mi", 768 << 20: "768Mi", 1 << 30: "1Gi", 1100 << 20: "1280Mi", 3 << 30: "3Gi"} {
		if got := memQuantity(in); got != want {
			t.Errorf("memQuantity(%v) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{45 * time.Second: "45s", 3 * time.Minute: "3m", 155 * time.Second: "2m35s", 65 * time.Minute: "1h5m"} {
		if got := humanDur(in); got != want {
			t.Errorf("humanDur(%v) = %s, want %s", in, got, want)
		}
	}
	if len(All()) != 6 {
		t.Errorf("want 6 detectors, got %d", len(All()))
	}
}
