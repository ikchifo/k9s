# 11 - startup at 20k pods

Host: M1 Max (10 CPU), go1.27.1, client-go v0.37.1, kind-kwok-k9s-l (20,171 pods).

## Where the time goes

klog -v6 of a base startup: setup calls finish in ~40ms, the pods WatchList opens,
the initial-events bookmark arrives after `duration=4.09s`, render takes ~0.3s.
So ~4.1s of ~4.4s is the pods initial sync.

`go tool trace` of the informer (probe): the `StreamWatcher.receive` goroutine is
Running 3.97s of 4.10s. Startup is bound by one goroutine decoding JSON.
Wire and store insertion are not the limit.

| What (pods, l) | Time |
|---|---|
| raw JSON WatchList, curl, 108MB | 1.95-1.99s |
| raw JSON, Go reader, HTTP/2 / HTTP/1.1 | 2.19s / 1.82s |
| raw protobuf watch, 68MB | 0.75s |
| dynamic informer sync (stock) | 4.0-4.4s |
| typed protobuf informer sync | 0.80s |
| dynamic informer, this change | 2.7-2.9s |

## Options

### a) GC / heap tuning - no win
Harness, 3 interleaved runs, `startup_full_table` median:
GOGC=100 4473ms, GOGC=400 4417ms, GOGC=off+GOMEMLIMIT=3GiB 4395ms (noise).
A 1GB ballast did not remove madvise either (still ~46% of samples).
The ~50% `runtime.madvise` in macOS profiles is `sysUsedOS` ->
`madvise(MADV_FREE_REUSE)` on span reuse, done by GC/alloc on other threads.
It is real CPU but off the critical path. Wrappers in `wrap/`.

### b) protobuf -> unstructured - no win; typed informer - big win, high risk
Offline bench on the real 20,170-pod stream (`bench/`, -cpu 4):

| Decode path | Time | Alloc | Allocs |
|---|---|---|---|
| client-go dynamic watch chain (stock) | 3.44-3.9s | 943MB | 15.65M |
| JSON -> unstructured, objects only | 1.72-2.09s | 810MB | 15.2M |
| protobuf -> typed -> `ToUnstructured` | 1.85-3.3s | 1.49GB | 20.7M |
| protobuf typed only | 0.12s | 423MB | 3.3M |
| fast chain (this change) | 2.19-2.21s | 923MB | 15.37M |

`ToUnstructured` costs as much as JSON parsing, so protobuf buys nothing while
dao/render need `*unstructured.Unstructured`. A typed pods informer would be
~5x faster but touches every pod path in dao/render/view. Not done.

Why the stock chain is slow: per event, `json.Serializer.Decode` runs
`meta.Interpret` (encoding/json, to sniff GVK), then unmarshals into
`Unstructured`, whose `UnmarshalJSON` validates and re-parses; the event
envelope is also parsed, validated and copied to a `RawExtension`. Object bytes
get scanned ~4 times.

### c) paginated LIST instead of WatchList - worse
`KUBE_FEATURE_WatchListClient=false`: sync 4.7-4.8s vs 4.2s, heap 945MB vs 760MB.
`HasSynced` only flips after the full list, so no partial first page either.

### d) Linux ~2x slower - hypotheses
- Not GOMAXPROCS: Go 1.25+ honours the cgroup CPU limit (16 on a 16-CPU limit)
  unless `GOMAXPROCS`/`GODEBUG=containermaxprocs=0` is set.
- Not madvise: on Linux `sysUsedOS` is a no-op.
- Startup is single-goroutine decode, so per-core speed dominates: a GKE vCPU is
  likely 1.5-2x slower single-threaded than an M1 Max core.
- Also possible: CFS throttling when GC workers burst alongside decode, page
  faults on a fresh ~1GB heap, TLS + real network for 108MB JSON.
- Idle 2x: could be timer/epoll wakeups or GC with more Ps; needs a profile.

What to measure on the Linux pod:
- `GODEBUG=gctrace=1`, and `/sched/gomaxprocs` from runtime/metrics.
- `/sys/fs/cgroup/cpu.stat` `nr_throttled`/`throttled_usec` before/after startup.
- `k11probe -mode raw|informer|fast|typed` (`probe/`) to split wire vs decode.
- `K9S_PROF_TRACE` trace: Running time of the receive goroutine.
- A 30s CPU profile at idle.

## Change

`internal/client/codec.go`: the dynamic client gets a wrapped
NegotiatedSerializer whose JSON decoders parse each watch event once:
- event envelope: `sigs.k8s.io/json` straight into `*metav1.WatchEvent`;
- object: one unmarshal into `map[string]any` -> `Unstructured`.

It falls back to the stock serializer when a target type or GVK is given, on
parse errors, missing kind/version, or kinds known to the meta scheme
(`Status` etc.), so error events and typed decodes are unchanged.
`TestWatchCodecMatchesStock` checks parity; offline, all 20,170 real events
were `reflect.DeepEqual` to stock.

### Numbers
Micro bench (`BenchmarkWatchDecode`, 2000 pods, -cpu 4, n=10), `benchstat.txt`:

```
WatchDecode-4   231.4m ± 4%   151.2m ± 22%   -34.65% (p=0.000 n=10)
B/op            57.74Mi        56.07Mi        -2.89%
allocs/op       1.108M         1.082M         -2.35%
```

E2E harness, l, 5 interleaved runs (`harness/`), `startup_full_table`:

| | median | raw (ms) |
|---|---|---|
| base | 4508ms | 4502, 4518, 4464, 4650, 4508 |
| codec | 3061ms | 3580, 3061, 2987, 3059, 3081 |

-32% time to full table. CPU diff in `pprof-diff.txt` (the saved scan/skip and
encoding/json `Interpret` work).

## Files
- `bench/` offline decode bench on the real stream (own go.mod; data in `$T/api`).
- `probe/` informer probe (`-mode raw|informer|typed|decode|pipe|fast`).
- `tracestat/` goroutine state summary for `go tool trace` files.
- `startup-base.trace` base k9s startup trace.
- `informer.*.pb.gz`, `decode.cpu.pb.gz`, `bench/*.cpu.pb.gz` profiles.
- `cpu-before.pb.gz`, `cpu-after.pb.gz`, `bench-*.txt`, `benchstat.txt`.
