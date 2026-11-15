#!/usr/bin/env bash
# Slack alerts for the collector.
#   secret  ask for the webhook URL (hidden), store it as a Secret, restart the collector
#   test    send a sample alert from inside the collector pod
#   off     delete the Secret and restart (alerts stop)
#
# The webhook URL is a password: anyone with it can post to your channel.
# It is never echoed, never put on a command line, and never written to disk.
set -euo pipefail

ns=sophron-system
secret=sophron-slack
deploy=deploy/sophron-collector

restart() {
  kubectl -n "$ns" rollout restart "$deploy" >/dev/null
  kubectl -n "$ns" rollout status "$deploy" --timeout=120s
}

case "${1:-}" in
  secret)
    read -rsp "Paste the Slack webhook URL (input hidden), then press Enter: " url
    echo
    url="${url//[[:space:]]/}"
    if [[ "$url" != https://hooks.slack.com/services/* ]]; then
      echo "That doesn't look like a Slack webhook URL (https://hooks.slack.com/services/...). Nothing saved." >&2
      exit 1
    fi
    # printf is a shell builtin, so the URL never appears in the process list.
    printf '%s' "$url" | kubectl -n "$ns" create secret generic "$secret" \
      --from-file=webhook-url=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    unset url
    echo "Secret $ns/$secret saved. Restarting the collector to pick it up..."
    restart
    kubectl -n "$ns" logs "$deploy" | grep -o '"msg":"slack alerts [a-z]*[^"]*"' | tail -1 || true
    echo "Next: make slack-test"
    ;;
  test)
    if ! kubectl -n "$ns" get secret "$secret" >/dev/null 2>&1; then
      echo "No Slack Secret yet. Run: make slack-secret" >&2
      exit 1
    fi
    # The pod already has the URL in its environment, so we never handle it here.
    kubectl -n "$ns" exec "$deploy" -- /sophron-collector --send-test-alert
    ;;
  off)
    kubectl -n "$ns" delete secret "$secret" --ignore-not-found
    restart
    echo "Slack alerts are off."
    ;;
  *)
    echo "usage: $0 secret|test|off" >&2
    exit 1
    ;;
esac
