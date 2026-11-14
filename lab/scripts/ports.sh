#!/usr/bin/env bash
# Open (or close) local ports to the lab's web UIs using kubectl port-forward.
# Grafana is not here: it is always published on port 3000 (see lab/kind/cluster.yaml).
# Ports listen on 127.0.0.1 only, so nothing is exposed to the internet.
# From your laptop, reach them through an SSH tunnel (see lab/README.md).
#
# Usage: bash lab/scripts/ports.sh start|stop
set -euo pipefail

STATE_DIR=".lab"
PIDS="$STATE_DIR/port-forward.pids"
mkdir -p "$STATE_DIR"

svc_by_label() { # namespace label -> service name
  kubectl -n "$1" get svc -l "$2" -o jsonpath='{.items[0].metadata.name}'
}

stop() {
  if [[ -f "$PIDS" ]]; then
    while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$PIDS"
    rm -f "$PIDS"
    echo "Port forwards stopped."
  else
    echo "No port forwards running."
  fi
}

start() {
  stop >/dev/null
  local prom
  prom="$(svc_by_label monitoring app=kube-prometheus-stack-prometheus)"

  forward() { # namespace service local:remote
    kubectl -n "$1" port-forward "svc/$2" "$3" --address 127.0.0.1 >"$STATE_DIR/pf-$2.log" 2>&1 &
    echo $! >> "$PIDS"
  }
  forward monitoring "$prom"    9090:9090
  forward llm        llm-server 8080:8080
  sleep 2

  echo "Open on this machine (or through your SSH tunnel):"
  echo "  Grafana     http://localhost:3000   (always on, no forward needed: make lab-grafana)"
  echo "  Prometheus  http://localhost:9090"
  echo "  Model API   http://localhost:8080/v1/chat/completions"
  echo "Stop with: make lab-ports-stop"
}

case "${1:-}" in
  start) start ;;
  stop)  stop ;;
  *) echo "usage: $0 start|stop" >&2; exit 1 ;;
esac
