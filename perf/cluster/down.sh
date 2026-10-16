#!/usr/bin/env bash
# Deletes the perf clusters. Usage: perf/cluster/down.sh [name...]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
for name in "${@:-k9s-s k9s-m k9s-l}"; do
  for n in $name; do
    kwokctl delete cluster --name "$n" --kubeconfig "$here/../.kube/$n.yaml" || true
  done
done
