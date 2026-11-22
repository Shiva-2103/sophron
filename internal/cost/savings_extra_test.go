package cost

import (
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

func TestSpanAndGPUs(t *testing.T) {
	for d, want := range map[time.Duration]string{168 * time.Hour: "7 days", 24 * time.Hour: "24 hours", 90 * time.Minute: "90 minutes"} {
		if got := span(d); got != want {
			t.Errorf("span(%s) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{0: "", 1: ", 1 GPU", 2: ", 2 GPUs"} {
		if got := gpus(n); got != want {
			t.Errorf("gpus(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestNoMemoryCutForOOMKilledWorkload(t *testing.T) {
	np := NodePrice{Hourly: 1, Units: UnitPrices{CPUCoreHour: 0.03, MemoryGiBHour: 0.004}}
	snaps := func(reason string) []collector.Snapshot {
		var g []collector.Snapshot
		for i := range minSamples + 5 {
			used := float64(400 << 20)
			g = append(g, collector.Snapshot{Time: time.Unix(int64(i*60), 0), MemoryRequestBytes: 3 * gib,
				MemoryWorkingSetBytes: &used, LastTerminationReason: reason, Container: "c"})
		}
		return g
	}
	k := workloadKey{ns: "llm", kind: "Deployment", name: "m"}
	for _, tc := range []struct {
		reason string
		want   int
	}{{"", 1}, {"OOMKilled", 0}} {
		g := snaps(tc.reason)
		if got := len(rightSize(k, g, g[len(g)-1], np, 1, 24*time.Hour)); got != tc.want {
			t.Errorf("last exit %q: %d suggestions, want %d", tc.reason, got, tc.want)
		}
	}
}
