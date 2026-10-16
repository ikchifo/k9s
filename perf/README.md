# k9s performance harness

Measures what a user feels, against fake clusters of 3 sizes.

## Phase 0: what "fast" means

All timings are wall clock, measured from the keystroke (or process start)
to the moment the terminal screen shows the expected content.

| Metric | What it is | s (500) | m (5k) | l (20k) |
|---|---|---|---|---|
| `startup_first_frame` | launch to `pods(all)` table drawn | 500ms | 1.5s | 3s |
| `startup_full_table` | launch to all pods counted in the title | 500ms | 1.5s | 3s |
| `switch_to_deploy` / `switch_to_pods` | `:deploy` / `:pods` to full table | 150ms | 250ms | 500ms |
| `context_switch` | `:ctx X` to full pod table on the new cluster | 500ms | 1.5s | 3s |
| `filter_key_echo` | keypress to the char showing in the prompt | 16ms | 16ms | 33ms |
| `filter_apply` | last filter key to filtered table | 250ms | 300ms | 400ms |
| `scroll_page_down` / `sort_toggle` | keypress to redrawn table | 16ms | 33ms | 100ms |
| `refresh_cpu` | CPU time of one 2s refresh tick | 20ms | 100ms | 400ms |
| `idle_cpu_pct` | CPU while idle on the pods view | 1% | 5% | 20% |
| `idle_rss` | resident memory while idle | 150MiB | 400MiB | 1.2GiB |

How I picked them:

- 16ms is one frame at 60Hz; anything typed should echo within it.
- 100ms is the usual "instant" limit for a reaction to a keypress.
- ~1s keeps a flow of thought; startup and context switch on big clusters are
  bounded by the API server (a 20k pod LIST is ~100MB of JSON), so they get more.
- Filtering has a 100ms debounce by design, so the budget is debounce + work.
- Idle cost matters because k9s often sits open all day on a laptop.

## Running

```sh
perf/cluster/up.sh k9s-s 500
perf/cluster/up.sh k9s-m 5000
perf/cluster/up.sh k9s-l 20000

(cd perf/harness && go build -o ../../bin/harness .)
go build -o bin/k9s .
bin/harness -bin bin/k9s -label mychange -runs 5 -out perf/results
bin/harness -bin bin/k9s -label check -budgets perf/budgets.json   # exits 1 on regression

perf/cluster/down.sh
```

Kubeconfigs live in `perf/.kube/` and never touch `~/.kube/config`.

## Keeping it fast

Two checks fail when a change makes k9s slower:

- `perf/benchcheck.sh` runs the hot-path Go benchmarks and fails when an
  allocation count (or heap bytes per cached pod) goes over
  `perf/bench-budgets.txt`. Allocations are stable across machines, so this
  can run in CI without a cluster.
- The harness with `-budgets` exits 1 when a median goes over budget.
  - `perf/budgets.json` holds the Phase 0 targets.
  - `perf/guard-macos.json` is the macOS result of the final run plus 30%
    headroom, so it catches regressions even where the Phase 0 target isn't met yet.

```sh
perf/benchcheck.sh
bin/harness -bin bin/k9s -label check -runs 3 -budgets perf/guard-macos.json
```

## Profiling build

`perf/prof/build.sh bin/k9s-prof` builds k9s with pprof, fgprof, trace and the
flight recorder, without changing `go.mod`. The harness forwards `K9S_PROF_*`:

```sh
K9S_PROF_ADDR=127.0.0.1:6060 bin/harness -bin bin/k9s-prof ...
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=20
K9S_PROF_KLOG_V=6 K9S_PROF_KLOG_FILE=/tmp/api.log ...   # client-go request log
K9S_PROF_TRACE=/tmp/start.trace ...                     # first 15s of runtime/trace
K9S_PROF_FLIGHT=/tmp/flight.trace ...                   # curl :6060/debug/flight dumps last 10s
```
