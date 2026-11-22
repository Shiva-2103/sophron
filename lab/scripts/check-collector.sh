#!/usr/bin/env bash
# Checks the Phase 2 "done when" list and prints PASS/FAIL for each item.
# Run after `make collector-up`. Run `make lab-load` first to see token rates.
set -uo pipefail
source "$(dirname "$0")/auth.sh"

pass=0; fail=0
ok()  { printf '  \033[32mPASS\033[0m  %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; fail=$((fail + 1)); }
note() { printf '        %s\n' "$*"; }

port=18082
kubectl -n sophron-system port-forward svc/sophron-collector "$port:8080" --address 127.0.0.1 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 20); do curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break; sleep 0.25; done

echo "Phase 2 checks"

# 1. Collector pod is Ready (readiness = informers synced + a recent good cycle)
if kubectl -n sophron-system wait --for=condition=Ready pod -l 'app.kubernetes.io/name in (sophron-collector,sophron)' --timeout=5s >/dev/null 2>&1; then
  ok "collector pod Ready"
else
  bad "collector pod not Ready (see: make collector-logs)"
fi

# 2. Runs as non-root with a read-only filesystem
uid=$(kubectl -n sophron-system get deploy sophron-collector -o jsonpath='{.spec.template.spec.securityContext.runAsUser}')
ro=$(kubectl -n sophron-system get deploy sophron-collector -o jsonpath='{.spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem}')
[[ "$uid" == "65532" && "$ro" == "true" ]] && ok "runs as non-root (uid $uid), read-only root filesystem" || bad "security settings missing (uid=$uid readOnly=$ro)"

# 3. RBAC is read-only
if kubectl auth can-i list pods --as=system:serviceaccount:sophron-system:sophron-collector -A >/dev/null 2>&1 \
   && ! kubectl auth can-i delete pods --as=system:serviceaccount:sophron-system:sophron-collector -A >/dev/null 2>&1; then
  ok "RBAC: can list pods, cannot delete them"
else
  bad "RBAC is not read-only as expected"
fi

# 4. Last cycle succeeded and found the model pod
status=$(curl -fsS "${AUTH[@]}" "http://127.0.0.1:$port/api/v1/status" 2>/dev/null)
pods=$(jq -r '.pods // 0' <<<"$status" 2>/dev/null)
err=$(jq -r '.error // ""' <<<"$status" 2>/dev/null)
qerr=$(jq -r '.queryErrors // 0' <<<"$status" 2>/dev/null)
if [[ -n "$status" && -z "$err" && "$pods" -ge 1 ]]; then
  ok "last cycle found $pods model pod(s), $qerr failed queries"
else
  bad "last cycle: pods=${pods:-?} error='${err:-unreachable}'"
fi

# 5. Snapshot joins Kubernetes and Prometheus data
snap=$(curl -fsS "${AUTH[@]}" "http://127.0.0.1:$port/api/v1/snapshots/latest" 2>/dev/null | jq '[.[] | select(.namespace=="llm")][0] // empty')
if [[ -n "$snap" ]]; then
  ok "snapshot stored for $(jq -r '.namespace + "/" + .pod' <<<"$snap") (server: $(jq -r .server <<<"$snap"))"
  node=$(jq -r '.node // ""' <<<"$snap"); provider=$(jq -r '.nodeProvider // ""' <<<"$snap")
  [[ -n "$node" && "$provider" == "kind" ]] && ok "Kubernetes facts joined: node $node, provider $provider, $(jq -r .nodeCpuCores <<<"$snap") cores" \
                                            || bad "node facts missing (node='$node' provider='$provider')"
  cpu=$(jq -r '.cpuCores // empty' <<<"$snap")
  [[ -n "$cpu" ]] && ok "Prometheus metrics joined: cpu_cores=$(printf '%.2f' "$cpu")" \
                  || bad "cpu_cores missing: is Prometheus reachable from sophron-system?"
  tps=$(jq -r '.generationTokensPerSecond // empty' <<<"$snap")
  [[ -n "$tps" ]] && note "tokens/sec: $(printf '%.2f' "$tps")  (run make lab-load to raise it)"
  note "startup took: $(jq -r '.startupSeconds // "n/a"' <<<"$snap") s, restarts: $(jq -r .restarts <<<"$snap")"
else
  bad "no snapshot for the llm namespace yet (wait 30s, or check make collector-logs)"
fi

echo
echo "Result: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
