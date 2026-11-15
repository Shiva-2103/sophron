#!/usr/bin/env bash
# Checks the Phase 4 "done when" list and prints PASS/FAIL for each item.
# Run after `make collector-up`. It sends some load first, so tokens exist.
set -uo pipefail

pass=0; fail=0
ok()  { printf '  \033[32mPASS\033[0m  %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; fail=$((fail + 1)); }
note() { printf '        %s\n' "$*"; }

port=18083
kubectl -n sophron-system port-forward svc/sophron-collector "$port:8080" --address 127.0.0.1 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 20); do curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
api() { curl -fsS "http://127.0.0.1:$port$1" 2>/dev/null; }

echo "Phase 4 checks"
echo "  (sending load for ~2 minutes so there are tokens to price...)"
kubectl -n llm delete job loadgen --ignore-not-found >/dev/null 2>&1
kubectl apply -f lab/loadgen/job.yaml >/dev/null
kubectl -n llm wait --for=condition=complete job/loadgen --timeout=600s >/dev/null 2>&1 || true
sleep 35 # one more collection cycle

# 1. Every node has a price from the price file (not a guess)
nodes=$(api /api/v1/cost/nodes)
n=$(jq 'length' <<<"$nodes" 2>/dev/null || echo 0)
est=$(jq '[.[] | select(.estimate)] | length' <<<"$nodes" 2>/dev/null || echo 0)
if [[ "$n" -ge 1 && "$est" == "0" ]]; then
  ok "$n nodes priced, sources: $(jq -r '[.[].source] | unique | join(", ")' <<<"$nodes")"
else
  bad "nodes priced: $n, estimated: $est (is the sophron-pricing ConfigMap mounted?)"
fi

# 2. Model + other + idle adds up to each node's price
gap=$(jq '[.[] | ((.hourly - .modelCostPerHour - .otherCostPerHour - .idleCostPerHour) | if . < 0 then -. else . end)] | max // 1' <<<"$nodes")
if jq -e --argjson g "$gap" -n '$g < 0.0001' >/dev/null 2>&1; then
  ok "model + other pods + idle = node price on every node"
else
  bad "split doesn't add up (largest gap \$$gap/h)"
fi

# 3. The lab model has a cost per hour and per 1M tokens
m=$(api '/api/v1/cost/models?window=15m' | jq '[.[] | select(.namespace=="llm")][0] // empty')
if [[ -n "$m" ]]; then
  cph=$(jq -r '.costPerHour // empty' <<<"$m")
  [[ -n "$cph" ]] && ok "llm/$(jq -r .workload <<<"$m") costs \$$(printf '%.4f' "$cph")/hour (\$$(printf '%.2f' "$(jq -r .monthForecast <<<"$m")")/month)" \
                  || bad "model has no cost per hour"
  c1m=$(jq -r '.costPer1MOutputTokens // empty' <<<"$m")
  if [[ -n "$c1m" ]]; then
    ok "cost per 1M output tokens: \$$(printf '%.3f' "$c1m") on average (idle time included)"
    busy=$(jq -r '.costPer1MOutputTokensBusy // empty' <<<"$m")
    [[ -n "$busy" ]] && note "while busy: \$$(printf '%.3f' "$busy") per 1M; busy $(jq -n "$(jq -r '.busyShare // 0' <<<"$m")*100|floor")% of the window"
  else
    bad "no cost per 1M tokens (no token metrics in the window?)"
  fi
  cap=$(jq -r '.capacityUsed // empty' <<<"$m")
  [[ -n "$cap" ]] && note "capacity used now: $(jq -n "$cap*100|floor")% of the peak seen"
  jq -r '.apiComparison[]? | "        vs \(.api): API \(.apiCostPerMonth|floor)/month, self-hosted \(.selfCostPerMonth|floor)/month"' <<<"$m"
  jq -r '.notes[]? | "        note: " + .' <<<"$m"
else
  bad "no cost report for the llm namespace"
fi

# 4. Summary
s=$(api /api/v1/cost/summary)
if [[ -n "$s" ]]; then
  ok "cluster: \$$(printf '%.4f' "$(jq -r .spendPerHour <<<"$s")")/hour, idle $(jq -n "$(jq -r .idleShare <<<"$s")*100|floor")%"
else
  bad "no cost summary"
fi

echo
echo "  $pass passed, $fail failed"
[[ $fail -eq 0 ]]
