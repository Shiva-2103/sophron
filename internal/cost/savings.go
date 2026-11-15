package cost

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/kube"
)

// Kinds of saving.
const (
	SaveRightSize = "right-size"
	SaveIdleModel = "idle-model"
	SaveIdleNode  = "idle-node"
	SaveNodeFit   = "node-fit"
)

// Saving is one suggestion to spend less, with the evidence behind it.
type Saving struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Node      string `json:"node,omitempty"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	// USD per month, an upper bound when Confidence is "low".
	SavingPerMonth float64  `json:"savingPerMonth"`
	Confidence     string   `json:"confidence"` // high, medium, low
	Evidence       []string `json:"evidence"`
	Snippet        string   `json:"snippet,omitempty"`
}

// minSaving hides suggestions not worth a person's time.
const minSaving = 1.0 // USD per month

// minSamples is how many snapshots a workload needs before we judge it.
const minSamples = 20

// Savings looks at the window (7 days is best) and suggests what to change.
func (r *Reporter) Savings(ctx context.Context, window time.Duration) ([]Saving, error) {
	now := r.now()
	snaps, err := r.Store.Since(ctx, now.Add(-window))
	if err != nil {
		return nil, err
	}
	groups := map[workloadKey][]collector.Snapshot{}
	for _, s := range snaps {
		if s.CostPerHour != nil {
			groups[keyOf(s)] = append(groups[keyOf(s)], s)
		}
	}
	prices := map[string]NodePrice{}
	nodes := r.Pricer.Nodes.Nodes()
	for _, n := range nodes {
		prices[n.Name] = r.Pricer.NodePrice(ctx, n)
	}

	var out []Saving
	for k, g := range groups {
		if len(g) < minSamples {
			continue
		}
		last := g[len(g)-1]
		for _, s := range g {
			if s.Time.After(last.Time) {
				last = s
			}
		}
		if last.Time.Before(now.Add(-2 * maxGap)) {
			continue // gone
		}
		np, ok := prices[last.Node]
		if !ok {
			continue
		}
		pods := 0
		for _, s := range g {
			if s.Time.Equal(last.Time) {
				pods++
			}
		}
		out = append(out, rightSize(k, g, last, np, pods, window)...)
		if s, ok := idleModel(k, g, now); ok {
			out = append(out, s)
		}
		if s, ok := nodeFit(k, last, np, nodes, prices, pods); ok {
			out = append(out, s)
		}
	}

	nodeCosts, err := r.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodeCosts {
		monthly := n.IdleCost * HoursPerMonth
		if n.IdleShare >= 0.5 && monthly >= 5 {
			out = append(out, Saving{
				Kind: SaveIdleNode, Node: n.Node,
				Title:          fmt.Sprintf("Node %s is %.0f%% idle", n.Node, n.IdleShare*100),
				Detail:         "No pod reserves most of this node. Move its pods onto fewer nodes, or use a smaller instance type, and let the autoscaler remove the empty node.",
				SavingPerMonth: monthly, Confidence: "medium",
				Evidence: []string{
					fmt.Sprintf("node price $%.4f/hour (%s)", n.Hourly, n.Source),
					fmt.Sprintf("idle $%.4f/hour = $%.2f/month", n.IdleCost, monthly),
					fmt.Sprintf("%d model pods, %d other pods", n.ModelPods, n.OtherPods),
				},
			})
		}
	}

	kept := out[:0]
	for _, s := range out {
		if s.SavingPerMonth >= minSaving || s.Kind == SaveRightSize && s.SavingPerMonth == 0 {
			kept = append(kept, s)
		}
	}
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].SavingPerMonth > kept[b].SavingPerMonth })
	return kept, nil
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

// rightSize compares reservations with real use. Too much reserved costs
// money; too little risks throttling and OOM kills.
func rightSize(k workloadKey, g []collector.Snapshot, last collector.Snapshot, np NodePrice, pods int, window time.Duration) []Saving {
	var cpu, mem []float64
	for _, s := range g {
		if s.CPUCores != nil {
			cpu = append(cpu, *s.CPUCores)
		}
		if s.MemoryWorkingSetBytes != nil {
			mem = append(mem, *s.MemoryWorkingSetBytes)
		}
	}
	var out []Saving
	target := fmt.Sprintf("%s/%s", k.ns, k.name)
	if len(cpu) >= minSamples && last.CPURequestCores > 0 {
		p95 := percentile(cpu, 0.95)
		want := math.Max(0.1, math.Ceil(p95*1.2*10)/10) // p95 + 20%, in 0.1 cores
		ev := []string{
			fmt.Sprintf("CPU request %s cores per pod", cores(last.CPURequestCores)),
			fmt.Sprintf("CPU used: p95 %s cores, max %s cores over %d samples (%s)", cores(p95), cores(percentile(cpu, 1)), len(cpu), window),
		}
		switch {
		case want < last.CPURequestCores*0.8:
			saving := (last.CPURequestCores - want) * np.Units.CPUCoreHour * HoursPerMonth * float64(pods)
			out = append(out, Saving{
				Kind: SaveRightSize, Namespace: k.ns, Workload: k.name,
				Title:          fmt.Sprintf("Lower the CPU request of %s from %s to %s cores", target, cores(last.CPURequestCores), cores(want)),
				Detail:         "It reserves more CPU than it uses even at busy times, so the scheduler keeps that CPU away from other pods. The limit can stay as it is.",
				SavingPerMonth: saving, Confidence: confidence(len(cpu), window), Evidence: ev,
				Snippet: fmt.Sprintf("kubectl -n %s set resources %s/%s -c %s --requests=cpu=%s", k.ns, lower(k.kind), k.name, last.Container, cores(want)),
			})
		case p95 > last.CPURequestCores*1.2:
			out = append(out, Saving{
				Kind: SaveRightSize, Namespace: k.ns, Workload: k.name,
				Title:          fmt.Sprintf("Raise the CPU request of %s from %s to %s cores", target, cores(last.CPURequestCores), cores(want)),
				Detail:         "It regularly uses more CPU than it reserves. That works only while the node has spare CPU; on a busy node it slows down. This costs a little more but makes it reliable.",
				SavingPerMonth: 0, Confidence: confidence(len(cpu), window), Evidence: ev,
				Snippet: fmt.Sprintf("kubectl -n %s set resources %s/%s -c %s --requests=cpu=%s", k.ns, lower(k.kind), k.name, last.Container, cores(want)),
			})
		}
	}
	if len(mem) >= minSamples && last.MemoryRequestBytes > 0 {
		top := percentile(mem, 1)
		want := memStep(top * 1.25) // memory is not elastic: peak + 25%
		if want < last.MemoryRequestBytes*0.8 {
			saving := (last.MemoryRequestBytes - want) / gib * np.Units.MemoryGiBHour * HoursPerMonth * float64(pods)
			out = append(out, Saving{
				Kind: SaveRightSize, Namespace: k.ns, Workload: k.name,
				Title:          fmt.Sprintf("Lower the memory request of %s from %s to %s", target, quantityBytes(last.MemoryRequestBytes), quantityBytes(want)),
				Detail:         "Its highest memory use is well below what it reserves. Keep the limit above the peak so it isn't OOM-killed.",
				SavingPerMonth: saving, Confidence: confidence(len(mem), window),
				Evidence: []string{
					fmt.Sprintf("memory request %s per pod", quantityBytes(last.MemoryRequestBytes)),
					fmt.Sprintf("highest memory used: %s over %d samples (%s)", quantityBytes(top), len(mem), window),
				},
				Snippet: fmt.Sprintf("kubectl -n %s set resources %s/%s -c %s --requests=memory=%s", k.ns, lower(k.kind), k.name, last.Container, quantityBytes(want)),
			})
		}
	}
	return out
}

// idleModel finds models that mostly wait for traffic.
func idleModel(k workloadKey, g []collector.Snapshot, now time.Time) (Saving, bool) {
	w := weights(g, now)
	var busy, total, cost float64
	for i, s := range g {
		if s.GenerationTokensPerSecond == nil {
			continue
		}
		total += w[i]
		if *s.GenerationTokensPerSecond >= busyTokens {
			busy += w[i]
		}
		cost += *s.CostPerHour * w[i] / 3600
	}
	if total < 6*3600 || cost == 0 {
		return Saving{}, false // judge only with at least 6 hours of data
	}
	share := busy / total
	monthly := cost / (total / 3600) * HoursPerMonth
	if share >= 0.1 {
		return Saving{}, false
	}
	return Saving{
		Kind: SaveIdleModel, Namespace: k.ns, Workload: k.name,
		Title:          fmt.Sprintf("%s/%s is busy only %.0f%% of the time", k.ns, k.name, share*100),
		Detail:         "It is paid for around the clock but serves traffic rarely. Scale it to zero outside working hours (KEDA or a CronJob), or share one model server between teams.",
		SavingPerMonth: monthly * (1 - share), Confidence: "low",
		Evidence: []string{
			fmt.Sprintf("producing tokens %.0f%% of %s", share*100, time.Duration(total*float64(time.Second)).Round(time.Hour)),
			fmt.Sprintf("costs about $%.2f/month", monthly),
		},
	}, true
}

// nodeFit looks for a cheaper node type, already in the cluster, that has
// room for the model's pod.
func nodeFit(k workloadKey, last collector.Snapshot, cur NodePrice, nodes []kube.NodeInfo, prices map[string]NodePrice, pods int) (Saving, bool) {
	needCPU := last.CPURequestCores
	if last.CPUCores != nil {
		needCPU = max(needCPU, *last.CPUCores)
	}
	needMem := last.MemoryRequestBytes
	if last.MemoryWorkingSetBytes != nil {
		needMem = max(needMem, *last.MemoryWorkingSetBytes)
	}
	// Only worth it when the model takes most of its node.
	var curNode kube.NodeInfo
	for _, n := range nodes {
		if n.Name == last.Node {
			curNode = n
		}
	}
	if curNode.CPUCores == 0 || needCPU/curNode.CPUCores < 0.5 && last.GPURequest == 0 {
		return Saving{}, false
	}
	var best kube.NodeInfo
	bestPrice := cur.Hourly
	for _, n := range nodes {
		p := prices[n.Name]
		if n.Name == last.Node || p.Estimate || n.InstanceType == "" || n.InstanceType == curNode.InstanceType {
			continue
		}
		fits := n.CPUCores >= needCPU*1.1 && n.MemoryBytes >= needMem*1.1 && n.GPUs >= last.GPURequest
		if fits && p.Hourly < bestPrice*0.9 {
			best, bestPrice = n, p.Hourly
		}
	}
	if best.Name == "" {
		return Saving{}, false
	}
	monthly := (cur.Hourly - bestPrice) * HoursPerMonth * float64(pods)
	return Saving{
		Kind: SaveNodeFit, Namespace: k.ns, Workload: k.name,
		Title:          fmt.Sprintf("Run %s/%s on %s instead of %s", k.ns, k.name, best.InstanceType, curNode.InstanceType),
		Detail:         "A cheaper node type already in your cluster has room for this model. Pin it there with a node selector, and let the autoscaler remove the bigger node. Check the speed in a test first: fewer cores can mean fewer tokens per second.",
		SavingPerMonth: monthly, Confidence: "medium",
		Evidence: []string{
			fmt.Sprintf("needs %s cores, %s memory, %d GPUs", cores(needCPU), quantityBytes(needMem), last.GPURequest),
			fmt.Sprintf("now on %s at $%.4f/hour", curNode.InstanceType, cur.Hourly),
			fmt.Sprintf("%s has %s cores, %s, %d GPUs at $%.4f/hour", best.InstanceType, cores(best.CPUCores), quantityBytes(best.MemoryBytes), best.GPUs, bestPrice),
		},
		Snippet: fmt.Sprintf("spec:\n  template:\n    spec:\n      nodeSelector:\n        node.kubernetes.io/instance-type: %s", best.InstanceType),
	}, true
}

func confidence(samples int, window time.Duration) string {
	switch {
	case window >= 7*24*time.Hour && samples >= 1000:
		return "high"
	case window >= 24*time.Hour && samples >= 200:
		return "medium"
	}
	return "low"
}

func cores(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	for len(s) > 1 && (s[len(s)-1] == '0') {
		s = s[:len(s)-1]
	}
	if s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// memStep rounds up to 128Mi.
func memStep(b float64) float64 {
	const step = 128 << 20
	return math.Ceil(b/step) * step
}

func quantityBytes(b float64) string {
	if b >= gib && math.Mod(b, gib) == 0 {
		return fmt.Sprintf("%.0fGi", b/gib)
	}
	return fmt.Sprintf("%.0fMi", math.Ceil(b/(1<<20)))
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
