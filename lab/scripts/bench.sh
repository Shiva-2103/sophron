#!/usr/bin/env bash
# Benchmark the model server: run a fixed load and measure it the same way
# every time, so different settings (threads, CPU limits, models) compare fairly.
#
# Usage: bash lab/scripts/bench.sh [requests-per-worker]
# Results are appended to .lab/bench.csv.
set -euo pipefail

REQUESTS="${1:-25}"
WORKERS=4
STATE=".lab"
mkdir -p "$STATE"

prom_svc=$(kubectl -n monitoring get svc -l app=kube-prometheus-stack-prometheus -o jsonpath='{.items[0].metadata.name}')
kubectl -n monitoring port-forward "svc/$prom_svc" 19091:9090 --address 127.0.0.1 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
sleep 2

q() { # promql -> first value, or 0
  curl -fsS -m 20 --get http://127.0.0.1:19091/api/v1/query --data-urlencode "query=$1" \
    | jq -r '.data.result[0].value[1] // "0"'
}

threads=$(kubectl -n llm get deploy llm-server -o jsonpath='{.spec.template.spec.containers[0].args[0]}' | grep -oE -- '--threads [0-9]+' | awk '{print $2}')
cpu_limit=$(kubectl -n llm get deploy llm-server -o jsonpath='{.spec.template.spec.containers[0].resources.limits.cpu}')
node=$(kubectl -n llm get pod -l app.kubernetes.io/name=llm-server -o jsonpath='{.items[0].spec.nodeName}')
echo "==> Benchmark: threads=${threads:-?} cpu_limit=$cpu_limit, $WORKERS workers x $REQUESTS requests"

# Wait until the model is idle, so earlier load doesn't leak into this run.
for _ in $(seq 1 40); do
  busy=$(q 'sum(llamacpp:requests_processing{namespace="llm"})')
  [[ "${busy%.*}" == "0" ]] && break
  sleep 3
done

before=$(q 'sum(llamacpp:tokens_predicted_total{namespace="llm"})')
kubectl -n llm delete job loadgen --ignore-not-found >/dev/null
sed "s/value: \"25\"/value: \"$REQUESTS\"/" lab/loadgen/job.yaml | kubectl apply -f - >/dev/null
echo "==> Running load (this takes a few minutes)"
kubectl -n llm wait --for=condition=complete job/loadgen --timeout=30m >/dev/null

start=$(kubectl -n llm get job loadgen -o jsonpath='{.status.startTime}')
end=$(kubectl -n llm get job loadgen -o jsonpath='{.status.completionTime}')
secs=$(( $(date -d "$end" +%s) - $(date -d "$start" +%s) ))
ok=$(kubectl -n llm logs -l job-name=loadgen --tail=1 | grep -oE 'finished: [0-9]+' | awk '{s+=$2} END {print s+0}')

# Let Prometheus scrape the final counter value (scrape interval 15s).
sleep 20
after=$(q 'sum(llamacpp:tokens_predicted_total{namespace="llm"})')
win="${secs}s"
peak=$(q "max_over_time(sum(rate(llamacpp:tokens_predicted_total{namespace=\"llm\"}[1m]))[$win:15s])")
cpu=$(q "avg_over_time(sum(rate(container_cpu_usage_seconds_total{namespace=\"llm\",container=\"llama-server\"}[1m]))[$win:15s])")
thr=$(q "avg_over_time((sum(rate(container_cpu_cfs_throttled_periods_total{namespace=\"llm\",container=\"llama-server\"}[1m])) / sum(rate(container_cpu_cfs_periods_total{namespace=\"llm\",container=\"llama-server\"}[1m])))[$win:15s])")

tokens=$(awk -v a="$after" -v b="$before" 'BEGIN{printf "%d", a-b}')
avg=$(awk -v t="$tokens" -v s="$secs" 'BEGIN{printf "%.1f", (s>0)?t/s:0}')
per_req=$(awk -v s="$secs" -v r="$REQUESTS" 'BEGIN{printf "%.1f", (r>0)?s/r:0}')

echo
printf '%-26s %s\n' "threads / CPU limit" "${threads:-?} / $cpu_limit"
printf '%-26s %s of %s\n' "requests succeeded" "$ok" "$((WORKERS * REQUESTS))"
printf '%-26s %s s\n' "total time" "$secs"
printf '%-26s %s s per request (per worker)\n' "latency" "$per_req"
printf '%-26s %s\n' "tokens generated" "$tokens"
printf '%-26s %s\n' "average tokens/sec" "$avg"
printf '%-26s %.1f\n' "peak tokens/sec (1m rate)" "$peak"
printf '%-26s %.2f cores\n' "average CPU used" "$cpu"
printf '%-26s %.1f %%\n' "average throttled" "$(awk -v t="$thr" 'BEGIN{print t*100}')"

csv="$STATE/bench.csv"
[[ -f "$csv" ]] || echo "time,node,threads,cpu_limit,requests,succeeded,seconds,tokens,avg_tps,peak_tps,cpu_cores,throttled_ratio" > "$csv"
echo "$(date -u +%FT%TZ),$node,${threads:-},$cpu_limit,$((WORKERS * REQUESTS)),$ok,$secs,$tokens,$avg,$peak,$cpu,$thr" >> "$csv"
echo
echo "Saved to $csv. Compare runs with: column -s, -t < $csv"
