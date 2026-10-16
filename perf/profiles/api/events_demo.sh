#!/bin/bash
# Short session at l: :events (as=Table dao path) + :rs (informer path) to
# compare API behavior of the two DAO routes.
set -u
REPO=/Users/ikennao/repos/OSS/k9s
T=/private/var/folders/bs/z4bl587j47j1x1r2vjhd1srh0000gn/T/opencode
BIN=$T/k9s-prof-api
CTX=kind-kwok-k9s-l
KCFG=$REPO/perf/.kube/k9s-l.yaml
SESS=api-evts
WORK=$T/api/run-evts
OUT=$REPO/perf/profiles/api
KLOG=$OUT/klog-l-events.log

rm -rf "$WORK"; mkdir -p "$WORK/cfg"
cat > "$WORK/cfg/config.yaml" <<'EOF'
k9s:
  refreshRate: 2
  skipLatestRevCheck: true
  ui:
    splashless: true
    enableMouse: false
EOF
mkdir -p "$WORK/cfg/clusters/$CTX/$CTX"
printf 'k9s:\n  cluster: %s\n  namespace:\n    active: all\n  view:\n    active: pods\n' "$CTX" > "$WORK/cfg/clusters/$CTX/$CTX/config.yaml"

now() { python3 -c 'import time; print(time.time())'; }
rec() { echo "$1 $(now)" >> "$WORK/events.txt"; }
ecount() { tmux capture-pane -p -t $SESS 2>/dev/null | grep -o "events(all)\[[0-9,]*\]" | head -1 | grep -o '\[[0-9,]*\]' | tr -d ',[]'; }
rcount() { tmux capture-pane -p -t $SESS 2>/dev/null | grep -o "replicasets(all)\[[0-9,]*\]" | head -1 | grep -o '\[[0-9,]*\]' | tr -d ',[]'; }
wait_for() { local cfn=$1 want=$2 end=$(( $(date +%s) + $3 )); while [ "$(date +%s)" -lt "$end" ]; do c=$($cfn); [ -n "$c" ] && [ "$c" -ge "$want" ] && return 0; sleep 0.05; done; return 1; }

rm -f "$KLOG" "$WORK/events.txt"
tmux kill-session -t $SESS 2>/dev/null
rec "launch"
tmux new-session -d -s $SESS -x 200 -y 50 "env KUBECONFIG=$KCFG KUBECACHEDIR=$WORK/kubecache K9S_CONFIG_DIR=$WORK/cfg K9S_LOGS_DIR=$WORK/cfg TERM=xterm-256color K9S_PROF_KLOG_V=9 K9S_PROF_KLOG_FILE=$KLOG $BIN --context $CTX -c pods -A; sleep 300"
sleep 6
rec "to_events"
tmux send-keys -t $SESS ':events' Enter
wait_for ecount 21000 60 && rec "events_table"
sleep 10
rec "to_rs"
tmux send-keys -t $SESS ':rs' Enter
wait_for rcount 2000 60 && rec "rs_table"
sleep 8
rec "done"
tmux send-keys -t $SESS ':q' Enter
sleep 3
tmux kill-session -t $SESS 2>/dev/null
cp "$WORK/events.txt" "$OUT/events-l-events.txt"
echo "events session done"
