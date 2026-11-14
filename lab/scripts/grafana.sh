#!/usr/bin/env bash
# Grafana helpers for the lab.
#
#   bash lab/scripts/grafana.sh url      print the Grafana URL and login
#   bash lab/scripts/grafana.sh allow    open port 3000 in the EC2 security group for your IP
#   bash lab/scripts/grafana.sh revoke   close it again
#
# "Your IP" is the address you are SSH'd in from (taken from $SSH_CLIENT),
# or set MY_IP=1.2.3.4 yourself.
set -euo pipefail

CLUSTER="${CLUSTER:-sophron-lab}"
STATE_DIR=".lab"
PORT=3000

# EC2 instance metadata (IMDSv2). Empty output when not on EC2.
imds() {
  local token
  token=$(curl -fsS -m 2 -X PUT http://169.254.169.254/latest/api/token \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60" 2>/dev/null) || return 0
  curl -fsS -m 2 -H "X-aws-ec2-metadata-token: $token" "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null || true
}

url() {
  if ! docker port "$CLUSTER-control-plane" 30000/tcp 2>/dev/null | grep -q ":$PORT"; then
    echo "Grafana is not published on port $PORT in this cluster."
    echo "It was created before the port mapping existed. Recreate it:"
    echo "  make lab-down && make lab-up"
    return 1
  fi
  local ip
  ip=$(imds public-ipv4)
  echo "Grafana"
  echo "  on this machine:  http://localhost:$PORT"
  if [[ -n "$ip" ]]; then
    echo "  from your laptop: http://$ip:$PORT   (security group must allow your IP: make lab-grafana-allow-me)"
  fi
  echo "  user:             admin"
  echo "  password:         $(cat "$STATE_DIR/grafana-password" 2>/dev/null || echo 'unknown (run make lab-up)')"
}

security_group() {
  local id region sg
  id=$(imds instance-id)
  region=$(imds placement/region)
  if [[ -z "$id" || -z "$region" ]]; then
    echo "Not running on EC2 (no instance metadata). Open port $PORT in your firewall yourself." >&2
    return 1
  fi
  sg=$(aws ec2 describe-instances --region "$region" --instance-ids "$id" \
    --query 'Reservations[0].Instances[0].SecurityGroups[0].GroupId' --output text)
  echo "$region $sg"
}

my_ip() {
  local ip="${MY_IP:-${SSH_CLIENT%% *}}"
  if [[ -z "$ip" ]]; then
    echo "Can't tell your IP. Run this over SSH, or set MY_IP=your.public.ip" >&2
    return 1
  fi
  if [[ "$ip" == *:* ]]; then
    echo "Your IP ($ip) is IPv6. Add the rule in the EC2 console instead." >&2
    return 1
  fi
  echo "$ip"
}

allow() {
  local ip loc region sg out
  ip=$(my_ip)
  loc=$(security_group)
  read -r region sg <<<"$loc"
  if out=$(aws ec2 authorize-security-group-ingress --region "$region" --group-id "$sg" \
      --protocol tcp --port "$PORT" --cidr "$ip/32" 2>&1); then
    echo "Opened port $PORT on $sg for $ip/32 only."
  elif grep -q 'InvalidPermission.Duplicate' <<<"$out"; then
    echo "Port $PORT is already open on $sg for $ip/32."
  else
    echo "$out" >&2
    return 1
  fi
}

revoke() {
  local ip loc region sg
  ip=$(my_ip)
  loc=$(security_group)
  read -r region sg <<<"$loc"
  aws ec2 revoke-security-group-ingress --region "$region" --group-id "$sg" \
    --protocol tcp --port "$PORT" --cidr "$ip/32" >/dev/null
  echo "Closed port $PORT on $sg for $ip/32."
}

case "${1:-}" in
  url) url ;;
  allow) allow ;;
  revoke) revoke ;;
  *) echo "usage: $0 url|allow|revoke" >&2; exit 1 ;;
esac
