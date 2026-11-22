#!/usr/bin/env bash
# Break the lab's model server on purpose, one realistic failure at a time,
# so we can watch Sophron's detectors catch it.
#
#   bash lab/scripts/chaos.sh list          show the scenarios
#   bash lab/scripts/chaos.sh <scenario>    break it
#   bash lab/scripts/chaos.sh reset         put everything back
set -euo pipefail

NS=llm
DEPLOY=llm-server
MANIFEST=lab/llm/model-server.yaml
C0=/spec/template/spec/containers/0
I0=/spec/template/spec/initContainers/0

# name | detector that should catch it | how long until it fires | what breaks
SCENARIOS=$(cat <<'EOF'
oom|oom-killed|~2 min|Context window too big for the memory limit (ctx 32768, limit 512Mi)
crash|container-failing|~2 min|Model file path is wrong, so the server exits at start
gpu|pending-unschedulable|~3 min|Asks for a GPU the cluster doesn't have
rollout|rollout-stuck|~4 min|Rolling update needs room for old and new pods at once
slow-start|slow-cold-start|~3 min|Model "download" takes 210s before the server starts
queue|queue-backlog|~3 min|Only 1 serving slot while 4 clients send requests
EOF
)

patch_json() { kubectl -n "$NS" patch deploy "$DEPLOY" --type=json -p "$1" >/dev/null; }

replace() { # json-pointer value
  patch_json "$(jq -cn --arg p "$1" --arg v "$2" '[{op:"replace",path:$p,value:$v}]')"
}

current() { kubectl -n "$NS" get deploy "$DEPLOY" -o jsonpath="{$1}"; }

server_args() { current '.spec.template.spec.containers[0].args[0]'; }

list() {
  printf '%-11s %-22s %-7s %s\n' SCENARIO "SHOULD FIRE" TIME "WHAT BREAKS"
  while IFS='|' read -r n d t w; do printf '%-11s %-22s %-7s %s\n' "$n" "$d" "$t" "$w"; done <<<"$SCENARIOS"
}

reset() {
  echo "==> Resetting the model server"
  kubectl -n "$NS" delete job loadgen --ignore-not-found >/dev/null
  # kubectl apply can't remove fields it never set, so undo those first.
  patch_json '[{"op":"replace","path":"/spec/strategy","value":{"type":"Recreate"}}]'
  kubectl -n "$NS" patch deploy "$DEPLOY" --type=json \
    -p '[{"op":"remove","path":"'"$C0"'/resources/limits/nvidia.com~1gpu"}]' >/dev/null 2>&1 || true
  kubectl apply -f "$MANIFEST" >/dev/null
  kubectl -n "$NS" rollout status deploy/"$DEPLOY" --timeout=10m
}

scenario() {
  local s=$1
  echo "==> Chaos: $s"
  case "$s" in
    oom)
      # llama.cpp's KV cache grows with --ctx-size. 32768 tokens of KV cache
      # for this model is ~900 MiB, far over a 512 MiB limit.
      replace "$C0/args/0" "$(server_args | sed -E 's/--ctx-size [0-9]+/--ctx-size 32768/')"
      replace "$C0/resources/requests/memory" 512Mi
      replace "$C0/resources/limits/memory" 512Mi
      ;;
    crash)
      replace "$C0/args/0" "$(server_args | sed -E 's#-m "[^"]*"#-m "/models/missing-model.gguf"#')"
      ;;
    gpu)
      patch_json '[{"op":"add","path":"'"$C0"'/resources/limits/nvidia.com~1gpu","value":"1"}]'
      ;;
    rollout)
      # Our Phase 1 lesson, on purpose: surge a second 4-CPU pod onto a node
      # that only has room for one.
      patch_json '[{"op":"replace","path":"/spec/strategy","value":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":1,"maxUnavailable":0}}}]'
      kubectl -n "$NS" rollout restart deploy/"$DEPLOY" >/dev/null
      ;;
    slow-start)
      replace "$I0/args/0" "$(printf 'echo "chaos: simulating a slow model download"; sleep 210\n%s' "$(current '.spec.template.spec.initContainers[0].args[0]')")"
      ;;
    queue)
      replace "$C0/args/0" "$(server_args | sed -E 's/--parallel [0-9]+/--parallel 1/')"
      kubectl -n "$NS" rollout status deploy/"$DEPLOY" --timeout=10m >/dev/null
      kubectl -n "$NS" delete job loadgen --ignore-not-found >/dev/null
      kubectl apply -f lab/loadgen/job.yaml >/dev/null
      echo "    load generator started (4 clients)"
      ;;
    *)
      echo "unknown scenario: $s" >&2
      list >&2
      exit 1
      ;;
  esac
  local d t
  d=$(awk -F'|' -v s="$s" '$1==s {print $2}' <<<"$SCENARIOS")
  t=$(awk -F'|' -v s="$s" '$1==s {print $3}' <<<"$SCENARIOS")
  echo "    expect detector '$d' within $t. Watch: make findings"
  echo "    undo with: make chaos-reset"
}

case "${1:-}" in
  list) list ;;
  reset) reset ;;
  "") list; exit 1 ;;
  *) scenario "$1" ;;
esac
