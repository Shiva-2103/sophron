# Sophron developer commands. Run `make help` to list them.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

CLUSTER      ?= sophron-lab
LAB_DIR      := lab
STATE_DIR    := .lab
REQUESTS     ?= 25
MON_NS       := monitoring
MON_RELEASE  := monitoring
IMAGE        ?= sophron-collector:dev

.PHONY: help slack-buttons slack-approvers admin-password dashboard dashboard-private dashboard-allow-me dashboard-revoke-me dashboard-stop dashboard-3000 dashboard-3000-off dashboard-tunnel demo doctor fixes e2e-check savings cost cost-models cost-nodes cost-check slack-secret slack-test slack-off build test lint findings findings-all detectors chaos-list chaos chaos-reset rules-check collector-image collector-deploy collector-up collector-logs collector-status collector-snapshots collector-check lab-tools lab-up lab-down lab-status lab-load lab-check lab-load-summary lab-bench lab-ports lab-ports-stop lab-grafana lab-grafana-allow-me lab-grafana-revoke-me lab-grafana-password

help: ## List available commands
	@awk 'BEGIN{FS=":.*?## "} /^##@/{printf "\n\033[1m%s\033[0m\n", substr($$0,5)} /^[a-zA-Z0-9_-]+:.*?## /{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Go
build: ## Build the collector binary into bin/
	CGO_ENABLED=0 go build -trimpath -o bin/sophron-collector ./cmd/collector

test: ## Run unit tests (with the race detector)
	go test -race -count=1 ./...

lint: ## Format check, go vet, and golangci-lint if installed
	@test -z "$$(gofmt -l .)" || (echo "Run gofmt -w on:"; gofmt -l .; exit 1)
	go vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run || echo "golangci-lint not installed, skipped (CI runs it)"

##@ Collector (in the lab cluster)
collector-image: ## Build the collector image and load it into the kind cluster
	docker build -t $(IMAGE) .
	kind load docker-image $(IMAGE) --name $(CLUSTER)

collector-deploy: ## Deploy or redeploy the collector
	kubectl apply -f deploy/collector/collector.yaml
	kubectl -n sophron-system rollout restart deploy/sophron-collector
	kubectl -n sophron-system rollout status deploy/sophron-collector --timeout=3m

collector-up: collector-image collector-deploy ## Build, load and deploy the collector

collector-logs: ## Follow the collector logs
	kubectl -n sophron-system logs -f deploy/sophron-collector

collector-status: ## Show the last collection cycle
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/status

collector-snapshots: ## Show the latest snapshot of every model pod
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/snapshots/latest

collector-check: ## Verify the Phase 2 "done when" list
	@bash $(LAB_DIR)/scripts/check-collector.sh

##@ Rules engine (Phase 3)
findings: ## Show open findings (problems Sophron found)
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/findings

findings-all: ## Show open and resolved findings
	@bash $(LAB_DIR)/scripts/api.sh '/api/v1/findings?status=all'

detectors: ## List the detectors and what each looks for
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/detectors

chaos-list: ## List the chaos scenarios
	@bash $(LAB_DIR)/scripts/chaos.sh list

chaos: ## Break the model server on purpose: make chaos SCENARIO=oom
	@test -n "$(SCENARIO)" || (bash $(LAB_DIR)/scripts/chaos.sh list; echo; echo "Usage: make chaos SCENARIO=<name>"; exit 1)
	@bash $(LAB_DIR)/scripts/chaos.sh $(SCENARIO)

chaos-reset: ## Undo any chaos scenario
	@bash $(LAB_DIR)/scripts/chaos.sh reset

rules-check: ## Phase 3 check: run each chaos scenario, verify the right detector fires (~25 min)
	@bash $(LAB_DIR)/scripts/check-rules.sh $(SCENARIOS)

##@ Cost engine (Phase 4)
cost: ## Cluster cost at a glance: spend, idle, cost per 1M tokens (WINDOW=1h)
	@bash $(LAB_DIR)/scripts/api.sh '/api/v1/cost/summary?window=$(or $(WINDOW),1h)'

