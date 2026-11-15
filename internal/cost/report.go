package cost

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

// busyTokens is the output rate above which a model counts as busy.
const busyTokens = 0.5

// HoursPerMonth is the average month (365 × 24 / 12), as cloud bills use.
const HoursPerMonth = 730

// maxGap caps how long one snapshot counts for, so a collector outage
// isn't billed as one long snapshot.
const maxGap = 2 * time.Minute

// SnapshotReader is what reports need from the database.
type SnapshotReader interface {
	Since(ctx context.Context, since time.Time) ([]collector.Snapshot, error)
}

// Peak is a workload's highest output throughput in one cycle.
type Peak struct {
	Namespace, Kind, Name string
	Max                   float64 // output tokens/s, all pods together
	Cycles                int     // cycles with throughput data
}

// PeakReader is optional: a store that computes peaks itself saves the
// reporter from loading days of snapshots.
type PeakReader interface {
	OutputPeaks(ctx context.Context, since time.Time) ([]Peak, error)
}

// Reporter builds the cost reports the API serves.
type Reporter struct {
	Store  SnapshotReader
	Pricer *Pricer
	File   *PriceFile
	// How far back to look for a model's peak throughput.
	PeakWindow time.Duration
	Now        func() time.Time
}

// ModelCost is one model (workload): what it costs and what it delivers.
type ModelCost struct {
	Namespace    string `json:"namespace"`
	WorkloadKind string `json:"workloadKind,omitempty"`
	Workload     string `json:"workload"`
	Server       string `json:"server"`
	Pods         int    `json:"pods"`
	ReadyPods    int    `json:"readyPods"`

	// Now: the latest cycle.
	CostPerHour   *float64 `json:"costPerHour"`
	MonthForecast *float64 `json:"monthForecast"`
	PriceSources  []string `json:"priceSources"`
	Estimate      bool     `json:"estimate"`

	// Over the report window.
	Window                string   `json:"window"`
	Cost                  float64  `json:"cost"` // USD spent in the window
	OutputTokens          float64  `json:"outputTokens"`
	InputTokens           float64  `json:"inputTokens"`
	CostPer1MOutputTokens *float64 `json:"costPer1MOutputTokens"`
	CostPer1MTokens       *float64 `json:"costPer1MTokens"` // input + output
	// Only the time the model was producing tokens: what a token costs when
	// busy. The gap to CostPer1MOutputTokens is the price of idle time.
	CostPer1MOutputTokensBusy *float64 `json:"costPer1MOutputTokensBusy"`
	BusyShare                 *float64 `json:"busyShare"` // share of the window with traffic
	OutputTokensPerSecond     *float64 `json:"outputTokensPerSecond"`
	PeakOutputTokensPerSec    *float64 `json:"peakOutputTokensPerSecond"`
	// Share of the highest throughput seen recently: low = paying for idle
	// capacity, near 1 = add capacity soon.
	CapacityUsed *float64 `json:"capacityUsed"`

	APIComparison []APIComparison `json:"apiComparison,omitempty"`
	Notes         []string        `json:"notes,omitempty"`
}

// APIComparison compares a month of self-hosting with a hosted API.
type APIComparison struct {
	API string `json:"api"`
	// What this month's tokens would cost on the API.
	APICostPerMonth  float64 `json:"apiCostPerMonth"`
	SelfCostPerMonth float64 `json:"selfCostPerMonth"`
	// Positive: self-hosting saves this much.
	SavingPerMonth float64 `json:"savingPerMonth"`
	// Tokens per month above which self-hosting is cheaper.
	BreakEvenTokensPerMonth float64 `json:"breakEvenTokensPerMonth"`
	TokensPerMonth          float64 `json:"tokensPerMonth"`
}

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

type workloadKey struct{ ns, kind, name string }

func keyOf(s collector.Snapshot) workloadKey {
	if s.WorkloadName == "" {
		return workloadKey{s.Namespace, "Pod", s.Pod}
	}
	return workloadKey{s.Namespace, s.WorkloadKind, s.WorkloadName}
}

