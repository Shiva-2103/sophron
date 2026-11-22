#!/usr/bin/env bash
# Reach the dashboard from your laptop without SSM.
#
#   bash lab/scripts/expose.sh port3000-on    dashboard on http://<public-ip>:3000 (Grafana moves aside)
#   bash lab/scripts/expose.sh port3000-off   Grafana back on 3000
#   bash lab/scripts/expose.sh tunnel         public HTTPS link via a Cloudflare quick tunnel (Ctrl+C stops it)
#
# port3000 uses the security group rule you already have for Grafana.
# Plain http on a public IP: sign-in cookies are allowed without Secure
# while it's on. Turn it off when you're done.
set -euo pipefail

imds() {
  local token
  token=$(curl -fsS -m 2 -X PUT http://169.254.169.254/latest/api/token \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60" 2>/dev/null) || return 0
  curl -fsS -m 2 -H "X-aws-ec2-metadata-token: $token" "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null || true
}

case "${1:-}" in
  port3000-on)
    # The kind cluster publishes NodePort 30000 on the machine's port 3000.
    kubectl -n monitoring patch svc monitoring-grafana --type=json \
      -p '[{"op":"replace","path":"/spec/ports/0/nodePort","value":30001}]' >/dev/null
    kubectl -n sophron-system apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Service
metadata:
  name: sophron-dashboard
  namespace: sophron-system
spec:
  type: NodePort
  selector:
$(kubectl -n sophron-system get svc sophron-collector -o go-template='{{range $k, $v := .spec.selector}}    {{$k}}: {{$v}}{{"\n"}}{{end}}')
  ports:
    - name: http
      port: 8080
      targetPort: http
      nodePort: 30000
YAML
    kubectl -n sophron-system set env deploy/sophron-collector SOPHRON_INSECURE_HTTP_COOKIES=true >/dev/null
    kubectl -n sophron-system rollout status deploy/sophron-collector --timeout=120s
    ip=$(imds public-ipv4)
    echo
    echo "Dashboard: http://${ip:-<public-ip>}:3000   (plain http; lab only)"
    echo "Sign in as admin. Password: kubectl -n sophron-system get secret sophron-admin -o jsonpath='{.data.password}' | base64 -d; echo"
    echo "When done: make dashboard-3000-off"
    ;;
  port3000-off)
    kubectl -n sophron-system delete svc sophron-dashboard --ignore-not-found >/dev/null
    kubectl -n sophron-system set env deploy/sophron-collector SOPHRON_INSECURE_HTTP_COOKIES- >/dev/null
    kubectl -n monitoring patch svc monitoring-grafana --type=json \
      -p '[{"op":"replace","path":"/spec/ports/0/nodePort","value":30000}]' >/dev/null
    kubectl -n sophron-system rollout status deploy/sophron-collector --timeout=120s
    echo "Grafana is back on port 3000; the dashboard is private again."
    ;;
  tunnel)
    if ! command -v cloudflared >/dev/null; then
      echo "Installing cloudflared..."
      arch=$(dpkg --print-architecture)
      curl -fsSLo /tmp/cloudflared.deb "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${arch}.deb"
      sudo dpkg -i /tmp/cloudflared.deb >/dev/null
    fi
    bash "$(dirname "$0")/dashboard.sh" start >/dev/null
    echo "Starting a Cloudflare quick tunnel. Look for the https://....trycloudflare.com link below."
    echo "Anyone with the link reaches the sign-in page: keep it to yourself and stop it (Ctrl+C) when done."
    echo
    cloudflared tunnel --no-autoupdate --url http://localhost:8081
    ;;
  *)
    echo "usage: $0 port3000-on|port3000-off|tunnel" >&2
    exit 1
    ;;
esac