cost-models: ## Cost and throughput per model (WINDOW=1h)
	@bash $(LAB_DIR)/scripts/api.sh '/api/v1/cost/models?window=$(or $(WINDOW),1h)'

cost-nodes: ## Each node: price, model pods, other pods, idle
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/cost/nodes

cost-check: ## Phase 4 check: send load, verify prices and cost per token (~3 min)
	@bash $(LAB_DIR)/scripts/check-cost.sh

savings: ## Savings suggestions from the last 7 days
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/savings

##@ Dashboard and fixes (Phase 5/6)
admin-password: ## Create the dashboard admin and viewer passwords (once) and print them
	@bash $(LAB_DIR)/scripts/dashboard.sh password

dashboard: ## Serve the dashboard at http://<public-ip>:8081 (open 8081 to your IP in the security group)
	@bash $(LAB_DIR)/scripts/dashboard.sh start

dashboard-private: ## Serve the dashboard on this machine only; prints the SSM port-forward for your laptop
	@BIND=127.0.0.1 bash $(LAB_DIR)/scripts/dashboard.sh start

dashboard-allow-me: ## EC2: open port 8081 in the security group for your IP only
	@PORT=8081 bash $(LAB_DIR)/scripts/grafana.sh allow

dashboard-revoke-me: ## EC2: close port 8081 for your IP again
	@PORT=8081 bash $(LAB_DIR)/scripts/grafana.sh revoke

dashboard-stop: ## Stop serving the dashboard
	@bash $(LAB_DIR)/scripts/dashboard.sh stop

dashboard-3000: ## No SSM? Serve the dashboard on port 3000 (Grafana's port and security group rule)
	@bash $(LAB_DIR)/scripts/expose.sh port3000-on

dashboard-3000-off: ## Put Grafana back on port 3000
	@bash $(LAB_DIR)/scripts/expose.sh port3000-off

dashboard-tunnel: ## No SSM, no security group? Public HTTPS link via a Cloudflare quick tunnel
	@bash $(LAB_DIR)/scripts/expose.sh tunnel

demo: ## Run the dashboard on sample data, no cluster needed (http://localhost:8090)
	go run ./hack/demo

doctor: ## Check Sophron's setup: data sources, prices, Slack, login, fix permissions
	@bash $(LAB_DIR)/scripts/api.sh /api/v1/setup

fixes: ## Fix requests and their status
	@bash $(LAB_DIR)/scripts/api.sh '/api/v1/fixes?status=all'

e2e-check: ## The whole loop: break, alert, approve, fixed, receipt (~5-15 min)
	@bash $(LAB_DIR)/scripts/check-e2e.sh

##@ Slack
slack-secret: ## Save the Slack webhook URL as a Kubernetes Secret (hidden prompt)
	@bash $(LAB_DIR)/scripts/slack.sh secret

slack-buttons: ## Save the Slack app-level token (hidden prompt) for Approve/Deny buttons
	@bash $(LAB_DIR)/scripts/slack.sh buttons

slack-approvers: ## Who may approve from Slack: make slack-approvers IDS=U0123ABCD
	@bash $(LAB_DIR)/scripts/slack.sh approvers "$(IDS)"

slack-test: ## Send a sample alert to Slack
	@bash $(LAB_DIR)/scripts/slack.sh test

slack-off: ## Delete the Slack Secret (alerts stop)
	@bash $(LAB_DIR)/scripts/slack.sh off

##@ Lab
lab-tools: ## Install Docker, kind, kubectl, helm on Ubuntu (needs sudo, run once)
	sudo bash $(LAB_DIR)/scripts/bootstrap-ubuntu.sh