// weights gives each snapshot the seconds it stands for: the gap to the
// same pod's next snapshot (or to now), capped at maxGap.
func weights(snaps []collector.Snapshot, now time.Time) []float64 {
	idx := map[string][]int{}
	for i, s := range snaps {
		k := s.Namespace + "/" + s.Pod
		idx[k] = append(idx[k], i)
	}
	w := make([]float64, len(snaps))
	for _, list := range idx {
		sort.Slice(list, func(a, b int) bool { return snaps[list[a]].Time.Before(snaps[list[b]].Time) })
		for j, i := range list {
			end := now
			if j+1 < len(list) {
				end = snaps[list[j+1]].Time
			}
			w[i] = min(end.Sub(snaps[i].Time), maxGap).Seconds()
			if w[i] < 0 {
				w[i] = 0
			}
		}
	}
	return w
}

// Models reports every model's cost over the window (e.g. 1h).
func (r *Reporter) Models(ctx context.Context, window time.Duration) ([]ModelCost, error) {
	now := r.now()
	peakWindow := r.PeakWindow
	if peakWindow == 0 {
		peakWindow = 7 * 24 * time.Hour
	}
	peakWindow = max(peakWindow, window)
	var (
		all   []collector.Snapshot
		peaks map[workloadKey]Peak
		err   error
	)
	if pr, ok := r.Store.(PeakReader); ok {
		// Fast path: only the window's snapshots; peaks come from SQL.
		if all, err = r.Store.Since(ctx, now.Add(-window)); err != nil {
			return nil, err
		}
		list, err := pr.OutputPeaks(ctx, now.Add(-peakWindow))
		if err != nil {
			return nil, err
		}
		peaks = map[workloadKey]Peak{}
		for _, p := range list {
			peaks[workloadKey{p.Namespace, p.Kind, p.Name}] = p
		}
	} else if all, err = r.Store.Since(ctx, now.Add(-peakWindow)); err != nil {
		return nil, err
	}
	groups := map[workloadKey][]collector.Snapshot{}
	for _, s := range all {
		groups[keyOf(s)] = append(groups[keyOf(s)], s)
	}
	var out []ModelCost
	for k, snaps := range groups {
		var pk *Peak
		if p, ok := peaks[k]; ok {
			pk = &p
		}
		m := r.model(k, snaps, now, window, pk)
		if m.Pods == 0 && m.Cost == 0 {
			continue // gone before the window started
		}
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Namespace+"/"+out[a].Workload < out[b].Namespace+"/"+out[b].Workload
	})
	return out, nil
}

