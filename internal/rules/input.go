package rules

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

// Config holds detector thresholds. Defaults suit both the lab and small
// production clusters; every value can be changed with a flag.
type Config struct {
	// A pod that can't be scheduled for this long is reported.
	PendingAfter time.Duration
	// A new version that isn't Ready for this long, while the old one
	// still serves, is a stuck rollout.
	RolloutStuckAfter time.Duration
	// Starting (created to Ready) longer than this is a slow cold start.
	SlowStartAfter time.Duration
	// Report a finished slow start for this long after the pod became Ready.
	SlowStartRecent time.Duration
	// Restarts counted over this window for crash detection.
	RestartWindow time.Duration
	// This many restarts in RestartWindow means the container keeps crashing.
	RestartThreshold int32
	// An OOMKilled container counts if it restarted within this window.
	OOMWindow time.Duration
	// Requests waiting in this many consecutive snapshots is a backlog.
	QueueCycles int
}

// DefaultConfig returns the default thresholds.
func DefaultConfig() Config {
	return Config{
		PendingAfter:      2 * time.Minute,
		RolloutStuckAfter: 3 * time.Minute,
		SlowStartAfter:    2 * time.Minute,
		SlowStartRecent:   30 * time.Minute,
		RestartWindow:     15 * time.Minute,
		RestartThreshold:  2,
		OOMWindow:         30 * time.Minute,
		QueueCycles:       3,
	}
}

// HistoryWindow is how much history detectors need.
func (c Config) HistoryWindow() time.Duration {
	w := 15 * time.Minute
	for _, d := range []time.Duration{c.RestartWindow, c.OOMWindow, c.SlowStartRecent} {
		w = max(w, d)
	}
	return w + 5*time.Minute
}

// PodKey identifies a pod.
type PodKey struct{ Namespace, Pod string }

// Input is what every detector sees: this cycle's snapshot of each model pod,
// plus each pod's recent history.
type Input struct {
	Now     time.Time
	Latest  []collector.Snapshot
	History map[PodKey][]collector.Snapshot // oldest first, includes Latest
	Config  Config
}

// HistoryOf returns a pod's recent snapshots, oldest first.
func (in Input) HistoryOf(s collector.Snapshot) []collector.Snapshot {
	if h := in.History[PodKey{s.Namespace, s.Pod}]; len(h) > 0 {
		return h
	}
	return []collector.Snapshot{s}
}

// WorkloadPods returns this cycle's snapshots of every pod in the same
// workload (for example all pods of one Deployment), including s.
func (in Input) WorkloadPods(s collector.Snapshot) []collector.Snapshot {
	if s.WorkloadName == "" {
		return []collector.Snapshot{s}
	}
	var out []collector.Snapshot
	for _, o := range in.Latest {
		if o.Namespace == s.Namespace && o.WorkloadKind == s.WorkloadKind && o.WorkloadName == s.WorkloadName {
			out = append(out, o)
		}
	}
	return out
}

// newFinding fills in the fields every finding shares.
func newFinding(detector string, s collector.Snapshot, sev Severity, title, summary string, evidence []string, fix Fix) Finding {
	f := Finding{
		Detector: detector, Severity: sev,
		Namespace: s.Namespace, Pod: s.Pod, WorkloadKind: s.WorkloadKind, WorkloadName: s.WorkloadName,
		Title: title, Summary: summary, Evidence: evidence, Fix: fix,
	}
	f.Key = f.MakeKey()
	return f
}

// name is how findings refer to a workload or pod: "llm/llm-server".
func name(s collector.Snapshot) string {
	if s.WorkloadName != "" {
		return s.Namespace + "/" + s.WorkloadName
	}
	return s.Namespace + "/" + s.Pod
}

// kubectlTarget is the workload as kubectl writes it: "deployment/llm-server".
func kubectlTarget(s collector.Snapshot) string {
	if s.WorkloadName != "" && s.WorkloadKind != "" {
		return strings.ToLower(s.WorkloadKind) + "/" + s.WorkloadName
	}
	return "pod/" + s.Pod
}

// age is how long ago t was, or 0 when t is unset.
func age(now, t time.Time) time.Duration {
	if t.IsZero() || t.After(now) {
		return 0
	}
	return now.Sub(t)
}

// restartsWithin counts container restarts of one pod inside the window.
func restartsWithin(hist []collector.Snapshot, now time.Time, window time.Duration) int32 {
	if len(hist) == 0 {
		return 0
	}
	last := hist[len(hist)-1].Restarts
	low := last
	for _, h := range hist {
		if now.Sub(h.Time) <= window && h.Restarts < low {
			low = h.Restarts
		}
	}
	return last - low
}

// trailing counts how many of the most recent snapshots match, newest first,
// stopping at the first that doesn't.
func trailing(hist []collector.Snapshot, match func(collector.Snapshot) bool) int {
	n := 0
	for i := len(hist) - 1; i >= 0 && match(hist[i]); i-- {
		n++
	}
	return n
}

// peak returns the highest value of a metric in the history, if any.
func peak(hist []collector.Snapshot, get func(collector.Snapshot) *float64) (float64, bool) {
	best, ok := 0.0, false
	for _, h := range hist {
		if v := get(h); v != nil && (!ok || *v > best) {
			best, ok = *v, true
		}
	}
	return best, ok
}

func humanDur(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0 && s > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func humanBytes(b float64) string {
	const mi, gi = 1 << 20, 1 << 30
	if b >= gi {
		return fmt.Sprintf("%.1f GiB", b/gi)
	}
	return fmt.Sprintf("%.0f MiB", b/mi)
}

// memQuantity rounds bytes up to a tidy Kubernetes quantity (256Mi steps).
func memQuantity(b float64) string {
	const step = 256 << 20
	n := int64(math.Ceil(b/step)) * step
	if n%(1<<30) == 0 {
		return fmt.Sprintf("%dGi", n>>30)
	}
	return fmt.Sprintf("%dMi", n>>20)
}

func cores(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func secondsToDur(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
