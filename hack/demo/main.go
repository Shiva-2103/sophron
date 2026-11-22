// Command demo serves the real dashboard and API on sample data: a day of
// snapshots for four models, open and resolved problems, a fix waiting for
// approval. No cluster needed.
//
//	go run ./hack/demo            # http://localhost:8090, admin / demo-password-1
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Shiva-2103/sophron/internal/api"
	"github.com/Shiva-2103/sophron/internal/auth"
	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/cost"
	"github.com/Shiva-2103/sophron/internal/fixes"
	"github.com/Shiva-2103/sophron/internal/kube"
	"github.com/Shiva-2103/sophron/internal/rules"
	"github.com/Shiva-2103/sophron/internal/store"
	"github.com/Shiva-2103/sophron/internal/web"
)

const gib = 1 << 30

type nodes map[string]kube.NodeInfo

func (n nodes) Node(name string) (kube.NodeInfo, bool) { x, ok := n[name]; return x, ok }
func (n nodes) Nodes() []kube.NodeInfo {
	var out []kube.NodeInfo
	for _, x := range n {
		out = append(out, x)
	}
	return out
}
func (n nodes) AllNodeRequests() map[string]kube.Requests {
	return map[string]kube.Requests{
		"cpu-1": {CPUCores: 6.5, MemoryBytes: 10 * gib, Pods: 4},
		"gpu-1": {CPUCores: 6, MemoryBytes: 30 * gib, GPUs: 1, Pods: 2},
		"gpu-2": {CPUCores: 6, MemoryBytes: 30 * gib, GPUs: 1, Pods: 1},
	}
}

type okApplier struct{}

func (okApplier) Apply(_ context.Context, a rules.Action) (string, error) {
	return "demo: " + a.Summary + " (nothing was changed, this is sample data)", nil
}

func f(v float64) *float64 { return &v }