func (r *Reporter) model(k workloadKey, snaps []collector.Snapshot, now time.Time, window time.Duration, pk *Peak) ModelCost {
	m := ModelCost{Namespace: k.ns, WorkloadKind: k.kind, Workload: k.name, Window: window.String()}
	w := weights(snaps, now)
	start := now.Add(-window)

	// Throughput per cycle (all pods together), for the peak.
	perCycle := map[time.Time]float64{}
	var latest time.Time
	for _, s := range snaps {
		if s.Time.After(latest) {
			latest = s.Time
		}
		if s.GenerationTokensPerSecond != nil {
			perCycle[s.Time] += *s.GenerationTokensPerSecond
		}
	}

	sources := map[string]bool{}
	var covered, tokenSeconds, busyCost, busySeconds float64
	for i, s := range snaps {
		if s.Time.Equal(latest) {
			m.Pods++
			m.Server = s.Server
			if s.Ready {
				m.ReadyPods++
			}
			if s.CostPerHour != nil {
				v := *s.CostPerHour
				if m.CostPerHour != nil {
					v += *m.CostPerHour
				}
				m.CostPerHour = &v
				sources[s.PriceSource] = true
			}
		}
		if s.Time.Before(start) {
			continue
		}
		if s.CostPerHour != nil {
			m.Cost += *s.CostPerHour * w[i] / 3600
			covered += w[i]
		}
		if s.GenerationTokensPerSecond != nil {
			m.OutputTokens += *s.GenerationTokensPerSecond * w[i]
			tokenSeconds += w[i]
			if *s.GenerationTokensPerSecond >= busyTokens && s.CostPerHour != nil {
				busyCost += *s.CostPerHour * w[i] / 3600
				busySeconds += w[i]
			}
		}
		if s.PromptTokensPerSecond != nil {
			m.InputTokens += *s.PromptTokensPerSecond * w[i]
		}
	}
	if latest.Before(now.Add(-2 * maxGap)) {
		// Not seen in the latest cycles: the model is gone.
		m.Pods, m.ReadyPods, m.CostPerHour = 0, 0, nil
	}
	for src := range sources {
		m.PriceSources = append(m.PriceSources, src)
		if src == SourceDefault {
			m.Estimate = true
		}
	}
	sort.Strings(m.PriceSources)
	if m.CostPerHour != nil {
		v := *m.CostPerHour * HoursPerMonth
		m.MonthForecast = &v
	}
	if m.OutputTokens > 0 && covered > 0 {
		v := m.Cost / m.OutputTokens * 1e6
		m.CostPer1MOutputTokens = &v
		t := m.Cost / (m.OutputTokens + m.InputTokens) * 1e6
		m.CostPer1MTokens = &t
	}
	if tokenSeconds > 0 {
		v := m.OutputTokens / tokenSeconds
		m.OutputTokensPerSecond = &v
		b := busySeconds / tokenSeconds
		m.BusyShare = &b
	}
	if m.OutputTokens > 0 && busyCost > 0 {
		v := busyCost / m.OutputTokens * 1e6
		m.CostPer1MOutputTokensBusy = &v
	}

	var peak float64
	cycles := len(perCycle)
	for _, v := range perCycle {
		peak = max(peak, v)
	}
	if pk != nil {
		peak, cycles = max(peak, pk.Max), max(cycles, pk.Cycles)
	}
	if peak > 0 {
		m.PeakOutputTokensPerSec = &peak
		if cur, ok := perCycle[latest]; ok && m.Pods > 0 {
			v := cur / peak
			m.CapacityUsed = &v
		}
	}
	if cycles < 10 {
		m.Notes = append(m.Notes, "few throughput samples yet; the peak (and capacity used) will settle as history builds up")
	}
	if tokenSeconds == 0 {
		m.Notes = append(m.Notes, "no token metrics from this model server, so no cost per token (is its ServiceMonitor set up?)")
	}
	if m.Estimate {
		m.Notes = append(m.Notes, "no price found for its node: using default unit prices, an estimate (set a price file, a node label, or install OpenCost)")
	}
	if covered > 0 && covered < window.Seconds()*0.5 {
		m.Notes = append(m.Notes, fmt.Sprintf("only %s of the %s window has data", time.Duration(covered*float64(time.Second)).Round(time.Minute), window))
	}

	if r.File != nil && m.MonthForecast != nil && tokenSeconds > 0 {
		perMonth := HoursPerMonth * 3600 / tokenSeconds // window tokens → month tokens
		in, out := m.InputTokens*perMonth, m.OutputTokens*perMonth
		for _, a := range r.File.ReferenceAPIs {
			api := in/1e6*a.InputPer1M + out/1e6*a.OutputPer1M
			c := APIComparison{API: a.Name, APICostPerMonth: api, SelfCostPerMonth: *m.MonthForecast,
				SavingPerMonth: api - *m.MonthForecast, TokensPerMonth: in + out}
			if api > 0 {
				// The API's blended price per token at this traffic mix.
				c.BreakEvenTokensPerMonth = *m.MonthForecast / (api / (in + out))
			}
			m.APIComparison = append(m.APIComparison, c)
		}
	}
	return m
}

// NodeCost splits one node's price: model pods, other pods, idle.
type NodeCost struct {
	NodePrice
	InstanceType  string  `json:"instanceType,omitempty"`
	CapacityType  string  `json:"capacityType,omitempty"`
	ModelPods     int     `json:"modelPods"`
	ModelCost     float64 `json:"modelCostPerHour"`
	OtherPods     int     `json:"otherPods"`
	OtherCost     float64 `json:"otherCostPerHour"`
	IdleCost      float64 `json:"idleCostPerHour"`
	IdleShare     float64 `json:"idleShare"`
	MonthForecast float64 `json:"monthForecast"`
}

