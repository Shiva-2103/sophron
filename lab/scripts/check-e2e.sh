#!/usr/bin/env bash
# The whole loop, for real: break the model server (OOM), wait for Sophron
# to find it and propose a fix, approve the fix as admin, and check that the
# problem goes away with a receipt. Larger steps are approved until it works
# (at most 5). Takes about 5 to 15 minutes. Needs: make admin-password.
set -uo pipefail
source "$(dirname "$0")/auth.sh"

if [[ ${#AUTH[@]} -eq 0 ]]; then
  echo "No admin password yet. Run: make admin-password" >&2
  exit 1
fi
port=18084
kubectl -n sophron-system port-forward svc/sophron-collector "$port:8080" --address 127.0.0.1 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 20); do curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
api() { curl -fsS "${AUTH[@]}" "http://127.0.0.1:$port/api/v1$1" "${@:2}"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; exit 1; }
wait_for() { # seconds, description, command...
  local t=$1 what=$2; shift 2
  for _ in $(seq 1 $((t / 10))); do "$@" && return 0; sleep 10; done
  bad "timed out after ${t}s waiting for: $what"
}

step "1. Break it: the OOM chaos scenario"
bash "$(dirname "$0")/chaos.sh" reset >/dev/null 2>&1
bash "$(dirname "$0")/chaos.sh" oom >/dev/null
ok "llm-server now has a 512Mi limit and a 32k context"

finding_open() { api /findings | jq -e '[.[] | select(.detector=="oom-killed" and .namespace=="llm")] | length > 0' >/dev/null; }
step "2. Sophron finds it"
wait_for 300 "an oom-killed finding" finding_open
fid=$(api /findings | jq -r '[.[] | select(.detector=="oom-killed" and .namespace=="llm")][0].id')
ok "finding #$fid: $(api /findings | jq -r "[.[] | select(.id==$fid)][0].title")"

pending() { api '/fixes?status=pending' | jq -e "[.[] | select(.findingId==$fid)] | length > 0" >/dev/null; }
resolved() { api '/findings?status=resolved&limit=20' | jq -e "[.[] | select(.id==$fid)] | length > 0" >/dev/null; }
pending_or_resolved() { pending || resolved; }
for round in 1 2 3 4 5; do
  step "3.$round Approve the fix Sophron proposes"
  wait_for 300 "a pending fix for finding #$fid (or the problem resolving)" pending_or_resolved
  if resolved; then break; fi
  fix=$(api '/fixes?status=pending' | jq -c "[.[] | select(.findingId==$fid)][0]")
  id=$(jq -r .id <<<"$fix")
  ok "fix #$id: $(jq -r .action.summary <<<"$fix")"
  out=$(api "/fixes/$id/approve" -X POST -d '{"reason":"e2e check"}' 2>&1) || bad "approve failed: $out"
  ok "approved and $(jq -r .status <<<"$out"): $(jq -r .result <<<"$out")"
  step "4.$round Wait: fixed, or a bigger step needed"
  wait_for 420 "the finding to resolve or a follow-up fix" pending_or_resolved
  if resolved; then break; fi
  echo "  still OOM after this step; Sophron proposed a bigger one"
done
resolved || bad "finding #$fid did not resolve after 5 approved steps"

step "5. The receipt"
f=$(api '/findings?status=resolved&limit=20' | jq -c "[.[] | select(.id==$fid)][0]")
echo "$f" | jq -r '"  " + .resolution.summary, (.resolution.changes[]? | "    • " + .), "  changed with: " + (.resolution.changedBy // "-"), "  approved by:  " + (.resolution.approvedBy // "-"), "  cost while down: " + ((.resolution.downCost // 0) | tostring)'
[[ $(jq -r .resolution.approvedBy <<<"$f") == "admin" ]] && ok "the receipt names the approver" || bad "no approver on the receipt"
verified=$(api '/fixes?status=verified' | jq "[.[] | select(.findingId==$fid)] | length")
[[ "$verified" -ge 1 ]] && ok "$verified fix(es) verified" || bad "no verified fix"
audit=$(api /setup | jq -r '.[] | select(.name=="Audit log") | .status')
[[ "$audit" == "ok" ]] && ok "audit log hash chain intact" || bad "audit chain: $audit"

step "6. Put the lab back"
bash "$(dirname "$0")/chaos.sh" reset >/dev/null 2>&1 && ok "chaos reset"
echo
echo "End to end: break → alert → approve → fixed → receipt. PASS"
