package rules

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

// Resolution says how a finding went away: what changed in the workload,
// and which tool changed it.
type Resolution struct {
	// One line: "Fixed by a change to the workload." or "Cleared without a change: ...".
	Summary string `json:"summary"`
	// What is different now, e.g. "memory limit 512Mi → 6Gi".
	Changes []string `json:"changes,omitempty"`
	// The tool that last changed the workload, from Kubernetes managedFields
	// (e.g. "kubectl apply", "helm", "argocd-controller"). Kubernetes records
	// the tool, not the person; the person is in the audit log.
	ChangedBy string    `json:"changedBy,omitempty"`
	ChangedAt time.Time `json:"changedAt,omitzero"`
	// Who approved the fix, when Sophron applies fixes itself (later).
	ApprovedBy string `json:"approvedBy,omitempty"`
	// While the finding was open, how long no pod of the workload was Ready,
	// and what the workload cost during that time (USD). Its price was paid
	// for nothing.
	DownSeconds float64  `json:"downSeconds,omitempty"`
	DownCost    *float64 `json:"downCost,omitempty"`
}

// downtime adds up the cycles, between first and last seen, in which the
// workload had pods but none of them Ready, and what they cost meanwhile.
func downtime(f Finding, snaps []collector.Snapshot) (float64, *float64) {
	type cycle struct {
		pods, ready int
		cost        float64
		priced      bool
	}
	cycles := map[time.Time]*cycle{}
	for _, s := range snaps {
		if !sameWorkload(f, s) || s.Time.Before(f.FirstSeen) || s.Time.After(f.LastSeen) {
			continue
		}
		c := cycles[s.Time]
		if c == nil {
			c = &cycle{}
			cycles[s.Time] = c
		}
		c.pods++
		if s.Ready {
			c.ready++
		}
		if s.CostPerHour != nil {
			c.cost += *s.CostPerHour
			c.priced = true
		}
	}
	times := make([]time.Time, 0, len(cycles))
	for t := range cycles {
		times = append(times, t)
	}
	sort.Slice(times, func(a, b int) bool { return times[a].Before(times[b]) })
	var secs, cost float64
	priced := false
	for i, t := range times {
		c := cycles[t]
		if c.ready > 0 || i+1 == len(times) {
			continue
		}
		gap := min(times[i+1].Sub(t), 2*time.Minute).Seconds()
		secs += gap
		if c.priced {
			cost += c.cost * gap / 3600
			priced = true
		}
	}
	if !priced {
		return secs, nil
	}
	return secs, &cost
}

// Approvals tells who approved the fix Sophron applied for a finding.
type Approvals interface {
	ApprovedFor(ctx context.Context, findingID int64) (by string, at time.Time, ok bool)
}

// ChangeFinder looks up who last changed a workload's spec. Optional: without
// it, resolutions still say what changed, just not with which tool.
type ChangeFinder interface {
	LastChange(ctx context.Context, kind, namespace, name string) (manager string, at time.Time, err error)
}

