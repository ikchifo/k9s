# 01: profile-guided optimization

Change: add `default.pgo` at the repo root. `go build` uses it by default
(`-pgo=auto`), so `make build`, goreleaser and `go install` all pick it up.
Profile: `perf/profiles/cpu/default.pgo` (idle + sort + filter on the 20k pods view, 70s).

## Harness (5 runs, median; `harness.txt`)

| metric | size | before | after | delta |
|---|---|---|---|---|
| idle_cpu_pct | l | 65.5% | 56.5% | -14% |
| refresh_cpu | l | 1311ms | 1130ms | -14% |
| idle_rss | l | 1876MiB | 1731MiB | -8% |
| sort_toggle | l | 117ms | 94ms | -19% |
| filter_key_echo | l | 41ms | 31ms | -25% |
| idle_cpu_pct | m | 21.0% | 19.7% | -6% |
| startup / context_switch | all | | | within ±2% |
| idle_cpu_pct | s | 3.3% | 3.7% | +12% (raw ranges overlap: 3.0-3.7 vs 3.2-3.8) |

l refresh_cpu raw: before 1297-1345, after 1074-1173 ms. No overlap.

## benchstat (`benchstat.txt`, -count=10)

`BenchmarkPodRender` -11.5% sec/op (p=0.000), others unchanged.

## pprof diff (`pprof-diff.txt`)

Idle on l, 2 x 20s per binary (`prof-base/`, `prof-pgo/`), built with
`perf/prof/build.sh` with and without `-pgo`. Total samples 20.27s -> 19.30s (-4.8%).
The biggest drops are `runtime.madvise` (-0.57s), `moveInlineMarks` (-0.54s),
`memclrNoHeapPointers` (-0.37s) and `fromUnstructured` (-0.84s cum).

## Take

A small, real win at the cost of a 70KB file and slower builds.
The profile is the pods view at 20k; it goes stale as the hot path changes, so
it has to be regenerated after the pod render fix.
