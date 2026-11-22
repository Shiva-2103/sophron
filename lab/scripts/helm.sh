#!/usr/bin/env bash
# Run the published Sophron (image + chart from ghcr.io) in the lab, in place
# of the locally built one, and back.
#
#   bash lab/scripts/helm.sh swap     remove the kubectl-applied Sophron, helm install the release
#   bash lab/scripts/helm.sh status   what is running, which version, from where
#   bash lab/scripts/helm.sh back     helm uninstall, then run: make collector-up
#
# Kept across the swap: the namespace, the admin logins (sophron-admin), the
# Slack secret (sophron-slack, with the buttons token) and the approvers list.
# History starts fresh in a new volume; the old one (sophron-data) is kept
# for "back".
#
# While the GitHub packages are private, the cluster needs a pull secret and
# Helm needs a login. Both use a GitHub token with read:packages, typed at a
# hidden prompt: it goes to a Kubernetes Secret through stdin, is never
# printed, and Helm logs out after the install. Set PUBLIC=1 once the
# packages are public to skip this.
set -euo pipefail

NS=sophron-system
RELEASE=sophron-collector          # same names as before, so the lab scripts keep working
CHART=oci://ghcr.io/shiva-2103/charts/sophron
VERSION="${VERSION:-0.1.2}"
PULL_SECRET=ghcr-pull
VALUES="$(dirname "$0")/../helm/values-lab.yaml"

imds() {
  local token
  token=$(curl -fsS -m 2 -X PUT http://169.254.169.254/latest/api/token \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60" 2>/dev/null) || return 0
  curl -fsS -m 2 -H "X-aws-ec2-metadata-token: $token" "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null || true
}

# login asks for a GitHub token once, creates the pull secret, and logs Helm in.
login() {
  local user token auth
  read -rp "GitHub user name [Shiva-2103]: " user
  user="${user:-Shiva-2103}"
  read -rsp "GitHub token with read:packages (hidden): " token
  echo
  [[ -n "$token" ]] || { echo "No token given." >&2; exit 1; }
  auth=$(printf '%s:%s' "$user" "$token" | base64 -w0)
  printf '{"auths":{"ghcr.io":{"auth":"%s"}}}' "$auth" |
    kubectl -n "$NS" create secret generic "$PULL_SECRET" --type=kubernetes.io/dockerconfigjson \
      --from-file=.dockerconfigjson=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  printf '%s' "$token" | helm registry login ghcr.io -u "$user" --password-stdin >/dev/null
  unset token auth
  echo "Pull secret $PULL_SECRET stored; Helm logged in to ghcr.io."
}

swap() {
  command -v helm >/dev/null || { echo "helm is not installed (make lab-tools)" >&2; exit 1; }
  kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS" >/dev/null

  # Keep the approvers list from the running deployment.
  local approvers
  approvers=$(kubectl -n "$NS" get deploy "$RELEASE" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SOPHRON_SLACK_APPROVERS")].value}' 2>/dev/null || true)

  local args=()
  if [[ "${PUBLIC:-0}" != "1" ]]; then
    login
    args+=(--set "image.pullSecrets={$PULL_SECRET}")
  fi
  [[ -n "$approvers" ]] && args+=(--set "slack.approvers={$approvers}")
  local ip
  ip=$(imds public-ipv4)
  [[ -n "$ip" ]] && args+=(--set "dashboardURL=http://$ip:8081")

  if ! kubectl -n "$NS" get deploy "$RELEASE" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}' 2>/dev/null | grep -q Helm; then
    echo "Removing the kubectl-applied Sophron (secrets and the old volume stay)..."
    kubectl -n "$NS" delete deploy/"$RELEASE" svc/"$RELEASE" sa/"$RELEASE" svc/sophron-dashboard \
      cm/sophron-pricing cm/sophron-policy --ignore-not-found >/dev/null
    kubectl delete clusterrole/"$RELEASE" clusterrolebinding/"$RELEASE" --ignore-not-found >/dev/null
    kubectl -n llm delete role/sophron-fixer rolebinding/sophron-fixer --ignore-not-found >/dev/null
  fi

  echo "Installing $CHART $VERSION ..."
  helm upgrade --install "$RELEASE" "$CHART" --version "$VERSION" --namespace "$NS" \
    -f "$VALUES" "${args[@]}" --wait --timeout 5m
  [[ "${PUBLIC:-0}" != "1" ]] && helm registry logout ghcr.io >/dev/null 2>&1 || true
  status
  echo
  echo "Dashboard: make dashboard. Checks: make doctor, make collector-check."
}

status() {
  if ! helm -n "$NS" status "$RELEASE" >/dev/null 2>&1; then
    echo "Sophron is not installed with Helm here (it's the kubectl-applied one, or none)."
    return 0
  fi
  helm -n "$NS" list --filter "^$RELEASE\$"
  echo
  kubectl -n "$NS" get pods -l app.kubernetes.io/instance="$RELEASE" -o wide
  echo
  echo "Image:   $(kubectl -n "$NS" get deploy "$RELEASE" -o jsonpath='{.spec.template.spec.containers[0].image}')"
  echo "Digest:  $(kubectl -n "$NS" get pods -l app.kubernetes.io/instance="$RELEASE" -o jsonpath='{.items[0].status.containerStatuses[0].imageID}')"
  echo "Version: $(kubectl -n "$NS" logs deploy/"$RELEASE" 2>/dev/null | grep -m1 -o 'version=[^ ]*' || echo unknown)"
}

back() {
  helm -n "$NS" uninstall "$RELEASE" --wait 2>/dev/null || true
  kubectl -n "$NS" delete secret "$PULL_SECRET" --ignore-not-found >/dev/null
  echo "Helm release removed. The admin and Slack secrets are kept."
  echo "Now run: make collector-up   (the locally built Sophron, with its old history)"
}

case "${1:-}" in
  swap) swap ;;
  status) status ;;
  back) back ;;
  *) echo "usage: $0 swap|status|back" >&2; exit 1 ;;
esac
