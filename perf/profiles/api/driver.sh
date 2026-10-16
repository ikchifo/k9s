#!/bin/bash
# Drives k9s in tmux through a fixed action script while client-go request
# logging (klog v=8) captures every API call with timestamps.
# Usage: driver.sh <size s|m|l> <pods> <deploys> <outdir>
set -u
SIZE=$1; PODS=$2; DEPLOYS=$3; OUT=$4
REPO=/Users/ikennao/repos/OSS/k9s
T=/private/var/folders/bs/z4bl587j47j1x1r2vjhd1srh0000gn/T/opencode
BIN=$T/k9s-prof-api
CTX=kind-kwok-k9s-$SIZE
OTHER=s; [ "$SIZE" = s ] && OTHER=m
OTHERCTX=kind-kwok-k9s-$OTHER
KCFG=$REPO/perf/.kube/k9s-$SIZE.yaml
OKCFG=$REPO/perf/.kube/k9s-$OTHER.yaml
SESS=api-$SIZE
WORK=$T/api/run-$SIZE

rm -rf "$WORK"; mkdir -p "$WORK/cfg"
cat > "$WORK/cfg/config.yaml" <<'EOF'
k9s:
  refreshRate: 2
  skipLatestRevCheck: true
  ui:
    splashless: true
    enableMouse: false
EOF
for c in $CTX $OTHERCTX; do
  mkdir -p "$WORK/cfg/clusters/$c/$c"
  printf 'k9s:\n  cluster: %s\n  namespace:\n    active: all\n    favorites: [all, default]\n  view:\n    active: pods\n' "$c" > "$WORK/cfg/clusters/$c/$c/config.yaml"
done

now() { python3 -c 'import time; print(time.time())'; }
nowi() { date +%s; }
rec() { echo "$1 $(now)" >> "$WORK/events.txt"; }

pcount() {
  tmux capture-pane -p -t $SESS 2>/dev/null | grep -o "pods(all)\[[0-9,]*\]" | head -1 | grep -o '\[[0-9,]*\]' | tr -d ',[]'
}
dcount() {
  tmux capture-pane -p -t $SESS 2>/dev/null | grep -o "deployments(all)\[[0-9,]*\]" | head -1 | grep -o '\[[0-9,]*\]' | tr -d ',[]'
}
has_ctx() { tmux capture-pane -p -t $SESS 2>/dev/null | grep -c "Context: $1"; }

wait_for() { # wait_for <cfn> <want> <secs>
  local cfn=$1 want=$2 end=$(( $(nowi) + $3 ))
  while [ "$(nowi)" -lt "$end" ]; do
    local c=$($cfn)
    if [ -n "$c" ] && [ "$c" -ge "$want" ]; then return 0; fi
    sleep 0.05
  done
  echo "TIMEOUT via $cfn want=$want; screen:" >&2
  tmux capture-pane -p -t $SESS | head -3 >&2
  return 1
}

wait_ctx_pods() { # context + pods count (for ctx switch on small targets)
  local end=$(( $(nowi) + $1 ))
  while [ "$(nowi)" -lt "$end" ]; do
    if [ "$(has_ctx $CTX)" -ge 1 ]; then
      local c=$(pcount)
      if [ -n "$c" ] && [ "$c" -ge $PODS ]; then return 0; fi
    fi
    sleep 0.05
  done
  echo "TIMEOUT ctx switch" >&2; return 1
}

# --- Main session: startup -> idle -> :deploy -> :pods -----------------------
tmux kill-session -t $SESS 2>/dev/null
KLOG=$OUT/klog-$SIZE-main.log
rm -f "$KLOG"
kubectl --kubeconfig $KCFG get --raw /metrics > "$OUT/metrics-$SIZE-pre.txt" 2>/dev/null
rec "launch $CTX"
tmux new-session -d -s $SESS -x 200 -y 50 \
  "env KUBECONFIG=$KCFG KUBECACHEDIR=$WORK/kubecache K9S_CONFIG_DIR=$WORK/cfg K9S_LOGS_DIR=$WORK/cfg TERM=xterm-256color K9S_PROF_KLOG_V=9 K9S_PROF_KLOG_FILE=$KLOG $BIN --context $CTX -c pods -A; sleep 300"
sleep 0.2
wait_for pcount $PODS 180 && rec "full_pods_table"
sleep 1.2
rec "idle_start"; sleep 20; rec "idle_end"
tmux send-keys -t $SESS ':deploy' Enter
wait_for dcount $DEPLOYS 60 && rec "deploy_table"
sleep 2
tmux send-keys -t $SESS ':pods' Enter
wait_for pcount $PODS 60 && rec "pods_table"
sleep 6
tmux send-keys -t $SESS ':q' Enter
sleep 3
tmux kill-session -t $SESS 2>/dev/null
kubectl --kubeconfig $KCFG get --raw /metrics > "$OUT/metrics-$SIZE-post.txt" 2>/dev/null
echo "main session done for $SIZE"

# --- Context switch session: launch on OTHER, then :ctx to target -----------
SESS=api-$SIZE-ctx
KLOG=$OUT/klog-$SIZE-ctx.log
rm -f "$KLOG"
rec "ctx_launch $OTHERCTX"
tmux new-session -d -s $SESS -x 200 -y 50 \
  "env KUBECONFIG=$OKCFG:$KCFG KUBECACHEDIR=$WORK/kubecache2 K9S_CONFIG_DIR=$WORK/cfg K9S_LOGS_DIR=$WORK/cfg TERM=xterm-256color K9S_PROF_KLOG_V=9 K9S_PROF_KLOG_FILE=$KLOG $BIN --context $OTHERCTX -c pods -A; sleep 300"
sleep 0.2
OTHERPODS=514; [ "$OTHER" = m ] && OTHERPODS=5050
wait_for pcount $OTHERPODS 180 && rec "ctx_other_table"
sleep 3
tmux send-keys -t $SESS ":ctx $CTX" Enter
wait_ctx_pods 180 && rec "ctx_switch_done"
sleep 6
tmux send-keys -t $SESS ':q' Enter
sleep 3
tmux kill-session -t $SESS 2>/dev/null
cp "$WORK/events.txt" "$OUT/events-$SIZE.txt"
echo "ctx session done for $SIZE"
