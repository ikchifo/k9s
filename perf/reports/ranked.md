# Ranked bottlenecks

Combined from `cpu.md`, `wait.md`, `api.md` and `render.md` in this folder.
Baseline is `perf/results/baseline.json` (HEAD 6450b405, kwok clusters
s ≈ 500, m ≈ 5k, l ≈ 20k pods, 2s refresh).

Ranked by share of user-visible delay (or CPU/RAM), divided by risk.

| # | bottleneck | where | share (evidence) | likely fix | risk |
|---|---|---|---|---|---|
| 1 | Filter result only shows on the next 2s refresh | `ui/table.go:387` `Filter()` never calls `UpdateUI` | ~95% of `filter_apply` at every size (wait.md F1; render.md: 751ms at 2s refresh → 4754ms at 6s) | rebuild rows from filtered data in `Filter` | low |
| 2 | Fixed timers instead of "cache synced" events | `model/table.go:26` 300ms `initRefreshRate`; first rows wait for the next 2s tick after informer sync; `watch/factory.go:140` 100ms CRD poll | `switch_to_deploy` 274 of ~350ms (78%); startup/ctx m 1284 of 2627ms (49%), s 184+100 of 546ms (52%), l ~295 of 4813ms (6%) (wait.md F2–F4) | refresh as soon as the informer syncs; poll sync at a short interval | low |
| 3 | Pod rows rebuilt from unstructured maps on every tick | `render/pod.go:151,155` `FromUnstructured` of full spec+status | 62% of idle CPU at l, 56% at m; 81% of alloc bytes, 90% of objects; drives most of the 29% GC+malloc (cpu.md #1–2). `refresh_cpu` l 1311ms, `idle_cpu` 65% | stop converting the whole spec/status; cache per UID+resourceVersion; keep age live | low–med |
| 4 | Startup/ctx stream all pods as unstructured JSON (WatchList) | dynamic informers in `watch/factory.go` | l startup: ~4.1s of 4.8s is the pods stream; 2.04s client CPU for JSON → maps vs 0.11s for typed protobuf (api.md F1) | typed protobuf informer for pods | high (render layer reads unstructured everywhere) |
| 5 | Informer cache holds full pods | `watch/factory.go` | 92% of live heap at l (36.5KB/pod); `managedFields` is 1.9 of 5.3KB JSON (cpu.md #3) | informer transform dropping `managedFields` | low–med (check YAML view) |
| 6 | Regex filter over all rows on the UI goroutine per key | `model1/table_data.go:189` `rxFilter` | ~65ms UI CPU per key at l, 128ms/pass in bench; key echo l median 41ms, p90 up to 111ms (cpu.md #5, render.md #3) | lower-case once per row, substring fast path when no regex chars | low |
| 7 | Sort re-parses durations/quantities in every compare, from model order every tick | `model1/helpers.go:134-221` | sort_toggle l 117ms: ~22ms sort + 25ms row build; 2.6% idle CPU (render.md #2, cpu.md) | parse sort key once per row per sort | low |
| 8 | All 20k rows turned into tview cells each tick | `ui/table.go:472-548` | 4.9% idle CPU, 9.3% allocs at l; 99.8% of cells are off screen (render.md #1) | colorer once per row, reuse cells; visible-only needs tview fork change | low → high |
| 9 | Deep table clone on every refresh and every selected-row lookup | `model/table.go:196`, `ui/table.go:596` | 3.4% allocs, 12MB per call at l (render.md #4) | share immutable snapshot, look up without cloning | med |
| 10 | Context switch rebuilds clients and re-streams everything; config I/O on UI goroutine | `view/app.go` `switchContext` | ~100ms frozen UI + full re-stream (api.md F2, wait.md F5) | cache factory per context; move off UI goroutine | med |
| 11 | `GET /version` every 15s with a throwaway clientset | `client` `CheckConnectivity` | negligible locally; 1 RTT per 15s on real clusters (api.md F4) | reuse clientset | low |

Not bottlenecks (measured): terminal output (~20–35B per refresh,
`Application.draw` 0.5% CPU), locks (no k9s locks in mutex profile),
goroutine leaks (40 idle, stable after 6 switches), SSAR caching,
`AsThousands`/title/styles, `ComputeMaxColumns`.

## What Go itself costs

At l idle, CPU is ~62% pod rendering (reflection-based conversion) + ~29%
GC/malloc driven by that garbage. Both are caused by what the code does
each tick, not by the language: the same work in Rust would still convert
20k pods every 2s. Startup at l is dominated by moving and decoding 108MB
of JSON; the decode share is format choice (JSON → maps), not Go.
See `rust-memo.md`.

## Fix order

1. PGO (cheap baseline, measure only).
2. #1 filter redraw.
3. #2 event-driven first refresh.
4. #3 pod render without full conversion.
5. #6, #7 filter and sort CPU.
6. #5 managedFields transform.

#4, #8 (visible-only), #9, #10 are larger changes; revisit after
re-measuring.