func main() {
	addr := flag.String("listen", ":8090", "address")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "sophron-demo")
	db, err := store.Open(filepath.Join(dir, "demo.db"))
	if err != nil {
		panic(err)
	}
	pw := filepath.Join(dir, "pw")
	_ = os.WriteFile(pw, []byte("demo-password-1"), 0o600)
	authn, _ := auth.New(auth.Config{AdminPasswordFile: pw})

	ns := nodes{
		"cpu-1": {Name: "cpu-1", InstanceType: "m6i.2xlarge", CPUCores: 8, MemoryBytes: 32 * gib, HourlyPrice: "0.384", CapacityType: "on-demand"},
		"gpu-1": {Name: "gpu-1", InstanceType: "g6.2xlarge", CPUCores: 8, MemoryBytes: 32 * gib, GPUs: 1, HourlyPrice: "0.978", CapacityType: "on-demand"},
		"gpu-2": {Name: "gpu-2", InstanceType: "g5.2xlarge", CPUCores: 8, MemoryBytes: 32 * gib, GPUs: 1, HourlyPrice: "1.212", CapacityType: "on-demand"},
	}
	file := &cost.PriceFile{ReferenceAPIs: []cost.ReferenceAPI{{Name: "Example API (sample prices)", InputPer1M: 0.5, OutputPer1M: 1.5}}}
	pricer := &cost.Pricer{Nodes: ns, File: file}

	type model struct {
		ns, name, server, node, container string
		cpuReq, memReq                    float64
		gpu                               int64
		peak                              float64
	}
	models := []model{
		{"llm", "llama-3-8b", "vllm", "gpu-1", "vllm", 4, 24 * gib, 1, 420},
		{"chat", "qwen-7b", "vllm", "gpu-2", "vllm", 4, 24 * gib, 1, 300},
		{"llm", "llm-server", "llamacpp", "cpu-1", "llama-server", 4, 3 * gib, 0, 48},
		{"search", "embed-small", "tei", "cpu-1", "tei", 2, 4 * gib, 0, 1900},
	}
	now := time.Now().UTC().Truncate(time.Minute)
	var snaps []collector.Snapshot
	for i := 24 * 60; i >= 0; i-- {
		t := now.Add(-time.Duration(i) * time.Minute)
		hour := float64(t.Hour()) + float64(t.Minute())/60
		day := 0.15 + 0.85*math.Max(0, math.Sin((hour-9)/24*2*math.Pi)) // busy in the afternoon
		for j, m := range models {
			load := day
			if m.name == "qwen-7b" {
				load = math.Min(1, day*1.25)
			}
			if m.name == "embed-small" {
				load = day * 0.35
			}
			ready := true
			tok := m.peak * load
			if m.name == "llm-server" && i < 14 {
				ready, tok = false, 0 // OOM loop for the last 14 minutes
			}
			s := collector.Snapshot{Time: t, Namespace: m.ns, Pod: fmt.Sprintf("%s-7d9f8b-%d", m.name, j), Server: m.server,
				WorkloadKind: "Deployment", WorkloadName: m.name, Container: m.container, Revision: "7d9f8b", Phase: "Running", Ready: ready,
				CPURequestCores: m.cpuReq, CPULimitCores: m.cpuReq * 1.5, MemoryRequestBytes: m.memReq, MemoryLimitBytes: m.memReq,
				GPURequest: m.gpu, Node: m.node, NodeCPUCores: 8, NodeMemoryBytes: 32 * gib,
				GenerationTokensPerSecond: f(tok), PromptTokensPerSecond: f(tok * 0.3),
				CPUCores: f(m.cpuReq * (0.25 + 0.6*load)), MemoryWorkingSetBytes: f(m.memReq * (0.45 + 0.2*load))}
			if m.gpu > 0 {
				s.GPUUtilizationRatio = f(0.1 + 0.85*load)
			}
			if m.name == "llm-server" {
				s.MemoryLimitBytes, s.LastTerminationReason = 512<<20, "OOMKilled"
				s.MemoryWorkingSetBytes = f(float64(380<<20) + 120*float64(1<<20)*load) // hits the 512Mi limit at busy times
				if !ready {
					s.WaitingReason, s.Restarts = "CrashLoopBackOff", 4
				}
			}
			snaps = append(snaps, s)
		}
	}
	pricer.Price(ctx, snaps)
	if err := db.Save(ctx, snaps); err != nil {
		panic(err)
	}

	policy := fixes.Policy{Default: fixes.ModeApprove, Namespaces: []string{"llm", "chat"}}
	manager := &fixes.Manager{Store: db, Policy: policy, Applier: okApplier{}, Log: log}

	// A resolved problem with its receipt, and an open OOM with a fix waiting.
	old := rules.Finding{Key: "queue-backlog/chat/qwen-7b", Detector: rules.QueueBacklog, Severity: rules.Warning, Namespace: "chat",
		WorkloadKind: "Deployment", WorkloadName: "qwen-7b", Pod: "qwen-7b-7d9f8b-1",
		Title: "Requests are queuing on chat/qwen-7b: 6 waiting, 8 being served", Summary: "Every serving slot has been busy for several snapshots in a row.",
		Evidence: []string{"6 requests waiting, 3 snapshots in a row", "GPU is 97% busy"},
		Fix: rules.Fix{Summary: "The hardware is already busy. Add capacity to cut waiting.", Steps: []string{"Add a replica."},
			Action: &rules.Action{Kind: rules.ActionScaleUp, Namespace: "chat", WorkloadKind: "Deployment", WorkloadName: "qwen-7b", Value: "+1", Summary: "Add one replica to chat/qwen-7b"}},
		FirstSeen: now.Add(-5 * time.Hour), LastSeen: now.Add(-4 * time.Hour)}
	old.ID, _ = db.InsertFinding(ctx, old)
	_ = manager.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: old})
	if list, _ := db.Fixes(ctx, fixes.StatusPending, 5); len(list) > 0 {
		_, _ = manager.Approve(ctx, list[0].ID, "admin", "GPU is saturated; approved")
	}
	down := 0.04
	_ = db.ResolveFinding(ctx, old.ID, now.Add(-4*time.Hour), &rules.Resolution{Summary: "Fixed by a change to the workload.",
		Changes: []string{"2 of 2 pods Ready now", "requests waiting 6 → 0"}, ChangedBy: "sophron", ApprovedBy: "admin", DownCost: &down})
	_ = manager.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: old})

	oom := rules.Finding{Key: "oom-killed/llm/llm-server", Detector: rules.OOMKilled, Severity: rules.Critical, Namespace: "llm",
		WorkloadKind: "Deployment", WorkloadName: "llm-server", Pod: "llm-server-7d9f8b-2",
		Title:    "llm/llm-server is being killed for using too much memory (OOMKilled)",
		Summary:  "The model server tried to use more memory than its limit, so the kernel killed it and Kubernetes restarted it.",
		Evidence: []string{"last container exit: OOMKilled", "memory limit: 512 MiB", "restarts in the last 30m: 4", "container now: CrashLoopBackOff"},
		Fix: rules.Fix{Summary: "Give it more memory, or make it need less.", Steps: []string{"Raise the memory limit from 512 MiB to about 768Mi.", "Or lower --ctx-size / --parallel."},
			Snippet: "resources:\n  limits:\n    memory: 768Mi",
			Action:  &rules.Action{Kind: rules.ActionSetMemoryLimit, Namespace: "llm", WorkloadKind: "Deployment", WorkloadName: "llm-server", Container: "llama-server", Value: "768Mi", Summary: "Raise the memory limit of llama-server from 512Mi to 768Mi"}},
		FirstSeen: now.Add(-14 * time.Minute), LastSeen: now}
	oom.ID, _ = db.InsertFinding(ctx, oom)
	_ = manager.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: oom})

	srv := &api.Server{Store: db, Interval: 30 * time.Second, Synced: func() bool { return true },
		Status:   func() collector.Status { return collector.Status{LastRun: time.Now(), Pods: 4} },
		Findings: db, Rules: func() rules.Status { return rules.Status{} }, Detectors: rules.All(),
		Cost: &cost.Reporter{Store: db, Pricer: pricer, File: file}, Auth: authn, ClusterName: "demo-cluster",
		FixReader: db, FixDecider: manager, AuditReader: db, AuditWriter: db, UI: web.FS(),
		Setup: func(context.Context) []api.Check {
			return []api.Check{{Name: "Prometheus", Status: "ok", Detail: "sample data"}, {Name: "Token metrics", Status: "ok", Detail: "4 of 4 pods report tokens"},
				{Name: "GPU metrics", Status: "ok", Detail: "2 of 2 GPU pods report usage"}, {Name: "Prices", Status: "ok", Detail: "every pod has a real price"},
				{Name: "Slack", Status: "warn", Detail: "alerts go to the log only", Fix: "make slack-secret"}, {Name: "Login", Status: "ok", Detail: "admin sign-in is on"},
				{Name: "Fixes in llm", Status: "ok", Detail: "allowed by policy and RBAC (default mode: approve)"}, {Name: "Audit log", Status: "ok", Detail: "hash chain intact"}}
		}}
	fmt.Printf("Sophron demo on http://localhost%s  (admin / demo-password-1)\n", *addr)
	s := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := s.ListenAndServe(); err != nil {
		panic(err)
	}
}
