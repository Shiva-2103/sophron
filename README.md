# Sophron

> **Prudent operations for self-hosted LLMs on Kubernetes.**

*Sophron* (σώφρων) is Greek for "prudent, of sound mind". Kubernetes is the Greek *helmsman* that steers your workloads; Sophron is the prudent one that keeps your self-hosted LLMs **affordable, healthy and safe to change**.

> **Status:** Planning (Phase 0). Nothing to install yet. Follow the [roadmap](docs/ROADMAP.md).

---

## The problem

More teams now run open models (Llama, Qwen, Mistral…) on their own Kubernetes clusters, using servers like vLLM or KServe. They keep hitting the same three questions:

1. **"What is this actually costing us?"** GPUs are expensive, but nobody can say what one request costs, which GPUs sit idle, or whether a cheaper node would do.
2. **"Why did it break?"** Out-of-memory crashes, full KV cache, slow cold starts, pods stuck waiting for a GPU. The failures repeat, but every team debugs them from scratch.
3. **"Is this change safe?"** Swapping a model, changing context length or upgrading vLLM is usually "deploy and hope".

## What Sophron does

| | What you get |
|---|---|
| **See the cost** | Cost per model, cost per 1,000 tokens, idle GPU hours, and "this model fits on a cheaper node" suggestions |
| **Understand failures** | Known LLM-serving failures detected automatically and explained in plain English, with a ready fix |
| **Fix safely** | Fixes are proposed, checked against *your* policy, and applied only after approval (dashboard or Slack). Everything is audited. |
| **Change safely** *(later)* | Model rollouts that run your eval set on a small slice of traffic and roll back automatically if quality or latency drops |

## Principles

- **Runs entirely in your cluster.** One Helm chart. No data leaves your infrastructure. No phone-home.
- **Read-only by default.** Sophron never changes anything until you enable fixes and approve them.
- **Works without any AI.** The core diagnosis is a deterministic rules engine. An LLM is optional, for plain-English explanations (bring your own API key, your own endpoint, or a small bundled local model).
- **Reuses what you already run.** Your Prometheus, your Grafana, your Slack.
- **Easy to install, honest when it can't.** `sophron doctor` tells you exactly what's missing and how to fix it.

## Documentation

| Doc | What's inside |
|---|---|
| [Architecture](docs/ARCHITECTURE.md) | How Sophron works, part by part, in plain language |
| [Roadmap](docs/ROADMAP.md) | The phase-by-phase build plan |
| [Workflow](docs/WORKFLOW.md) | Branches, commits, PRs and releases |
| [Learning notes](docs/learning/) | What we built in each phase, how, why, and real-world examples |

## License

Apache License 2.0.
