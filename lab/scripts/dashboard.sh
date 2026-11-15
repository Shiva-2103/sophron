#!/usr/bin/env bash
# The Sophron dashboard in the lab.
#
#   bash lab/scripts/dashboard.sh password   create the admin password (once), print it
#   bash lab/scripts/dashboard.sh start      serve the dashboard on port 8081 of every interface
#   BIND=127.0.0.1 bash lab/scripts/dashboard.sh start   this machine only (use with SSM)
#   bash lab/scripts/dashboard.sh stop
#
# Public: open 8081 to your IP only in the security group, then
# http://<public-ip>:8081. Plain http, so lab only.
# Private: BIND=127.0.0.1, then forward through SSM from your laptop:
#   aws ssm start-session --target <instance-id> --document-name AWS-StartPortForwardingSession \
#     --parameters '{"portNumber":["8081"],"localPortNumber":["8081"]}'
set -euo pipefail

NS=sophron-system
PORT="${PORT:-8081}"
BIND="${BIND:-0.0.0.0}"
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
    # Plain http on a public IP: the browser drops Secure cookies, so allow
    # them without Secure (lab only). Skipped for BIND=127.0.0.1 (SSM use).
    if [[ "$BIND" != "127.0.0.1" ]]; then
      cur=$(kubectl -n "$NS" get deploy/sophron-collector \
        -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SOPHRON_INSECURE_HTTP_COOKIES")].value}')
      if [[ "$cur" != "true" ]]; then
        echo "Allowing sign-in over plain http (lab only), restarting the collector..."
        kubectl -n "$NS" set env deploy/sophron-collector SOPHRON_INSECURE_HTTP_COOKIES=true >/dev/null
        kubectl -n "$NS" rollout status deploy/sophron-collector --timeout=120s >/dev/null
      fi
    fi
    # port-forward exits whenever the collector pod restarts (an upgrade, a
    # Slack change), so keep it running in a loop.
    nohup bash -c "while true; do
        kubectl -n $NS port-forward svc/sophron-collector $PORT:8080 --address $BIND
        sleep 2
      done" >"$STATE_DIR/dashboard.log" 2>&1 &
    echo $! >"$PID"
    for _ in $(seq 1 15); do
      curl -fs "http://127.0.0.1:$PORT/healthz" >/dev/null && break
      sleep 1
    done
    if ! curl -fs "http://127.0.0.1:$PORT/healthz" >/dev/null; then
      echo "The dashboard didn't come up; see $STATE_DIR/dashboard.log" >&2
      exit 1
    fi
    if [[ "$BIND" == "127.0.0.1" ]]; then
      echo "Dashboard: http://localhost:$PORT (this machine only)"
      id=$(imds instance-id)
      if [[ -n "$id" ]]; then
        echo
        echo "From your laptop, run this, then open http://localhost:$PORT :"
        echo "  aws ssm start-session --target $id --document-name AWS-StartPortForwardingSession \\"
        echo "    --parameters '{\"portNumber\":[\"$PORT\"],\"localPortNumber\":[\"$PORT\"]}'"
      fi
    else
      ip=$(imds public-ipv4)
      [[ -n "$ip" ]] || ip="<public-ip>"
      echo "Dashboard: http://$ip:$PORT"
      echo
      echo "Open port $PORT to your IP only, in the instance's security group."
      echo "This is plain http: fine for the lab, not for real use."
    fi
    echo
    echo "Sign in as admin (password: make admin-password). Stop with: make dashboard-stop"
    ;;
  stop)
    if [[ -f "$PID" ]]; then
      pid=$(cat "$PID")
      pkill -P "$pid" 2>/dev/null || true   # the kubectl port-forward
      kill "$pid" 2>/dev/null || true       # the restart loop
    fi
    rm -f "$PID"
    echo "Dashboard stopped."
    ;;
  *)
    echo "usage: $0 password|start|stop" >&2
    exit 1
    ;;
esac
