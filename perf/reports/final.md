# Final results

Baseline: HEAD 6450b405. Final: master with changes 01-11.
Same kwok clusters (s ≈ 500, m ≈ 5k, l ≈ 20k pods), both binaries interleaved
in one harness run.

- macOS: M1 Max, 5 runs, `perf/results/{baseline-f,final}.json`.
- Linux: GKE pod, 16 vCPU, 10 runs per size, `perf/results/linux/<size>/{baseline-r5,final}.json`.

## User-facing timings (median, before → after)

| metric | size | macOS | Linux | budget |
|---|---|---|---|---|
| startup to full table | s | 550 → 232ms | 631 → 350ms | 500ms |
| | m | 2676 → 882ms | 2676 → 1881ms | 1.5s |
| | l | 4772 → 3037ms | 11102 → 6224ms | 3s |
| context switch | s | 550 → 216ms | 564 → 305ms | 500ms |
| | m | 2633 → 904ms | 2659 → 1842ms | 1.5s |
| | l | 4790 → 3129ms | 11028 → 6172ms | 3s |
| `:deploy` switch | l | 354 → 81ms | 389 → 151ms | 500ms |
| `:pods` switch | l | 283 → 282ms | 462 → 370ms | 500ms |
| filter key echo | l | 37 → 7ms | 70 → 8ms | 33ms |
| filter apply | s | 761 → 183ms | 723 → 171ms | 250ms |
| | l | 819 → 200ms | 1397 → 208ms | 400ms |
| sort toggle | l | 119 → 87ms | 180 → 107ms | 100ms |
| scroll page | l | 8.5 → 9.8ms (noise) | 9.8 → 8.4ms | 100ms |
| refresh CPU per tick | m | 433 → 62ms | 836 → 106ms | 100ms |
| | l | 1298 → 187ms | 2808 → 347ms | 400ms |
| idle CPU | m | 21.6 → 3.1% | 41.8 → 5.3% | 5% |
| | l | 64.9 → 9.4% | 140 → 17.3% | 20% |
| idle RSS | m | 531 → 337MiB | 512 → 335MiB | 400MiB |
| | l | 1805 → 1054MiB | 1735 → 1042MiB | 1.2GiB |

Full tables, p90 and raw samples are in the JSON files.
Linux benchstat over harness samples: 26 metrics significant (p < 0.05).
Not significant: `:pods` switch at s and m, scroll at s and m, sort at s.

## Budgets still missed

- macOS: l startup (3037 vs 3000ms) and l context switch (3129 vs 3000ms).
- Linux:
  - startup and context switch at m and l;
  - m `refresh_cpu` 106 vs 100ms, m idle CPU 5.3 vs 5%;
  - l sort 107 vs 100ms.

Startup and context switch are now bound by one goroutine decoding the pods
WatchList JSON (trace: decode goroutine Running 2.7s of the ~3.0s on macOS).
Linux is ~2x slower there because that path runs on a single core.

## Where the remaining time goes (macOS, l, profiling build)

- Idle, 20s: 1.58s CPU (7.9%).
  - runtime: madvise 27% flat, GC mark 15%, malloc 5%;
  - `NaturalCompare` 16%: sort tie-breaks on row ids, done on every tick;
  - `UpdateUI` 24% cumulative, `hydrateCached` 11%, `podState.fill` 6%.
- Startup, first 4s: 5.27s CPU across threads.
  - madvise 30%, GC mark 19%, `pthread_cond_signal`/`kevent` 29%;
  - JSON unmarshal 9%, decoders 9%, TLS read 4%;
  - rendering and `UpdateUI` 3%.

## Changes

| # | commit | what | main effect |
|---|---|---|---|
| 01 | fde0f4f4 | PGO profile | l idle CPU -14%, `PodRender` -11.5% |
| 02 | 7453f748 | filter redraws at once, trailing debounce | filter apply -60..-75%, key echo l -78% |
| 03 | 9ac29c78, c3bc88f9 | refresh when informer syncs | startup/ctx s, m -55%; `:deploy` -76..-85% |
| 04 | 9a30f89c | convert only needed pod fields | refresh CPU -24% |
| 05 | f98db3d2 | substring fast path for filters | filter apply l -33% |
| 06 | 84f9af2e | parse sort keys once | sort l -14% (Linux; macOS noise) |
| 07 | fba54d96 | drop managedFields from cache | RSS l -30% |
| 08 | 427c01ac | reuse rows of unchanged pods | refresh CPU -70..-82% |
| 09 | daf75e70..27e28f1d | reuse tview cells, colorer per row, cached header | sort l -19% |
| 10 | 4276de38 | reuse client for the 15s connectivity check | -68% CPU per check, /api, /apis 2 → 1 |
| 11 | 4c0c2366, 110b9ed0 | one-pass JSON watch decode | startup/ctx l -32% |

Evidence per change: `perf/evidence/NN-*/`.

## Not done

- Typed protobuf informer for pods: sync 0.8s instead of ~2.8s at l, but every
  pod path in dao, render and view reads unstructured. High risk.
- Draw only visible rows: the tview fork has no virtual table content.
- Per-context factory cache for switching back to a cluster.
- Share one table snapshot instead of deep clones: decorators write rows in
  place, so it needs copy-on-write first.
- Id tie-break sort keys (16% of the remaining idle CPU).

## Known issue

`go test -race ./...` fails on HEAD before any change, and identically after:
`TestFlash`, `TestFlashBurst`, `TestUpdateLogs` (internal/model), `TestFlash`
(internal/ui), `TestShowPrompt` (internal/ui/dialog). Same list before and after
the changes.
