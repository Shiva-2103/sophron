# Roadmap

![Build plan](assets/roadmap.png)

We build Sophron one working brick at a time. Each phase:

- ends with something you can run and demo
- has a "done when" checklist
- gets a learning note in [`docs/learning/`](learning/)

Timing assumes 8 to 10 hours a week.

> Order changed on 15 Nov 2026: Phase 6 (approvals and operator) comes before Phase 5 (dashboard), so the dashboard is designed around real approvals.

## Phase 0: Repo and plan

Week 1

- [ ] Repo with license, README and docs
- [ ] Plan merged into `main`
- [ ] Project board with Phase 1 issues

Learn: open-source repo setup, working with pull requests

## Phase 1: Lab cluster

Weeks 2 to 3. A laptop cluster serving a small model.

- [x] `make lab-up` starts a kind cluster in one command
- [x] A small model answers `curl` requests
- [x] Prometheus shows tokens per second and CPU use

Learn: how vLLM serves models, KV cache, Prometheus and PromQL

## Phase 2: Collector

Weeks 4 to 6. Sophron's first code.

- [x] Collector runs as a pod in the lab (`make collector-up`)
- [x] Snapshots join Prometheus metrics with pod, node and event facts
- [x] Labelled pods are tracked even while Pending
- [x] Read-only RBAC, non-root, read-only filesystem
- [x] CI runs tidy, format, vet, lint, race tests, image build and manifest checks
- [x] `make collector-check` passes in the lab

Learn: Go project layout, `client-go` informers, RBAC, PromQL from code, Docker multi-stage builds

## Phase 3: Rules engine

Weeks 7 to 9. Detect real failures.

- [x] Engine: findings open, update, escalate and resolve (no flapping)
- [x] 6 detectors with unit tests: pending, stuck rollout, OOM, crash, slow start, queue backlog
- [x] A chaos scenario for each failure (`make chaos SCENARIO=...`)
- [x] Findings API and `make findings`
- [x] `make rules-check` passes in the lab (all 6 scenarios)
- [x] Slack message with evidence and the suggested fix (`make slack-secret`, `make slack-test`)
- [x] Resolved alerts say what changed, with which tool, and how long it was down

Learn: turning runbooks into code, chaos testing, alerts that aren't noisy

## Phase 4: Cost engine

Weeks 10 to 12. What it costs and where it should run.

- [x] Every snapshot priced: node label, price file, OpenCost, or defaults marked as an estimate
- [x] Cost per model per hour, per month and per 1M tokens
- [x] Idle cost per node; model + other pods + idle = node price
- [x] Down cost on resolved findings and in Slack
- [x] Self-hosted vs API comparison and break-even (APIs you choose)
- [x] `make cost-check` passes in the lab (5 of 5)
- [x] Savings suggestions: right-size, idle model, idle node, cheaper node fit (`make savings`)
- [ ] Savings checked by hand for 3 models in the lab

Learn: Kubernetes FinOps, GPU memory math, quantization

## Phase 5: Dashboard and first release

Weeks 13 to 16. Something strangers can install.

- [x] Dashboard: overview, models, problems, approvals, cost, activity, setup; light and dark (`make dashboard`, `make demo`)
- [x] Sign-in with admin and viewer roles
- [x] Helm chart (`charts/sophron`), linted and rendered in CI
- [x] Doctor: setup checks with the fix for each (`make doctor`, Setup page)
- [ ] `helm install` tested on a fresh cluster (needs a published image)
- [ ] Release v0.1.0 and share it
- [ ] 10 people outside us have tried it

Learn: Helm chart design, releases, building in public

## Phase 6: Approvals and operator

Weeks 17 to 20. Fix things with a human in the loop.

- [x] Policy with auto, approve and never, per action and namespace
- [x] Approve or deny from the dashboard (Slack links to it)
- [x] Approve or deny inside Slack (Socket Mode, approvers list)
- [x] Audit log page, hash-chained
- [x] A "never" action can't run, whatever happens (tested)
- [x] Follow-up steps when a fix isn't enough
- [x] `make e2e-check` passes in the lab (3 approved steps: 512Mi → 768Mi → 1280Mi → 2Gi)

Learn: operators, custom resources, safe automation

## Phase 7: LLM explanations

Weeks 21 to 22.

- [ ] Your API key or your own endpoint
- [ ] Small model on CPU
- [ ] A test set to compare explanation quality

Learn: LLM integration, prompts, evaluating LLM output

## Phase 8: Safe rollouts

Weeks 23 to 26.

- [ ] `ModelRollout` resource
- [ ] Compare old and new on your test prompts
- [ ] Promote or roll back automatically

Learn: canary releases for models, LLM evaluation

## Later

- Train a small diagnosis model on our failure data
- Support more model servers: TGI, Triton, SGLang
- View many clusters in one place
- Contribute back to HolmesGPT and OpenCost
