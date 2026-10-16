#!/usr/bin/env bash
# Usage: perf/compare.sh <before-label> <after-label> [results-dir]
# Prints median before/after and the change for every metric.
set -euo pipefail

dir=${3:-perf/results}
before="$dir/$1.json"
after="$dir/$2.json"

jq -r -n --slurpfile a "$before" --slurpfile b "$after" '
  def key: .size + "/" + .metric;
  ($a[0].stats | map({(key): .}) | add) as $old
  | ["size", "metric", "unit", "before", "after", "delta"],
    ($b[0].stats[] | . as $n | $old[$n | key] as $o
      | select($o != null)
      | [$n.size, $n.metric, $n.unit,
         ($o.median * 10 | round / 10 | tostring),
         ($n.median * 10 | round / 10 | tostring),
         (if $o.median == 0 then "n/a"
          else ((($n.median - $o.median) / $o.median * 100) | round | tostring) + "%" end)])
  | @tsv' | column -t -s $'\t'