lab-up: ## Create the lab cluster with monitoring and a model server
	@mkdir -p $(STATE_DIR)
	@if kind get clusters 2>/dev/null | grep -qx $(CLUSTER); then \
	  echo "==> Cluster $(CLUSTER) already exists"; \
	else \
	  echo "==> Creating kind cluster $(CLUSTER)"; \
	  kind create cluster --config $(LAB_DIR)/kind/cluster.yaml --wait 120s; \
	fi
	@test -s $(STATE_DIR)/grafana-password || openssl rand -base64 18 > $(STATE_DIR)/grafana-password
	@echo "==> Installing Prometheus + Grafana (kube-prometheus-stack)"
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
	helm repo update >/dev/null
	helm upgrade --install $(MON_RELEASE) prometheus-community/kube-prometheus-stack \
	  --namespace $(MON_NS) --create-namespace \
	  -f $(LAB_DIR)/monitoring/values.yaml \
	  --set grafana.adminPassword="$$(cat $(STATE_DIR)/grafana-password)" \
	  --wait --timeout 10m
	@echo
	@bash $(LAB_DIR)/scripts/grafana.sh url || true
	@echo
	@echo "==> Deploying the model server (first start downloads about 1 GB)"
	kubectl apply -f $(LAB_DIR)/llm/model-server.yaml
	kubectl -n llm rollout status deploy/llm-server --timeout=15m
	@echo
	@echo "Lab is up. Grafana: make lab-grafana. Next: make lab-check (then: make lab-load)"

lab-down: ## Delete the lab cluster (the model download is lost too)
	kind delete cluster --name $(CLUSTER)
	@rm -f $(STATE_DIR)/port-forward.pids

lab-status: ## Show nodes and the main pods
	kubectl get nodes -L sophron.dev/role
	@echo
	kubectl get pods -n llm -o wide
	@echo
	kubectl get pods -n $(MON_NS)

lab-load: ## Send test requests to the model (REQUESTS=25 per worker, 4 workers)
	-kubectl -n llm delete job loadgen --ignore-not-found
	sed 's/value: "25"/value: "$(REQUESTS)"/' $(LAB_DIR)/loadgen/job.yaml | kubectl apply -f -
	@echo "==> Following logs (Ctrl+C stops watching, the load keeps running)"
	@kubectl -n llm wait --for=condition=Ready pod -l job-name=loadgen --timeout=120s >/dev/null || true
	@kubectl -n llm logs -f -l job-name=loadgen --max-log-requests 4 --prefix || true
	@echo
	@kubectl -n llm wait --for=condition=complete job/loadgen --timeout=60s >/dev/null 2>&1 || true
	@echo "==> Summary (rerun anytime: make lab-load-summary)"
	@$(MAKE) --no-print-directory lab-load-summary

lab-load-summary: ## Show how many load requests succeeded
	@kubectl -n llm get job loadgen -o jsonpath='{.status.succeeded}{" of 4 workers finished"}{"\n"}' 2>/dev/null || echo "No load job found"
	@kubectl -n llm logs -l job-name=loadgen --prefix 2>/dev/null | grep finished || true

lab-bench: ## Benchmark the model the same way every time (REQUESTS=25 per worker)
	@bash $(LAB_DIR)/scripts/bench.sh $(REQUESTS)

lab-check: ## Verify the Phase 1 "done when" list
	bash $(LAB_DIR)/scripts/check.sh

lab-ports: ## Open Prometheus :9090 and the model API :8080 on this machine's localhost
	bash $(LAB_DIR)/scripts/ports.sh start

lab-ports-stop: ## Close the ports opened by lab-ports
	bash $(LAB_DIR)/scripts/ports.sh stop

lab-grafana: ## Show the Grafana URL (port 3000) and login
	@bash $(LAB_DIR)/scripts/grafana.sh url

lab-grafana-allow-me: ## EC2: open port 3000 in the security group for your IP only
	@bash $(LAB_DIR)/scripts/grafana.sh allow

lab-grafana-revoke-me: ## EC2: close port 3000 for your IP again
	@bash $(LAB_DIR)/scripts/grafana.sh revoke

lab-grafana-password: ## Print the Grafana admin password (user: admin)
	@cat $(STATE_DIR)/grafana-password
