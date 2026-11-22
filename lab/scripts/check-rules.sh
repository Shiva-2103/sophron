#!/usr/bin/env bash
# Phase 3 end-to-end check: break the lab one scenario at a time and verify the
# right detector reports it, then reset and verify the findings resolve.
#
# Usage: bash lab/scripts/check-rules.sh [scenario ...]   (default: all)
# Takes about 25 minutes for all six.
set -uo pipefail
source "$(dirname "$0")/auth.sh"

ALL="oom crash gpu rollout slow-start queue"
SCENARIOS="${*:-$ALL}"
PORT=18083

# scenario -> detector, title pattern, timeout (seconds)
declare -A DET=([oom]=oom-killed [crash]=container-failing [gpu]=pending-unschedulable
  [rollout]=rollout-stuck [slow-start]=slow-cold-start [queue]=queue-backlog)
declare -A PAT=([oom]="OOMKilled" [crash]="keeps crashing" [gpu]="needs a GPU"
  [rollout]="Rollout of" [slow-start]="still starting|took .* to start" [queue]="queuing")
declare -A TMO=([oom]=300 [crash]=300 [gpu]=360 [rollout]=420 [slow-start]=360 [queue]=420)

pass=0; fail=0
ok()  { printf '  \033[32mPASS\033[0m  %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; fail=$((fail + 1)); }

pf=""
connect() {
  [[ -n "$pf" ]] && kill "$pf" 2>/dev/null
  kubectl -n sophron-system port-forward svc/sophron-collector "$PORT:8080" --address 127.0.0.1 >/dev/null 2>&1 &
  pf=$!
  for _ in $(seq 1 20); do curl -fs "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && return; sleep 0.25; done
}
trap 'kill $pf 2>/dev/null || true' EXIT

api() { # path -> json; reconnects once if the port-forward died
  curl -fsS "${AUTH[@]}" "http://127.0.0.1:$PORT$1" 2>/dev/null || { connect; curl -fsS "${AUTH[@]}" "http://127.0.0.1:$PORT$1"; }
}

# wait_for detector pattern since timeout -> prints the finding title, or nothing
wait_for() {
  local det=$1 pat=$2 since=$3 timeout=$4 start=$SECONDS
  while (( SECONDS - start < timeout )); do
    local hit
    hit=$(api /api/v1/findings | jq -r --arg d "$det" --arg p "$pat" --arg s "$since" \
      '[.[] | select(.detector == $d and (.title | test($p)) and ((.lastSeen | sub("\\.[0-9]+"; "")) >= $s))][0] | select(.) | "\(.severity)|\(.title)"' 2>/dev/null)
    if [[ -n "$hit" ]]; then
      echo "$hit|$(( SECONDS - start ))"
      return
    fi
    sleep 15
  done
}

if ! kubectl -n sophron-system get deploy sophron-collector >/dev/null 2>&1; then
  echo "The collector isn't deployed. Run: make collector-up" >&2
  exit 1
fi
connect
if ! api /api/v1/detectors | jq -e 'length > 0' >/dev/null 2>&1; then
  echo "The collector has no rules engine. Rebuild it from this branch: make collector-up" >&2
  exit 1
fi

echo "Phase 3 checks: $SCENARIOS"
bash lab/scripts/chaos.sh reset >/dev/null
for s in $SCENARIOS; do
  [[ -n "${DET[$s]:-}" ]] || { bad "unknown scenario $s"; continue; }
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  echo
  bash lab/scripts/chaos.sh "$s" | sed 's/^/  /'
  res=$(wait_for "${DET[$s]}" "${PAT[$s]}" "$since" "${TMO[$s]}")
  if [[ -n "$res" ]]; then
    IFS='|' read -r sev title took <<<"$res"
    ok "$s -> ${DET[$s]} ($sev) after ${took}s: $title"
  else
    bad "$s -> ${DET[$s]} not reported within ${TMO[$s]}s (see: make findings, make collector-logs)"
  fi
  bash lab/scripts/chaos.sh reset >/dev/null
done

echo
echo "  waiting for findings to resolve after the last reset (up to 5 min)"
start=$SECONDS; left=""
while (( SECONDS - start < 300 )); do
  # Info findings (a slow start that already finished) may stay open a while; that's by design.
  left=$(api /api/v1/findings | jq -r '[.[] | select(.severity != "info")] | length' 2>/dev/null)
  [[ "$left" == "0" ]] && break
  sleep 15
done
[[ "$left" == "0" ]] && ok "all warning and critical findings resolved after reset" \
                     || bad "$left findings still open after reset (make findings)"

echo
echo "Result: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
