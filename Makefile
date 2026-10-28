# Sophron developer commands. Run `make help` to list them.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

CLUSTER      ?= sophron-lab
LAB_DIR      := lab
STATE_DIR    := .lab
REQUESTS     ?= 25
MON_NS       := monitoring
MON_RELEASE  := monitoring

.PHONY: help lab-tools lab-up lab-down lab-status lab-load lab-check lab-ports lab-ports-stop lab-grafana-password

help: ## List available commands
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

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
	@echo "==> Deploying the model server (first start downloads about 1 GB)"
	kubectl apply -f $(LAB_DIR)/llm/model-server.yaml
	kubectl -n llm rollout status deploy/llm-server --timeout=15m
	@echo
	@echo "Lab is up. Next: make lab-check   (then: make lab-load)"

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
	@echo "==> Following logs (Ctrl+C stops watching, not the load)"
	kubectl -n llm wait --for=condition=Ready pod -l job-name=loadgen --timeout=120s || true
	kubectl -n llm logs -f -l job-name=loadgen --max-log-requests 4 --prefix

lab-check: ## Verify the Phase 1 "done when" list
	bash $(LAB_DIR)/scripts/check.sh

lab-ports: ## Open Grafana :3000, Prometheus :9090, model :8080 on this machine's localhost
	bash $(LAB_DIR)/scripts/ports.sh start

lab-ports-stop: ## Close the ports opened by lab-ports
	bash $(LAB_DIR)/scripts/ports.sh stop

lab-grafana-password: ## Print the Grafana admin password (user: admin)
	@cat $(STATE_DIR)/grafana-password
