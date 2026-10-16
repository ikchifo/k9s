#!/usr/bin/env bash
# Creates kind+KWOK clusters with fake nodes and N pods (via Deployments).
# Usage: perf/cluster/up.sh <name> <pods> [nodes]
# Kubeconfig goes to perf/.kube/<name>.yaml; ~/.kube/config is not touched.
# KWOK_RUNTIME picks the kwokctl runtime (default kind; binary needs no Docker).
set -euo pipefail

name=$1
pods=$2
nodes=${3:-$(( (pods + 249) / 250 ))}

here=$(cd "$(dirname "$0")" && pwd)
kcfg="$here/../.kube/$name.yaml"
mkdir -p "$(dirname "$kcfg")"

replicas=100
ns_count=$(( (pods + 999) / 1000 ))
deploys=$(( pods / replicas ))

export KUBECONFIG=$kcfg
if ! kwokctl get clusters 2>/dev/null | grep -qx "$name"; then
  kwokctl create cluster --name "$name" --runtime "${KWOK_RUNTIME:-kind}" --disable-qps-limits \
    --kubeconfig "$kcfg" --wait 5m
fi
kwokctl get kubeconfig --name "$name" > "$kcfg"
export KUBECONFIG=$kcfg

gen_nodes() {
  for i in $(seq 1 "$nodes"); do
    cat <<YAML
---
apiVersion: v1
kind: Node
metadata:
  name: kwok-node-$i
  annotations:
    kwok.x-k8s.io/node: fake
    node.alpha.kubernetes.io/ttl: "0"
  labels:
    type: kwok
    kubernetes.io/hostname: kwok-node-$i
    kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    topology.kubernetes.io/zone: zone-$(( i % 3 ))
status:
  allocatable: {cpu: "64", memory: 512Gi, pods: "300"}
  capacity: {cpu: "64", memory: 512Gi, pods: "300"}
  nodeInfo: {architecture: amd64, operatingSystem: linux, kubeletVersion: fake}
  phase: Running
YAML
  done
}

gen_workloads() {
  for n in $(seq 1 "$ns_count"); do
    printf -- '---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: team-%02d\n' "$n"
  done
  for d in $(seq 1 "$deploys"); do
    ns=$(printf 'team-%02d' $(( (d - 1) % ns_count + 1 )))
    cat <<YAML
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: svc-$d
  namespace: $ns
  labels: {app: svc-$d, tier: backend}
spec:
  replicas: $replicas
  selector: {matchLabels: {app: svc-$d}}
  template:
    metadata:
      labels: {app: svc-$d, tier: backend, version: v1}
    spec:
      nodeSelector: {type: kwok}
      containers:
      - name: app
        image: registry.example.com/team/svc-$d:1.2.3
        ports: [{containerPort: 8080, name: http}]
        env: [{name: MODE, value: perf}]
        resources:
          requests: {cpu: 10m, memory: 16Mi}
          limits: {cpu: 100m, memory: 64Mi}
      - name: sidecar
        image: registry.example.com/infra/proxy:2.0
        resources:
          requests: {cpu: 5m, memory: 8Mi}
YAML
  done
}

gen_nodes | kubectl apply --server-side -f - >/dev/null
gen_workloads | kubectl apply --server-side -f - >/dev/null

echo "waiting for $pods running pods in $name..."
for _ in $(seq 1 600); do
  running=$(kubectl get pods -A --field-selector=status.phase=Running --no-headers 2>/dev/null \
    | grep -c team- || true)
  if [ "$running" -ge "$pods" ]; then
    echo "$name: $running pods running on $nodes nodes"
    exit 0
  fi
  sleep 5
done
echo "timeout: only $running running" >&2
exit 1
