package cost

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/kube"
	"github.com/Shiva-2103/sophron/internal/prom"
	"sigs.k8s.io/yaml"
)

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %.6f, want %.6f", what, got, want)
	}
}

type fakeNodes struct {
	nodes map[string]kube.NodeInfo
	reqs  map[string]kube.Requests
}

func (n fakeNodes) Node(name string) (kube.NodeInfo, bool) { x, ok := n.nodes[name]; return x, ok }
func (n fakeNodes) Nodes() []kube.NodeInfo {
	var out []kube.NodeInfo
	for _, x := range n.nodes {
		out = append(out, x)
	}
	return out
}
func (n fakeNodes) NodeRequests(name string) kube.Requests { return n.reqs[name] }

type fakeProm struct{ samples []prom.Sample }

func (p fakeProm) Query(context.Context, string, time.Time) ([]prom.Sample, error) {
	return p.samples, nil
}

type memStore struct{ snaps []collector.Snapshot }

func (m memStore) Since(_ context.Context, since time.Time) ([]collector.Snapshot, error) {
	var out []collector.Snapshot
	for _, s := range m.snaps {
		if !s.Time.Before(since) {
			out = append(out, s)
		}
	}
	return out, nil
}

// The lab's EC2 t3.2xlarge: 8 cores, 32 GiB, $0.3328/hour.
var labNode = kube.NodeInfo{Name: "lab-worker", CPUCores: 8, MemoryBytes: 32 * gib, Labels: map[string]string{}}

const labPrice = 0.3328

// listPrice is the node at default unit prices: 8 × 0.031611 + 32 × 0.004237.
const listPrice = 8*0.031611 + 32*0.004237

func TestNodePriceSources(t *testing.T) {
	ctx := context.Background()
	file := &PriceFile{Nodes: []NodeRule{{Name: "lab-*", Hourly: labPrice, Note: "EC2 t3.2xlarge"}}}
	oc := fakeProm{samples: []prom.Sample{{Labels: map[string]string{"node": "aws-node"}, Value: 0.75}}}
	p := &Pricer{File: file, Prom: oc}

	labelled := labNode
	labelled.HourlyPrice = "$0.5"
	if np := p.NodePrice(ctx, labelled); np.Source != SourceNodeLabel || np.Hourly != 0.5 {
		t.Errorf("node label should win: %+v", np)
	}
	if np := p.NodePrice(ctx, labNode); np.Source != SourcePriceFile || np.Hourly != labPrice || np.Note != "EC2 t3.2xlarge" {
		t.Errorf("price file: %+v", np)
	}
	aws := kube.NodeInfo{Name: "aws-node", CPUCores: 4, MemoryBytes: 16 * gib}
	if np := p.NodePrice(ctx, aws); np.Source != SourceOpenCost || np.Hourly != 0.75 {
		t.Errorf("opencost: %+v", np)
	}
	other := kube.NodeInfo{Name: "other", CPUCores: 8, MemoryBytes: 32 * gib}
	np := p.NodePrice(ctx, other)
	if np.Source != SourceDefault || !np.Estimate {
		t.Errorf("want default estimate, got %+v", np)
	}
	near(t, "default node price", np.Hourly, listPrice)
}

func TestSplitAddsUpToNodePrice(t *testing.T) {
	gpuNode := kube.NodeInfo{Name: "gpu", CPUCores: 4, MemoryBytes: 16 * gib, GPUs: 1, HourlyPrice: "0.8048"}
	np := (&Pricer{}).NodePrice(context.Background(), gpuNode)
	whole := 4*np.Units.CPUCoreHour + 16*np.Units.MemoryGiBHour + np.Units.GPUHour
	near(t, "units × capacity", whole, 0.8048)
	if np.Units.GPUHour < 0.5*0.8048 {
		t.Errorf("a GPU node's price should land mostly on the GPU: %+v", np.Units)
	}
}

func TestPodCostChargesMaxOfRequestAndUse(t *testing.T) {
	np := (&Pricer{File: &PriceFile{Nodes: []NodeRule{{Name: "lab-*", Hourly: labPrice}}}}).NodePrice(context.Background(), labNode)
	s := collector.Snapshot{CPURequestCores: 4, CPUCores: f(6), MemoryRequestBytes: 3 * gib, MemoryWorkingSetBytes: f(1 * gib)}
	// 6 cores used (more than the 4 reserved), 3 GiB reserved (more than the 1 used).
	want := labPrice * (6*0.031611 + 3*0.004237) / listPrice
	near(t, "pod cost", PodCost(s, np), want)
}