// explainResolution compares the workload when the problem was last seen
// with how it is now.
func explainResolution(f Finding, in Input) Resolution {
	before, hasBefore := lastBefore(f, in)
	now := workloadNow(f, in.Latest)

	if len(now) == 0 {
		return Resolution{Summary: "The workload has no pods now: it was deleted or scaled to zero."}
	}
	cur := now[0]
	for _, p := range now {
		if p.Ready {
			cur = p
			break
		}
	}
	ready := 0
	for _, p := range now {
		if p.Ready {
			ready++
		}
	}

	var spec []string
	if hasBefore {
		if before.Revision != "" && cur.Revision != "" && before.Revision != cur.Revision {
			spec = append(spec, fmt.Sprintf("new version rolled out (revision %s → %s)", before.Revision, cur.Revision))
		}
		if d := quantityChange("memory limit", before.MemoryLimitBytes, cur.MemoryLimitBytes, quantity); d != "" {
			spec = append(spec, d)
		}
		if d := quantityChange("CPU request", before.CPURequestCores, cur.CPURequestCores, func(v float64) string { return cores(v) }); d != "" {
			spec = append(spec, d)
		}
		if d := quantityChange("CPU limit", before.CPULimitCores, cur.CPULimitCores, func(v float64) string { return cores(v) }); d != "" {
			spec = append(spec, d)
		}
		if before.GPURequest != cur.GPURequest {
			spec = append(spec, fmt.Sprintf("GPU request %d → %d", before.GPURequest, cur.GPURequest))
		}
	}

	changes := append([]string{}, spec...)
	changes = append(changes, fmt.Sprintf("%d of %d pods Ready now", ready, len(now)))
	if f.Detector == QueueBacklog && hasBefore && before.RequestsWaiting != nil {
		w := 0.0
		if cur.RequestsWaiting != nil {
			w = *cur.RequestsWaiting
		}
		changes = append(changes, fmt.Sprintf("requests waiting %.0f → %.0f", *before.RequestsWaiting, w))
	}

	r := Resolution{Changes: changes}
	if len(spec) > 0 {
		r.Summary = "Fixed by a change to the workload."
	} else {
		r.Summary = "Cleared without a change to the workload: " + clearedReason(f.Detector)
	}
	return r
}

func clearedReason(detector string) string {
	switch detector {
	case QueueBacklog:
		return "the queue drained (less traffic, or requests finished)."
	case PendingUnschedulable:
		return "the pod got a node (capacity freed up or a node was added)."
	case SlowColdStart:
		return "the pod is Ready and the slow start is in the past."
	case OOMKilled, ContainerFailing:
		return "the container stopped crashing."
	case RolloutStuck:
		return "the rollout finished."
	}
	return "the problem is no longer detected."
}

// lastBefore is the workload's snapshot from when the problem was last seen.
func lastBefore(f Finding, in Input) (collector.Snapshot, bool) {
	var best collector.Snapshot
	found := false
	for _, hist := range in.History {
		for _, h := range hist {
			if !sameWorkload(f, h) || h.Time.After(f.LastSeen) {
				continue
			}
			// Prefer the pod the finding was about, then the newest.
			better := !found || h.Time.After(best.Time) ||
				(h.Time.Equal(best.Time) && h.Pod == f.Pod)
			if better {
				best, found = h, true
			}
		}
	}
	return best, found
}

func workloadNow(f Finding, latest []collector.Snapshot) []collector.Snapshot {
	var out []collector.Snapshot
	for _, s := range latest {
		if sameWorkload(f, s) {
			out = append(out, s)
		}
	}
	return out
}

func sameWorkload(f Finding, s collector.Snapshot) bool {
	if s.Namespace != f.Namespace {
		return false
	}
	if f.WorkloadName != "" {
		return s.WorkloadKind == f.WorkloadKind && s.WorkloadName == f.WorkloadName
	}
	return s.Pod == f.Pod
}

func quantityChange(what string, before, now float64, format func(float64) string) string {
	if math.Abs(before-now) < 1e-9 {
		return ""
	}
	b, n := "none", "none"
	if before > 0 {
		b = format(before)
	}
	if now > 0 {
		n = format(now)
	}
	return fmt.Sprintf("%s %s → %s", what, b, n)
}

// quantity prints bytes the way Kubernetes manifests do: 512Mi, 6Gi.
func quantity(b float64) string {
	const mi, gi = 1 << 20, 1 << 30
	if b >= gi && math.Mod(b, gi) == 0 {
		return fmt.Sprintf("%.0fGi", b/gi)
	}
	return fmt.Sprintf("%.0fMi", b/mi) // 1280Mi, not 1.2Gi
}

// FriendlyManager turns a field manager name into what a person typed.
func FriendlyManager(m string) string {
	switch m {
	case "kubectl-client-side-apply", "kubectl":
		return "kubectl apply"
	case "kubectl-patch", "kubectl-set", "kubectl-edit", "kubectl-rollout", "kubectl-scale", "kubectl-label", "kubectl-annotate":
		return strings.Replace(m, "-", " ", 1)
	}
	return m
}
