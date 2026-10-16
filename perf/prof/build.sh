#!/usr/bin/env bash
# Builds a profiling k9s binary without touching go.mod or the source tree.
# Usage: perf/prof/build.sh <out-binary> [extra go build flags...]
# Runtime env: K9S_PROF_ADDR=127.0.0.1:6060 K9S_PROF_KLOG_V=6 K9S_PROF_KLOG_FILE=f K9S_PROF_TRACE=f K9S_PROF_FLIGHT=f
set -euo pipefail

out=${1:?out binary}
shift
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cp "$root/go.mod" "$work/go.mod"
cp "$root/go.sum" "$work/go.sum"
cat > "$work/overlay.json" <<EOF
{"Replace": {"$root/prof.go": "$root/perf/prof/prof.go.in"}}
EOF

cd "$root"
env -u GOROOT go get -modfile="$work/go.mod" github.com/felixge/fgprof@v0.9.5 >/dev/null 2>&1
env -u GOROOT go build -modfile="$work/go.mod" -overlay="$work/overlay.json" \
  -tags k9sprof -o "$out" "$@" .
