#!/usr/bin/env bash
# Call the collector's HTTP API through a temporary port-forward.
# Usage: bash lab/scripts/api.sh /api/v1/snapshots/latest
set -euo pipefail
source "$(dirname "$0")/auth.sh"

path="${1:?usage: $0 /api/path}"
port=18081

kubectl -n sophron-system port-forward svc/sophron-collector "$port:8080" --address 127.0.0.1 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT

for _ in $(seq 1 20); do
  curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break
  sleep 0.25
done

curl -fsS "${AUTH[@]}" "http://127.0.0.1:$port$path" | jq .
