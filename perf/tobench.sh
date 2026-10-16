#!/usr/bin/env bash
# Turns harness raw samples into Go benchmark lines so benchstat can compare
# two labels. Usage: perf/tobench.sh <label> [dir] > label.txt
set -euo pipefail

label=$1
dir=${2:-perf/results}

jq -r '
  .stats[]
  | select(.unit == "ms")
  | . as $s
  | .raw[]
  | "BenchmarkHarness/\($s.size)/\($s.metric) 1 \(. * 1000000 | floor) ns/op"
' "$dir/$label.json"
