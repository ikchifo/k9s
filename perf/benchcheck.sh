#!/usr/bin/env bash
# Runs the hot-path benchmarks and fails when a counted metric (allocs/op,
# heap-B/pod) goes over budget. Counts, not times, so it holds on any machine.
# Usage: perf/benchcheck.sh [budget-file]
set -euo pipefail
trap 'echo "benchcheck: failed at line $LINENO" >&2' ERR

budgets=${1:-perf/bench-budgets.txt}
out=$(mktemp)
trap 'rm -f "$out"' EXIT

tops=$(awk '!/^#/ && NF { split($1, p, "/"); if (!seen[p[1]]++) printf "%s%s", sep, p[1]; sep = "|" }' "$budgets")

go test -run '^$' -bench "^(${tops})\$" -benchmem -benchtime=50x \
	./internal/client/ ./internal/model1/ ./internal/render/ ./internal/ui/ ./internal/watch/ \
	| tee "$out"

awk '
	NR == FNR { if ($0 !~ /^#/ && NF) max[$1 " " $2] = $3; next }
	/^Benchmark/ {
		name = $1; sub(/-[0-9]+$/, "", name)
		for (i = 3; i < NF; i += 2) {
			key = name " " $(i + 1)
			if (!(key in max)) continue
			seen[key] = 1
			if ($i + 0 > max[key] + 0) {
				printf "over budget: %s %s > %s\n", key, $i, max[key]
				bad = 1
			}
		}
	}
	END {
		for (k in max) if (!(k in seen)) { printf "missing: %s\n", k; bad = 1 }
		exit bad
	}
' "$budgets" "$out"
