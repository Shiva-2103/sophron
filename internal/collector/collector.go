// Package collector builds snapshots: it finds model server pods, asks
// Prometheus for their numbers, joins them with Kubernetes facts, and saves
// one snapshot per pod on every cycle.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/Shiva-2103/sophron/internal/kube"
	"github.com/Shiva-2103/sophron/internal/mapping"
	"github.com/Shiva-2103/sophron/internal/prom"
)

// Saver stores snapshots. The SQLite store implements it.
type Saver interface {
	Save(ctx context.Context, snaps []Snapshot) error
}

// Collector runs the collection cycle.
type Collector struct {
	Prom    prom.Querier
	Kube    kube.Cache
	Mapping *mapping.Mapping
	Store   Saver
	Log     *slog.Logger
	// Ignore warning events older than this.
	WarningWindow time.Duration
	// Now is replaceable in tests.
	Now func() time.Time
	// Pricer fills in each snapshot's cost before it is saved (optional).
	Pricer Pricer
	// AfterCycle runs after every successful cycle with the snapshots it
	// saved (possibly none). The rules engine hooks in here.
	AfterCycle func(ctx context.Context, snaps []Snapshot)

	mu              sync.Mutex
	lastRun         time.Time
	lastErr         error
	lastN           int
	lastQueryErrors int
}

// Pricer adds cost to snapshots. The cost engine implements it.
type Pricer interface {
	Price(ctx context.Context, snaps []Snapshot)
}

// Status describes the last collection cycle.
type Status struct {
	LastRun     time.Time `json:"lastRun"`
	Pods        int       `json:"pods"`
	Error       string    `json:"error,omitempty"`
	QueryErrors int       `json:"queryErrors"`
}

// Status reports the last cycle, for the API.
func (c *Collector) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{LastRun: c.lastRun, Pods: c.lastN, QueryErrors: c.lastQueryErrors}
	if c.lastErr != nil {
		st.Error = c.lastErr.Error()
	}
	return st
}

// Run collects once, then every interval, until ctx is cancelled.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		start := time.Now()
		snaps, err := c.CollectOnce(ctx)
		if err == nil && len(snaps) > 0 {
			err = c.Store.Save(ctx, snaps)
		}
		c.mu.Lock()
		c.lastRun, c.lastErr, c.lastN = time.Now(), err, len(snaps)
		c.mu.Unlock()
		if err != nil {
			c.Log.Error("collection cycle failed", "error", err)
		} else {
			c.Log.Info("collection cycle done", "pods", len(snaps), "took", time.Since(start).Round(time.Millisecond))
			if c.AfterCycle != nil {
				c.AfterCycle(ctx, snaps)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type podMetrics map[string]float64

// CollectOnce builds one snapshot for every model pod.
func (c *Collector) CollectOnce(ctx context.Context) ([]Snapshot, error) {
	now := c.now()

	// 1. Discover model pods in two ways, so we miss neither:
	//    a) pods that expose a known model-server metric (no setup needed)
	//    b) pods labelled sophron.dev/model-server (works even while Pending,
	//       when there are no metrics yet)
	servers := map[kube.PodKey]string{}
	var errs []error
	for _, s := range c.Mapping.Servers {
		res, err := c.Prom.Query(ctx, fmt.Sprintf("count by (namespace, pod) (%s)", s.Detect), now)
		if err != nil {
			// Not fatal: labelled pods are still found through Kubernetes.
			errs = append(errs, fmt.Errorf("discover %s pods: %w", s.Name, err))
			continue
		}
		for _, r := range res {
			k := key(r.Labels)
			if k.Name == "" {
				continue
			}
			if _, seen := servers[k]; !seen {
				servers[k] = s.Name
			}
		}
	}
	labelled, err := c.Kube.ModelPods()
	if err != nil {
		return nil, fmt.Errorf("list labelled pods: %w", err)
	}
	for _, p := range labelled {
		if _, seen := servers[p.Key]; !seen {
			servers[p.Key] = p.ServerLabel
		}
	}
	if len(servers) == 0 {
		c.mu.Lock()
		c.lastQueryErrors = len(errs)
		c.mu.Unlock()
		return nil, nil
	}

	// 2. Run every query once for the whole cluster, then split by pod.
	//    That is a fixed number of queries, however many pods there are.
	metrics := map[kube.PodKey]podMetrics{}
	run := func(name, q string, onlyServer string) {
		res, err := c.Prom.Query(ctx, q, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return
		}
		for _, r := range res {
			k := key(r.Labels)
			srv, tracked := servers[k]
			if !tracked || (onlyServer != "" && srv != onlyServer) {
				continue
			}
			if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
				continue // e.g. 0/0 in the throttling ratio when the pod is idle
			}
			if metrics[k] == nil {
				metrics[k] = podMetrics{}
			}
			metrics[k][name] = r.Value
		}
	}
	for _, s := range c.Mapping.Servers {
		for _, name := range sortedKeys(s.Queries) {
			run(name, s.Queries[name], s.Name)
		}
	}
	for _, name := range sortedKeys(c.Mapping.Common) {
		run(name, c.Mapping.Common[name], "")
	}
	// A failing query is logged, not fatal: one broken metric must not stop
	// every snapshot. The values are simply missing.
	if len(errs) > 0 {
		c.Log.Warn("some Prometheus queries failed; their values are missing", "failed", len(errs), "error", errors.Join(errs...))
	}
	c.mu.Lock()
	c.lastQueryErrors = len(errs)
	c.mu.Unlock()

	// 3. Join with Kubernetes facts.
	keys := make([]kube.PodKey, 0, len(servers))
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a].String() < keys[b].String() })

	snaps := make([]Snapshot, 0, len(keys))
	for _, k := range keys {
		p, ok := c.Kube.Pod(k)
		if !ok {
			continue // metrics from a pod that no longer exists
		}
		snaps = append(snaps, c.build(now, servers[k], p, metrics[k]))
	}
	if c.Pricer != nil {
		c.Pricer.Price(ctx, snaps)
	}
	return snaps, nil
}

