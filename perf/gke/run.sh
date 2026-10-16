#!/usr/bin/env bash
# Runs the harness in a runner pod, detached. Results land in /work/results.
# Usage (in pod): gke/run.sh <size> <bins> <labels> [extra harness flags...]
#   bins/labels: comma-separated names under /work/linux (k9s-02,k9s-03 ...).
set -euo pipefail
trap 'echo "run failed at line $LINENO" >&2' ERR

size=$1 bins=$2 labels=$3
shift 3

paths=()
IFS=, read -ra names <<< "$bins"
for b in "${names[@]}"; do
  paths+=("/work/linux/$b")
done

unset KUBERNETES_SERVICE_HOST KUBERNETES_SERVICE_PORT
cd /work
mkdir -p results
/work/linux/harness -sizes "$size" -kube perf/.kube -out "${OUT:-results}" \
  -bin "$(IFS=,; echo "${paths[*]}")" -label "$labels" "$@"
