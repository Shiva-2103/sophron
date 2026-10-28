# Architecture

![Inside Sophron](assets/architecture.png)

Sophron works in four steps: **read, think, show, act**. It never acts without approval.

## The parts

### 1. Collector

Reads what your cluster already reports.

- Asks Prometheus for GPU use, tokens per second, queue length and KV cache usage
- Watches Kubernetes for pods, nodes, restarts and errors
- Saves one clean snapshot per model every 30 to 60 seconds

Think of it as a meter reader who checks every meter on a schedule.

Built with: Go, `client-go`, Prometheus API

### 2. Store

Keeps snapshots, problems found, proposed fixes and the audit log.

- SQLite by default, so there's nothing to set up
- Postgres for larger clusters

### 3. Rules engine

Finds known problems. Each rule (a "detector") looks for one failure:

| Detector | Notices | Suggests |
|---|---|---|
| `oom-context-length` | Pod ran out of memory, context length too high for the GPU | Lower `max-model-len` |
| `kv-cache-pressure` | KV cache almost full, requests waiting | Add a replica |
| `gpu-idle` | GPU under 10% use for hours | Scale down or share it |
| `pending-no-gpu` | Pod waiting, no free GPU | Check the node pool |
| `slow-cold-start` | Pod takes 10+ minutes to start | Cache the model on a volume |

Why rules, not AI? Rules are free, work offline, can be tested, and never invent a risky action.

Built with: Go, unit tests, plus a test scenario that triggers each failure on purpose

### 4. Cost engine

Answers "what does this cost, and what should it run on?"

- Gets node prices from cloud pricing APIs, or from prices you enter for on-prem
- Uses [OpenCost](https://www.opencost.io) for the basic Kubernetes cost split
- Adds cost per model, cost per 1,000 tokens and idle GPU cost
- Checks if a model fits on a cheaper node, and gives you the YAML change

Sophron suggests node changes. Your autoscaler (Karpenter or Cluster Autoscaler) does the actual work.

### 5. API and dashboard

Shows everything in one place: overview, cost, endpoints, problems, approvals, audit log and setup health.

Built with: Go API, React dashboard, served inside your cluster

### 6. Operator

Applies a fix only when your policy allows it.

![How a fix happens](assets/fix-flow.png)

Your rules live in a `RemediationPolicy`, a Kubernetes resource you control. Every action is written to the audit log.

Built with: Kubebuilder, custom resources, Slack buttons

### 7. LLM explanations (optional)

The rules already know what's wrong. An LLM only explains it in plain English. Pick one:

| Option | Runs on | Best for |
|---|---|---|
| None (default) | Pre-written explanations | Everyone, free, offline |
| Small bundled model | Your CPU | No API key, data stays inside |
| Your API key | OpenAI, Anthropic, Gemini | Best explanations |
| Your own endpoint | Your vLLM | Self-hosters |

The LLM never chooses actions. Actions come only from tested fixes.

### 8. Safe rollouts (later)

A new model version gets a small slice of traffic. Sophron runs your test prompts on old and new, compares quality and speed, then promotes or rolls back.

Built with: Argo Rollouts analysis

## Folder layout

```text
sophron/
├── cmd/            # programs: CLI, collector, api, operator
├── internal/       # collector, store, rules, cost, notify, explain
├── api/v1alpha1/   # custom resource types
├── charts/sophron/ # Helm chart
├── web/            # dashboard
├── lab/            # local test cluster and failure scenarios
└── docs/           # these docs
```

## Tech choices

| Choice | Why |
|---|---|
| Go | Standard for Kubernetes tools, small and fast |
| Kubebuilder | Standard way to build operators |
| SQLite, then Postgres | Zero setup, scales later |
| React | Widely known |
| Helm | Every Kubernetes user has it |
| kind or k3s for testing | Free, runs on a laptop |
