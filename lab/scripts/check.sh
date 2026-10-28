#!/usr/bin/env bash
# Checks the Phase 1 "done when" list and prints PASS/FAIL for each item.
# Run after `make lab-up`, and again after `make lab-load` to see metrics move.
set -uo pipefail

pass=0; fail=0
ok()  { printf '  \033[32mPASS\033[0m  %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; fail=$((fail + 1)); }

# Run a command with a temporary port-forward, then clean it up.
with_forward() { # namespace svc localport remoteport -- command...
  local ns=$1 svc=$2 lport=$3 rport=$4; shift 5
  kubectl -n "$ns" port-forward "svc/$svc" "$lport:$rport" --address 127.0.0.1 >/dev/null 2>&1 &
  local pf=$!
  sleep 2
  "$@"; local rc=$?
  kill "$pf" 2>/dev/null; wait "$pf" 2>/dev/null
  return $rc
}

echo "Phase 1 checks"

# 1. Cluster: 3 nodes, all Ready
ready=$(kubectl get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l)
[[ "$ready" -eq 3 ]] && ok "3 nodes Ready" || bad "expected 3 Ready nodes, found $ready"

# 2. Model server pod Ready on the LLM node
node=$(kubectl -n llm get pod -l app.kubernetes.io/name=llm-server \
  -o jsonpath='{.items[0].spec.nodeName}' 2>/dev/null)
role=$(kubectl get node "$node" -o jsonpath='{.metadata.labels.sophron\.dev/role}' 2>/dev/null)
if kubectl -n llm wait --for=condition=Ready pod -l app.kubernetes.io/name=llm-server --timeout=5s >/dev/null 2>&1; then
  [[ "$role" == "llm" ]] && ok "model server Ready on the llm node ($node)" \
                         || bad "model server Ready but on node '$node' (role '$role'), expected role llm"
else
  bad "model server not Ready (see: kubectl -n llm describe pod -l app.kubernetes.io/name=llm-server)"
fi

# 3. Model answers a chat request
answer() {
  curl -fsS -m 120 http://127.0.0.1:18080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"qwen2.5-1.5b-instruct","max_tokens":20,"messages":[{"role":"user","content":"Say hello in five words."}]}' \
    | jq -r '.choices[0].message.content'
}
reply=$(with_forward llm llm-server 18080 8080 -- answer 2>/dev/null)
[[ -n "$reply" && "$reply" != "null" ]] && ok "model answers: \"${reply//$'\n'/ }\"" || bad "model did not answer a chat request"

# 4. Prometheus is scraping the model's metrics
prom=$(kubectl -n monitoring get svc -l app=kube-prometheus-stack-prometheus -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
query() { # promql -> first value or empty
  curl -fsS -m 10 --get http://127.0.0.1:19090/api/v1/query --data-urlencode "query=$1" \
    | jq -r '.data.result[0].value[1] // empty'
}
check_metrics() {
  local up tokens rate
  up=$(query 'up{namespace="llm"}')
  [[ "$up" == "1" ]] && ok "Prometheus scrapes the model server (up=1)" \
                     || bad "Prometheus is not scraping the model server yet (up='${up:-none}'); wait 30s and retry"
  tokens=$(query 'sum(llamacpp:tokens_predicted_total)')
  [[ -n "$tokens" ]] && ok "metric llamacpp:tokens_predicted_total = $tokens" \
                     || bad "no llamacpp:tokens_predicted_total metric yet"
  rate=$(query 'sum(rate(llamacpp:tokens_predicted_total[2m]))')
  if [[ -n "$rate" ]]; then
    printf '        tokens per second (last 2 min): %.2f  (run make lab-load to raise it)\n' "$rate"
  fi
}
if [[ -n "$prom" ]]; then
  with_forward monitoring "$prom" 19090 9090 -- check_metrics
else
  bad "Prometheus service not found in namespace monitoring"
fi

echo
echo "Result: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
