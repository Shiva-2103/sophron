package cost

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/kube"
)

func TestSavings(t *testing.T) {
	big := kube.NodeInfo{Name: "big-1", InstanceType: "type-big", CPUCores: 8, MemoryBytes: 32 * gib, Labels: map[string]string{"size": "big"}}
	small := kube.NodeInfo{Name: "small-1", InstanceType: "type-small", CPUCores: 6, MemoryBytes: 16 * gib, Labels: map[string]string{"size": "small"}}
	file := &PriceFile{Nodes: []NodeRule{
		{Match: map[string]string{"size": "big"}, Hourly: 0.40},
		{Match: map[string]string{"size": "small"}, Hourly: 0.20},
	}}
	nodes := fakeNodes{nodes: map[string]kube.NodeInfo{"big-1": big, "small-1": small},
		reqs: map[string]kube.Requests{"big-1": {CPUCores: 4, MemoryBytes: 3 * gib, Pods: 1}}}

	// 8 hours, one snapshot a minute: reserves 4 cores and 3 GiB, uses about
	// 1 core and 1 GiB, and produces tokens only 2% of the time.
	var snaps []collector.Snapshot
	for i := 480; i >= 0; i-- {
		tok := 0.0
		if i%50 == 0 {
			tok = 40
		}
		snaps = append(snaps, collector.Snapshot{
			Time: t0.Add(-time.Duration(i) * time.Minute), Namespace: "llm", Pod: "p", Container: "server",
			WorkloadKind: "Deployment", WorkloadName: "chat", Node: "big-1", Ready: true,
			CPURequestCores: 4, CPUCores: f(0.8 + float64(i%5)*0.05), MemoryRequestBytes: 3 * gib,
			MemoryWorkingSetBytes: f(1 * gib), GenerationTokensPerSecond: f(tok), CostPerHour: f(0.2), PriceSource: SourcePriceFile,
		})
	}
	r := &Reporter{Store: memStore{snaps}, Pricer: &Pricer{Nodes: nodes, File: file}, File: file, Now: func() time.Time { return t0 }}
	got, err := r.Savings(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]Saving{}
	for _, s := range got {
		kinds[s.Kind+"|"+strings.SplitN(s.Title, " ", 4)[2]] = s
		if s.SavingPerMonth < 0 {
			t.Errorf("negative saving: %+v", s)
		}
	}
	cpu, ok := kinds[SaveRightSize+"|CPU"]
	if !ok || !strings.Contains(cpu.Title, "from 4 to 1.2 cores") || !strings.Contains(cpu.Snippet, "-c server --requests=cpu=1.2") {
		t.Errorf("cpu right-size: %+v (all: %+v)", cpu, got)
	}
	mem, ok := kinds[SaveRightSize+"|memory"]
	if !ok || !strings.Contains(mem.Title, "from 3Gi to 1280Mi") {
		t.Errorf("memory right-size: %+v", mem)
	}
	found := map[string]bool{}
	for _, s := range got {
		found[s.Kind] = true
	}
	if !found[SaveIdleModel] {
		t.Errorf("want an idle-model suggestion: %+v", got)
	}
	if !found[SaveIdleNode] {
		t.Errorf("want an idle-node suggestion for big-1: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].SavingPerMonth > got[i-1].SavingPerMonth {
			t.Error("savings should be sorted, biggest first")
		}
	}
}

func TestNodeFit(t *testing.T) {
	big := kube.NodeInfo{Name: "big-1", InstanceType: "type-big", CPUCores: 8, MemoryBytes: 32 * gib}
	small := kube.NodeInfo{Name: "small-1", InstanceType: "type-small", CPUCores: 6, MemoryBytes: 16 * gib}
	prices := map[string]NodePrice{"big-1": {Hourly: 0.4}, "small-1": {Hourly: 0.2}}
	last := collector.Snapshot{Node: "big-1", CPURequestCores: 4, CPUCores: f(5), MemoryRequestBytes: 4 * gib}
	s, ok := nodeFit(workloadKey{"llm", "Deployment", "chat"}, last, prices["big-1"], []kube.NodeInfo{big, small}, prices, 1)
	if !ok || s.SavingPerMonth != 0.2*730 || !strings.Contains(s.Snippet, "type-small") {
		t.Fatalf("node fit: %+v %v", s, ok)
	}
	// Needs 7 cores: doesn't fit on the 6-core node.
	last.CPUCores = f(7)
	if _, ok := nodeFit(workloadKey{"llm", "Deployment", "chat"}, last, prices["big-1"], []kube.NodeInfo{big, small}, prices, 1); ok {
		t.Error("a node that's too small must not be suggested")
	}
}
