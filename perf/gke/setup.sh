#!/usr/bin/env bash
# Runs inside a perf/gke/pod.yaml pod: installs kubectl + kwokctl and creates
# one KWOK cluster with the binary runtime.
# Usage (in pod, from /work/perf): gke/setup.sh <size>   size: s|m|l
set -euo pipefail
trap 'echo "setup failed at line $LINENO" >&2' ERR

readonly k8s_version=v1.36.1
readonly kwok_version=v0.8.0

size=$1
case "$size" in
  s) pods=500 ;;
  m) pods=5000 ;;
  l) pods=20000 ;;
  *) echo "size must be s, m or l" >&2; exit 1 ;;
esac

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates procps >/dev/null

install_bin() {
  local name=$1 url=$2
  [ -x "/usr/local/bin/$name" ] && return
  curl -fsSL -o "/usr/local/bin/$name" "$url"
  chmod +x "/usr/local/bin/$name"
}

install_bin kubectl "https://dl.k8s.io/release/$k8s_version/bin/linux/amd64/kubectl"
install_bin kwokctl "https://github.com/kubernetes-sigs/kwok/releases/download/$kwok_version/kwokctl-linux-amd64"

# Keep the kwok control plane from picking up the host pod's in-cluster config.
unset KUBERNETES_SERVICE_HOST KUBERNETES_SERVICE_PORT

here=$(cd "$(dirname "$0")/.." && pwd)
KWOK_RUNTIME=binary KWOK_KUBE_VERSION=$k8s_version \
  "$here/cluster/up.sh" "k9s-$size" "$pods"