func TestOwnedHardware(t *testing.T) {
	// $960 over 3 years + 300 W at $0.10/kWh with PUE 1.2.
	o := Owned{PurchasePrice: 960, LifetimeYears: 3, PowerWatts: 300, ElectricityPerKWh: 0.10, PUE: 1.2}
	near(t, "owned hourly", o.Hourly(), 960/(3*365*24.0)+0.3*0.10*1.2)
}

func TestPriceFileValidation(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("nodes:\n- hourly: 1\n- name: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPriceFile(bad)
	if err == nil || !strings.Contains(err.Error(), "nodes[0]") || !strings.Contains(err.Error(), "nodes[1]") {
		t.Errorf("want errors for both rules, got %v", err)
	}
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("nodes:\n- name: 'sophron-lab-*'\n  hourly: 0.1109\nreferenceAPIs:\n- name: Example API\n  inputPer1M: 0.5\n  outputPer1M: 1.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := LoadPriceFile(good)
	if err != nil || len(pf.Nodes) != 1 || len(pf.ReferenceAPIs) != 1 {
		t.Fatalf("good file: %+v, %v", pf, err)
	}
	if typo := filepath.Join(dir, "typo.yaml"); os.WriteFile(typo, []byte("node:\n- name: x\n"), 0o600) == nil {
		if _, err := LoadPriceFile(typo); err == nil {
			t.Error("an unknown key should be rejected, not silently ignored")
		}
	}
}

// labHour is one hour of the lab model at 48 output and 12 input tokens/s,
// one snapshot every 30 s, plus one busier snapshot two hours earlier.
func labHour(cost float64) []collector.Snapshot {
	base := collector.Snapshot{Namespace: "llm", Pod: "llm-server-abc", Server: "llamacpp",
		WorkloadKind: "Deployment", WorkloadName: "llm-server", Node: "lab-worker", Ready: true,
		CPURequestCores: 4, MemoryRequestBytes: 3 * gib, CostPerHour: f(cost), PriceSource: SourcePriceFile}
	var out []collector.Snapshot
	early := base
	early.Time, early.GenerationTokensPerSecond = t0.Add(-2*time.Hour), f(96)
	out = append(out, early)
	for i := 120; i >= 0; i-- {
		s := base
		s.Time = t0.Add(-time.Duration(i) * 30 * time.Second)
		s.GenerationTokensPerSecond, s.PromptTokensPerSecond = f(48), f(12)
		out = append(out, s)
	}
	return out
}

func TestModelReport(t *testing.T) {
	cost := labPrice * (4*0.031611 + 3*0.004237) / listPrice
	r := &Reporter{Store: memStore{labHour(cost)}, Now: func() time.Time { return t0 },
		File: &PriceFile{ReferenceAPIs: []ReferenceAPI{{Name: "Example API", InputPer1M: 0.5, OutputPer1M: 1.5}}}}
	models, err := r.Models(context.Background(), time.Hour)
	if err != nil || len(models) != 1 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	m := models[0]
	if m.Workload != "llm-server" || m.Pods != 1 || m.ReadyPods != 1 || m.Estimate {
		t.Errorf("unexpected model: %+v", m)
	}
	near(t, "cost in the hour", m.Cost, cost)
	near(t, "output tokens", m.OutputTokens, 48*3600)
	near(t, "cost per 1M output tokens", *m.CostPer1MOutputTokens, cost/(48*3600)*1e6)
	near(t, "cost per 1M tokens", *m.CostPer1MTokens, cost/(60*3600)*1e6)
	near(t, "month forecast", *m.MonthForecast, cost*730)
	near(t, "peak", *m.PeakOutputTokensPerSec, 96)
	near(t, "capacity used", *m.CapacityUsed, 0.5)

	if len(m.APIComparison) != 1 {
		t.Fatalf("api comparison: %+v", m.APIComparison)
	}
	c := m.APIComparison[0]
	in, out := 12*3600*730.0, 48*3600*730.0
	api := in/1e6*0.5 + out/1e6*1.5
	near(t, "API cost per month", c.APICostPerMonth, api)
	near(t, "saving", c.SavingPerMonth, api-cost*730)
	near(t, "break-even tokens", c.BreakEvenTokensPerMonth, cost*730/(api/(in+out)))
}

func TestModelWithoutTokensSaysWhy(t *testing.T) {
	s := collector.Snapshot{Time: t0, Namespace: "llm", Pod: "p", WorkloadKind: "Deployment", WorkloadName: "w",
		Node: "n", Ready: true, CostPerHour: f(0.1), PriceSource: SourceDefault}
	r := &Reporter{Store: memStore{[]collector.Snapshot{s}}, Now: func() time.Time { return t0.Add(30 * time.Second) }}
	m, _ := r.Models(context.Background(), time.Hour)
	if len(m) != 1 || m[0].CostPer1MOutputTokens != nil || !m[0].Estimate {
		t.Fatalf("got %+v", m)
	}
	notes := strings.Join(m[0].Notes, " | ")
	if !strings.Contains(notes, "no token metrics") || !strings.Contains(notes, "estimate") {
		t.Errorf("notes should explain the gaps: %s", notes)
	}
}

func TestNodeReportIdle(t *testing.T) {
	file := &PriceFile{Nodes: []NodeRule{{Name: "lab-*", Hourly: labPrice}}}
	nodes := fakeNodes{
		nodes: map[string]kube.NodeInfo{"lab-worker": labNode},
		// The model pod (4 cores, 3 GiB) plus one other pod (1 core, 1 GiB).
		reqs: map[string]kube.Requests{"lab-worker": {CPUCores: 5, MemoryBytes: 4 * gib, Pods: 2}},
	}
	snaps := labHour(0.2)
	r := &Reporter{Store: memStore{snaps}, Pricer: &Pricer{Nodes: nodes, File: file}, File: file, Now: func() time.Time { return t0 }}
	got, err := r.Nodes(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("nodes = %+v, %v", got, err)
	}
	n := got[0]
	unit := labPrice / listPrice
	model := unit * (4*0.031611 + 3*0.004237)
	other := unit * (1*0.031611 + 1*0.004237)
	near(t, "model cost", n.ModelCost, model)
	near(t, "other pods", n.OtherCost, other)
	near(t, "idle", n.IdleCost, labPrice-model-other)
	near(t, "everything adds up to the bill", n.ModelCost+n.OtherCost+n.IdleCost, labPrice)
	if n.ModelPods != 1 || n.OtherPods != 1 {
		t.Errorf("pods: model %d other %d", n.ModelPods, n.OtherPods)
	}

	sum, err := r.Summary(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "spend", sum.SpendPerHour, labPrice)
	near(t, "month", sum.MonthForecast, labPrice*730)
	if sum.Models != 1 || sum.ModelsServing != 1 || sum.CostPer1MOutputTokens == nil {
		t.Errorf("summary: %+v", sum)
	}
}

func TestPriceFillsSnapshots(t *testing.T) {
	nodes := fakeNodes{nodes: map[string]kube.NodeInfo{"lab-worker": labNode}}
	p := &Pricer{Nodes: nodes, File: &PriceFile{Nodes: []NodeRule{{Name: "lab-*", Hourly: labPrice}}}}
	snaps := []collector.Snapshot{
		{Pod: "running", Node: "lab-worker", CPURequestCores: 1},
		{Pod: "pending"},
	}
	p.Price(context.Background(), snaps)
	if snaps[0].CostPerHour == nil || *snaps[0].NodeCostPerHour != labPrice || snaps[0].PriceSource != SourcePriceFile {
		t.Errorf("running pod not priced: %+v", snaps[0])
	}
	if snaps[1].CostPerHour != nil {
		t.Error("a pending pod has no node, so no cost")
	}
}

// The lab manifest's price file must stay valid.
func TestLabPriceFile(t *testing.T) {
	data, err := os.ReadFile("../../deploy/collector/collector.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(data), "\n---\n") {
		var cm struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil || cm.Kind != "ConfigMap" {
			continue
		}
		p := filepath.Join(t.TempDir(), "prices.yaml")
		if err := os.WriteFile(p, []byte(cm.Data["prices.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		pf, err := LoadPriceFile(p)
		if err != nil {
			t.Fatalf("lab price file: %v", err)
		}
		r, ok := pf.rule("sophron-lab-worker", nil)
		if !ok || r.Hourly != 0.1109 {
			t.Errorf("lab worker rule = %+v, %v", r, ok)
		}
		return
	}
	t.Fatal("no ConfigMap with prices.yaml in the collector manifest")
}
