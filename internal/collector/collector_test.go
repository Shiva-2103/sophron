package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/kube"
	"github.com/Shiva-2103/sophron/internal/mapping"
	"github.com/Shiva-2103/sophron/internal/prom"
)

// fakeProm answers queries by matching a substring of the query.
type fakeProm struct {
	answers map[string][]prom.Sample
	fail    map[string]bool
}

func (f *fakeProm) Query(_ context.Context, q string, _ time.Time) ([]prom.Sample, error) {
	for sub := range f.fail {
		if strings.Contains(q, sub) {
			return nil, errors.New("boom")
		}
	}
	for sub, res := range f.answers {
		if strings.Contains(q, sub) {
			return res, nil
		}
	}
	return nil, nil
}

func sample(ns, pod string, v float64) prom.Sample {
	return prom.Sample{Labels: map[string]string{"namespace": ns, "pod": pod}, Value: v}
}

type fakeKube struct {
	pods     map[kube.PodKey]kube.PodInfo
	nodes    map[string]kube.NodeInfo
	warnings map[kube.PodKey]kube.Event
}

func (f *fakeKube) ModelPods() ([]kube.PodInfo, error) {
	var out []kube.PodInfo
	for _, p := range f.pods {
		if p.ServerLabel != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeKube) Pod(k kube.PodKey) (kube.PodInfo, bool) { p, ok := f.pods[k]; return p, ok }
func (f *fakeKube) Node(n string) (kube.NodeInfo, bool)    { x, ok := f.nodes[n]; return x, ok }
func (f *fakeKube) LastWarning(k kube.PodKey) (kube.Event, bool) {
	e, ok := f.warnings[k]
	return e, ok
}

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func newCollector(t *testing.T, p *fakeProm, k *fakeKube) *Collector {
	t.Helper()
	m, err := mapping.Default()
	if err != nil {
		t.Fatal(err)
	}
	return &Collector{
		Prom: p, Kube: k, Mapping: m,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now },
	}
}

func TestCollectJoinsMetricsAndKubernetes(t *testing.T) {
	running := kube.PodKey{Namespace: "llm", Name: "llm-a"}
	pending := kube.PodKey{Namespace: "llm", Name: "llm-b"}
	k := &fakeKube{
		pods: map[kube.PodKey]kube.PodInfo{
			// Discovered through metrics only (no label).
			running: {Key: running, Node: "w1", Phase: "Running", Ready: true, CPULimitCores: 6,
				StartedAt: now.Add(-10 * time.Minute), ReadyAt: now.Add(-8 * time.Minute)},
			// Labelled but Pending: no metrics yet, must still be tracked.
			pending: {Key: pending, Phase: "Pending", ServerLabel: "vllm"},
			// Unrelated pod: must be ignored even if cAdvisor has its metrics.
			{Namespace: "default", Name: "web"}: {Key: kube.PodKey{Namespace: "default", Name: "web"}},
		},
		nodes: map[string]kube.NodeInfo{"w1": {Name: "w1", Provider: "kind", CPUCores: 8}},
		warnings: map[kube.PodKey]kube.Event{
			pending: {Reason: "FailedScheduling", Message: "Insufficient cpu", Time: now.Add(-time.Minute)},
		},
	}
	p := &fakeProm{answers: map[string][]prom.Sample{
		"count by (namespace, pod) (llamacpp:tokens_predicted_total)": {sample("llm", "llm-a", 1)},
		"rate(llamacpp:tokens_predicted_total":                        {sample("llm", "llm-a", 48.1)},
		"container_cpu_usage_seconds_total":                           {sample("llm", "llm-a", 5.85), sample("default", "web", 0.1)},
		"container_cpu_cfs_throttled_periods_total":                   {sample("llm", "llm-a", math.NaN())},
	}}

	snaps, err := newCollector(t, p, k).CollectOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2: %+v", len(snaps), snaps)
	}
	a, b := snaps[0], snaps[1]
	if a.Pod != "llm-a" || a.Server != "llamacpp" || a.NodeProvider != "kind" || a.NodeCPUCores != 8 {
		t.Errorf("running pod snapshot wrong: %+v", a)
	}
	if a.GenerationTokensPerSecond == nil || *a.GenerationTokensPerSecond != 48.1 {
		t.Errorf("tokens/s = %v, want 48.1", a.GenerationTokensPerSecond)
	}
	if a.CPUCores == nil || *a.CPUCores != 5.85 {
		t.Errorf("cpu = %v, want 5.85", a.CPUCores)
	}
	if a.CPUThrottledRatio != nil {
		t.Errorf("NaN throttling should be dropped, got %v", *a.CPUThrottledRatio)
	}
	if a.GPUUtilizationRatio != nil {
		t.Errorf("no GPU data should stay nil, got %v", *a.GPUUtilizationRatio)
	}
	if a.StartupSeconds == nil || *a.StartupSeconds != 120 {
		t.Errorf("startup = %v, want 120", a.StartupSeconds)
	}
	if b.Pod != "llm-b" || b.Server != "vllm" || b.Phase != "Pending" || b.WarningReason != "FailedScheduling" {
		t.Errorf("pending pod snapshot wrong: %+v", b)
	}
}

func TestCollectSurvivesPrometheusOutage(t *testing.T) {
	key := kube.PodKey{Namespace: "llm", Name: "llm-a"}
	k := &fakeKube{pods: map[kube.PodKey]kube.PodInfo{key: {Key: key, Phase: "Running", ServerLabel: "llamacpp"}}}
	p := &fakeProm{fail: map[string]bool{"": true}} // every query fails

	c := newCollector(t, p, k)
	snaps, err := c.CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("a Prometheus outage must not fail the cycle: %v", err)
	}
	if len(snaps) != 1 || snaps[0].CPUCores != nil {
		t.Fatalf("expected one snapshot with no metrics, got %+v", snaps)
	}
	if c.Status().QueryErrors == 0 {
		t.Error("status should report the failed queries")
	}
}

func TestOldWarningsIgnored(t *testing.T) {
	key := kube.PodKey{Namespace: "llm", Name: "llm-a"}
	k := &fakeKube{
		pods:     map[kube.PodKey]kube.PodInfo{key: {Key: key, ServerLabel: "llamacpp"}},
		warnings: map[kube.PodKey]kube.Event{key: {Reason: "BackOff", Time: now.Add(-time.Hour)}},
	}
	snaps, err := newCollector(t, &fakeProm{}, k).CollectOnce(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snaps=%v err=%v", snaps, err)
	}
	if snaps[0].WarningReason != "" {
		t.Errorf("an hour-old warning should be ignored, got %q", snaps[0].WarningReason)
	}
}

func TestNoModelPods(t *testing.T) {
	snaps, err := newCollector(t, &fakeProm{}, &fakeKube{}).CollectOnce(context.Background())
	if err != nil || len(snaps) != 0 {
		t.Fatalf("snaps=%v err=%v", snaps, err)
	}
}
