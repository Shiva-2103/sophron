#!/usr/bin/env bash
# Prepare a fresh Ubuntu machine (22.04 / 24.04) to run the Sophron lab.
# Installs: Docker, kind, kubectl, helm, make, jq, git.
# Safe to run more than once: it skips tools that are already installed.
#
# Usage:  sudo bash lab/scripts/bootstrap-ubuntu.sh
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Run with sudo: sudo bash $0" >&2
  exit 1
fi

# The user who ran sudo (e.g. "ubuntu"), so we can give them Docker access.
TARGET_USER="${SUDO_USER:-ubuntu}"
ARCH="$(dpkg --print-architecture)"   # amd64 on t3 instances

step() { printf '\n==> %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

step "Base packages"
apt-get update -y
apt-get install -y ca-certificates curl git jq make

step "Docker"
if have docker; then
  echo "Docker already installed: $(docker --version)"
else
  # Docker's official install script for Ubuntu.
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker
usermod -aG docker "$TARGET_USER"

step "kubectl (latest stable)"
if have kubectl; then
  echo "kubectl already installed: $(kubectl version --client | head -1)"
else
  KUBECTL_VERSION="$(curl -fsSL https://dl.k8s.io/release/stable.txt)"
  curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl"
  chmod +x /usr/local/bin/kubectl
fi

step "kind (latest release)"
if have kind; then
  echo "kind already installed: $(kind version)"
else
  KIND_VERSION="${KIND_VERSION:-$(curl -fsSL https://api.github.com/repos/kubernetes-sigs/kind/releases/latest | jq -r .tag_name)}"
  curl -fsSLo /usr/local/bin/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${ARCH}"
  chmod +x /usr/local/bin/kind
fi

step "helm"
if have helm; then
  echo "helm already installed: $(helm version --short)"
else
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

step "Kernel settings for multi-node kind"
# Each kind node runs many watchers; the Ubuntu defaults are too low and
# cause "too many open files" errors in pods.
cat > /etc/sysctl.d/99-sophron-kind.conf <<'EOF'
fs.inotify.max_user_watches = 524288
fs.inotify.max_user_instances = 512
EOF
sysctl --system >/dev/null

step "Done"
docker --version
kubectl version --client | head -1
kind version
helm version --short
echo
echo "Log out and back in (or run: newgrp docker) so '$TARGET_USER' can use Docker without sudo."
