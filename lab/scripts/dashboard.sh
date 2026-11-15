#!/usr/bin/env bash
# The Sophron dashboard in the lab.
#
#   bash lab/scripts/dashboard.sh password   create the admin password (once), print it
#   bash lab/scripts/dashboard.sh start      serve the dashboard on this machine's port 8081
#   bash lab/scripts/dashboard.sh stop
#
# From your laptop, forward the port through SSM (no security group change):
#   aws ssm start-session --target <instance-id> --document-name AWS-StartPortForwardingSession \
#     --parameters '{"portNumber":["8081"],"localPortNumber":["8081"]}'
# then open http://localhost:8081
set -euo pipefail

NS=sophron-system
PORT="${PORT:-8081}"
STATE_DIR=".lab"
PID="$STATE_DIR/dashboard.pid"
mkdir -p "$STATE_DIR"

imds() {
  local token
  token=$(curl -fsS -m 2 -X PUT http://169.254.169.254/latest/api/token \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60" 2>/dev/null) || return 0
  curl -fsS -m 2 -H "X-aws-ec2-metadata-token: $token" "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null || true
}

case "${1:-}" in
  password)
    if kubectl -n "$NS" get secret sophron-admin >/dev/null 2>&1; then
      echo "An admin password already exists. To see it:"
      echo "  kubectl -n $NS get secret sophron-admin -o jsonpath='{.data.password}' | base64 -d; echo"
      echo "To replace it: kubectl -n $NS delete secret sophron-admin && make admin-password"
      exit 0
    fi
    pw=$(openssl rand -base64 24 | tr -d '/+=' | cut -c1-24)
    viewer=$(openssl rand -base64 24 | tr -d '/+=' | cut -c1-24)
    kubectl -n "$NS" create secret generic sophron-admin \
      --from-literal=password="$pw" --from-literal=viewer-password="$viewer" >/dev/null
    echo "Created the dashboard logins (stored only in the sophron-admin Secret):"
    echo "  admin  / $pw"
    echo "  viewer / $viewer   (read-only)"
    echo "Restarting the collector to pick them up..."
    kubectl -n "$NS" rollout restart deploy/sophron-collector >/dev/null
    kubectl -n "$NS" rollout status deploy/sophron-collector --timeout=120s
    ;;
  start)
    "$0" stop >/dev/null 2>&1 || true
    nohup kubectl -n "$NS" port-forward svc/sophron-collector "$PORT:8080" --address 127.0.0.1 >"$STATE_DIR/dashboard.log" 2>&1 &
    echo $! >"$PID"
    sleep 2
    if ! curl -fs "http://127.0.0.1:$PORT/healthz" >/dev/null; then
      echo "The dashboard didn't come up; see $STATE_DIR/dashboard.log" >&2
      exit 1
    fi
    id=$(imds instance-id)
    echo "Dashboard: http://localhost:$PORT (on this machine)"
    if [[ -n "$id" ]]; then
      echo
      echo "From your laptop, run this, then open http://localhost:$PORT :"
      echo "  aws ssm start-session --target $id --document-name AWS-StartPortForwardingSession \\"
      echo "    --parameters '{\"portNumber\":[\"$PORT\"],\"localPortNumber\":[\"$PORT\"]}'"
    fi
    echo
    echo "Sign in as admin (password: make admin-password). Stop with: make dashboard-stop"
    ;;
  stop)
    [[ -f "$PID" ]] && kill "$(cat "$PID")" 2>/dev/null || true
    rm -f "$PID"
    echo "Dashboard port-forward stopped."
    ;;
  *)
    echo "usage: $0 password|start|stop" >&2
    exit 1
    ;;
esac