func (c *Collector) build(now time.Time, server string, p kube.PodInfo, m podMetrics) Snapshot {
	s := Snapshot{
		Time: now, Namespace: p.Key.Namespace, Pod: p.Key.Name, Server: server,
		WorkloadKind: p.WorkloadKind, WorkloadName: p.WorkloadName, Revision: p.Revision, Container: p.Container,
		CreatedAt: p.CreatedAt, StartedAt: p.StartedAt, ReadyAt: p.ReadyAt,
		Phase: p.Phase, Ready: p.Ready, Restarts: p.Restarts,
		WaitingReason: p.WaitingReason, LastTerminationReason: p.LastTerminationReason,
		CPURequestCores: p.CPURequestCores, CPULimitCores: p.CPULimitCores,
		MemoryLimitBytes: p.MemoryLimitBytes, MemoryRequestBytes: p.MemoryRequestBytes, GPURequest: p.GPURequest,
		Node: p.Node,
	}
	if !p.StartedAt.IsZero() && !p.ReadyAt.IsZero() && p.ReadyAt.After(p.StartedAt) {
		v := p.ReadyAt.Sub(p.StartedAt).Seconds()
		s.StartupSeconds = &v
	}
	if ev, ok := c.Kube.LastWarning(p.Key); ok && now.Sub(ev.Time) <= c.warningWindow() {
		s.WarningReason, s.WarningMessage = ev.Reason, ev.Message
	}
	if n, ok := c.Kube.Node(p.Node); ok {
		s.NodeProvider, s.NodeInstanceType, s.NodeZone, s.NodeRegion = n.Provider, n.InstanceType, n.Zone, n.Region
		s.NodeCapacityType, s.NodeCPUCores, s.NodeMemoryBytes, s.NodeGPUs = n.CapacityType, n.CPUCores, n.MemoryBytes, n.GPUs
	}
	get := func(name string) *float64 {
		if v, ok := m[name]; ok {
			return &v
		}
		return nil
	}
	s.GenerationTokensPerSecond = get(mapping.GenerationTokensPerSecond)
	s.PromptTokensPerSecond = get(mapping.PromptTokensPerSecond)
	s.RequestsRunning = get(mapping.RequestsRunning)
	s.RequestsWaiting = get(mapping.RequestsWaiting)
	s.KVCacheUsageRatio = get(mapping.KVCacheUsageRatio)
	s.CPUCores = get(mapping.CPUCores)
	s.CPUThrottledRatio = get(mapping.CPUThrottledRatio)
	s.MemoryWorkingSetBytes = get(mapping.MemoryWorkingSetBytes)
	s.GPUUtilizationRatio = get(mapping.GPUUtilizationRatio)
	s.GPUMemoryUsedBytes = get(mapping.GPUMemoryUsedBytes)
	return s
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

func (c *Collector) warningWindow() time.Duration {
	if c.WarningWindow > 0 {
		return c.WarningWindow
	}
	return 15 * time.Minute
}

func key(l map[string]string) kube.PodKey {
	return kube.PodKey{Namespace: l["namespace"], Name: l["pod"]}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