// Nodes reports every node's cost split.
func (r *Reporter) Nodes(ctx context.Context) ([]NodeCost, error) {
	now := r.now()
	recent, err := r.Store.Since(ctx, now.Add(-2*maxGap))
	if err != nil {
		return nil, err
	}
	latest := latestPerPod(recent)
	allReqs := r.Pricer.Nodes.AllNodeRequests()
	var out []NodeCost
	for _, n := range r.Pricer.Nodes.Nodes() {
		np := r.Pricer.NodePrice(ctx, n)
		nc := NodeCost{NodePrice: np, InstanceType: n.InstanceType, CapacityType: n.CapacityType,
			MonthForecast: np.Hourly * HoursPerMonth}
		req := allReqs[n.Name]
		for _, s := range latest {
			if s.Node != n.Name {
				continue
			}
			nc.ModelPods++
			nc.ModelCost += PodCost(s, np)
			req.CPUCores -= s.CPURequestCores
			req.MemoryBytes -= s.MemoryRequestBytes
			req.GPUs -= s.GPURequest
			req.Pods--
		}
		req.CPUCores, req.MemoryBytes, req.GPUs = max(req.CPUCores, 0), max(req.MemoryBytes, 0), max(req.GPUs, 0)
		nc.OtherPods = max(req.Pods, 0)
		nc.OtherCost = min(RequestCost(req, np), max(np.Hourly-nc.ModelCost, 0))
		nc.IdleCost = max(np.Hourly-nc.ModelCost-nc.OtherCost, 0)
		if np.Hourly > 0 {
			nc.IdleShare = nc.IdleCost / np.Hourly
		}
		out = append(out, nc)
	}
	return out, nil
}

func latestPerPod(snaps []collector.Snapshot) []collector.Snapshot {
	last := map[string]collector.Snapshot{}
	for _, s := range snaps {
		k := s.Namespace + "/" + s.Pod
		if cur, ok := last[k]; !ok || s.Time.After(cur.Time) {
			last[k] = s
		}
	}
	out := make([]collector.Snapshot, 0, len(last))
	for _, s := range last {
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Namespace+out[a].Pod < out[b].Namespace+out[b].Pod })
	return out
}

// Summary is the cluster at a glance.
type Summary struct {
	Window                string   `json:"window"`
	SpendPerHour          float64  `json:"spendPerHour"` // all nodes
	MonthForecast         float64  `json:"monthForecast"`
	ModelCostPerHour      float64  `json:"modelCostPerHour"`
	IdleCostPerHour       float64  `json:"idleCostPerHour"`
	IdleShare             float64  `json:"idleShare"`
	Models                int      `json:"models"`
	ModelsServing         int      `json:"modelsServing"`
	CostPer1MOutputTokens *float64 `json:"costPer1MOutputTokens"`
	EstimatedNodes        int      `json:"estimatedNodes"`
	Nodes                 int      `json:"nodes"`
	OutputTokensInWindow  float64  `json:"outputTokensInWindow"`
	ModelCostInWindow     float64  `json:"modelCostInWindow"`
}

// Summary adds up the node and model reports.
func (r *Reporter) Summary(ctx context.Context, window time.Duration) (Summary, error) {
	nodes, err := r.Nodes(ctx)
	if err != nil {
		return Summary{}, err
	}
	models, err := r.Models(ctx, window)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Window: window.String(), Nodes: len(nodes)}
	for _, n := range nodes {
		s.SpendPerHour += n.Hourly
		s.ModelCostPerHour += n.ModelCost
		s.IdleCostPerHour += n.IdleCost
		if n.Estimate {
			s.EstimatedNodes++
		}
	}
	s.MonthForecast = s.SpendPerHour * HoursPerMonth
	if s.SpendPerHour > 0 {
		s.IdleShare = s.IdleCostPerHour / s.SpendPerHour
	}
	for _, m := range models {
		if m.Pods == 0 {
			continue
		}
		s.Models++
		if m.ReadyPods > 0 {
			s.ModelsServing++
		}
		s.OutputTokensInWindow += m.OutputTokens
		s.ModelCostInWindow += m.Cost
	}
	if s.OutputTokensInWindow > 0 {
		v := s.ModelCostInWindow / s.OutputTokensInWindow * 1e6
		s.CostPer1MOutputTokens = &v
	}
	return s, nil
}
