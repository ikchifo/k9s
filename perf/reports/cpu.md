# CPU / allocations / memory (area: cpu)

Binary: `$T/k9s-prof-cpu` (copy of `k9s-prof`, go1.27.1).
Driven in tmux with an isolated `K9S_CONFIG_DIR` (same config as the harness: refreshRate 2, splashless, `pods -A`).
All artifacts are in `perf/profiles/cpu/`. Stage shares come from `stages.sh` / `stages_startup.sh` (pprof `-focus=<regex>`, so a stage's % is the cumulative share of samples whose stack contains it).

Heads-up: `k9s-prof` was built from an older `prof.go.in`.
Its env vars are `K9S_PPROF_ADDR`, `K9S_TRACE_FILE`, `K9S_FLIGHT_FILE`, `K9S_KLOG_V` (found with `strings`), not `K9S_PROF_*`.
So the harness `K9S_PROF_*` forwarding does nothing with the current binary.

## Per-tick numbers (l, 20,170 pods, idle on `pods(all)`)

Tick period from the execution trace: 2.187s (`idle_l.tick_period.txt`, 9 Hydrate bursts of 10 goroutines in 20s, `idle_l.trace.hydrate_ticks.txt`).
The `time.After(rate)` starts after the refresh, so the period is 2s plus the refresh time.
30s CPU profile = 13.7 ticks (`per_tick_l.txt`).

| stage | cum % of CPU (`idle_l.stages.txt`) | ms/tick |
|---|---|---|
| `model1.Hydrate` -> `render.(*Pod).Render` (parallel workers) | 61.9% | 670 |
| . of which `FromUnstructured` (spec + status) | 56.7% | 614 |
| GC background mark (`runtime.gcBgMarkWorker`) | 19.6% | 212 |
| `runtime.mallocgc` (all callers) | 9.6% | 104 |
| scheduler/netpoll idle spin (`kevent`, `usleep`) | 10.6% | — |
| `ui.(*Table).UpdateUI` total | 4.9% | 52 |
| . `RowEvents.Sort` | 2.6% | 28 |
| . `buildRow` (incl. `NewTableCell`, `Pad`) | 1.9% | 20 |
| . `tview.SetCell` | 0.4% | 4 |
| . `ComputeMaxColumns` | 0.2% | 2 |
| . `UpdateTitle`/`AsThousands` | 0% | 0 |
| `model.(*Table).refresh/reconcile` (dao List, Update, delta) | 3.0% | 33 |
| . `dao.(*Pod).List` (lister) | 1.8% | 19 |
| . `TableData.Update`/delta | 0.5% | 6 |
| `TableData.Clone` via `Peek` | 0.6% | 7 |
| tcell/tview draw | 0.5% | 5 |

Total CPU: 14.86s in 30.03s = 1.08 CPU-s per tick (baseline said ~1.3s).
Wall time of the Hydrate burst per tick: 116–166 ms (10 workers in parallel, `idle_l.trace.hydrate_ticks.txt`).

Scaling (m, 5,050 pods, `idle_m.stages.txt`, `per_tick_m.txt`): 337 ms CPU/tick, Pod.Render 56.1%, GC mark 14.4%, UpdateUI 4.3%.
So 4x pods -> 3.2x CPU per tick; the mix is the same. It is linear in pods, and render dominates at every size.

Copies of the table per tick: 1 deep copy.
`model.(*Table).refresh` -> `Peek` -> `TableData.Clone` (`internal/model/table.go:200`) copies all `Fields` (11 MB/tick, `idle_l.allocdelta.peek_clone.txt`).
`ui.Table.filtered` -> `TableData.Filter` uses `NewTableDataFromTable` (shares `rowEvents`, no copy) when the filter is empty.
The UI then builds a full set of tview cells: 2.69M `NewTableCell` in the window = 196k cells per tick (`idle_l.allocdelta.objects.txt`).

Allocation per tick: 4,586 MB / 13.7 ticks = 334 MB/tick and 5.84M objects/tick (`idle_l.allocdelta.space.txt`, `-base allocs0 allocs1`).
`render.(*Pod).defaultRow` cum = 3,717 MB (81%) = 13.7 KB per pod per tick.

---

## Findings (ranked by share)

### 1. `render.(*Pod).defaultRow` converts unstructured spec+status into typed structs for every pod on every tick
- **What:** `internal/render/pod.go:151` `FromUnstructured(Object["status"], &st)` and `:155` `FromUnstructured(Object["spec"], spec)`, called for all 20k pods each tick via `model1.Hydrate` -> `parallelRender` (`internal/model1/helpers.go:63`).
- **Evidence:** `idle_l.list_defaultRow.txt`: line 155 = 5.78s, line 151 = 2.70s, out of 9.20s for the whole function (61.9% of all CPU).
  Inside, reflect map lookups (`reflect.Value.MapIndex` 16.2%, `mapaccess2_faststr` 13.7%, `memHashAES` 5.6% flat) and `structured-merge-diff ... FromUnstructured -> encoding/json.Marshal` (3.6%, `idle_l.peek_json.txt`).
  Allocations: 81% of all bytes and 90% of all objects (`reflect.unsafe_New` 45M objects in 30s, `idle_l.allocdelta.objects.txt`). Those allocations drive GC (finding 2).
  m shows the same: 56.1% (`idle_m.stages.txt`).
- **User metrics:** idle_cpu_pct and refresh_cpu (~57% directly, ~85% with the GC it causes).
  It also slows everything on the UI goroutine indirectly: in `sort_l` and `filter_l` it is still 36% / 21% of CPU, competing with the interactive work.
- **Likely fix (pick one or combine):**
  (a) Cache the rendered row per pod UID+resourceVersion and render only objects whose RV changed. With KWOK pods nearly static, that is ~0 renders per tick.
  Age needs a cheap per-tick recompute from the stored creationTimestamp, and metrics columns need re-rendering only when metrics change.
  (b) Read the few needed fields straight from the unstructured map (`unstructured.NestedSlice` etc.) instead of converting the whole spec/status.
  (c) Use typed (protobuf) informers for pods so no conversion is needed at all (also fixes finding 3).
- **Risk:** (a) medium: cache invalidation (metrics, age, node/pod-level decorations, toggles like wide/vul score). (b) low–medium: many fields, and it is easy to diverge from the typed semantics. (c) high: this is a factory-wide change.

### 2. GC driven by ~334 MB/tick of short-lived garbage
- **What:** GC background mark + malloc caused by allocation volume, mostly from finding 1, plus 196k tview cells per tick.
- **Evidence:** `gcBgMarkWorker` 19.6%, `mallocgc` 9.6% of idle CPU (`idle_l.stages.txt`). GC assist ≈0%.
  5 GC cycles in a 20s trace window (`idle_l.trace.gc_count.txt`). `GCCPUFraction` 0.7% understates it because it excludes idle-priority mark workers (`gcDrainMarkWorkerIdle` 10% cum).
  Top allocators besides the render reflection: `tview.NewTableCell` 328 MB (7.2%), `gatherPodMX` 596 MB cum (13%), `extractGPU` 217 MB, `NestedNullCoercingStringMap` 183 MB (labels), `NewDeltaRow` 125 MB, `Fields.Clone` 105 MB (`idle_l.allocdelta.space.txt`).
- **User metric:** idle_cpu_pct / refresh_cpu, about 20–30%.
- **Likely fix:** this falls mostly with finding 1. Cheap extras: `gatherPodMX`/`extractGPU` allocate per container per tick (`internal/render/pod.go`); reuse tview cells instead of `Clear()` + `NewTableCell` for every row.
- **Risk:** low.

### 3. Heap: the unstructured informer cache is 92% of live heap (36.5 KB per pod)
- **What:** the dynamic informer stores each pod as a `map[string]any` tree decoded by `unstructuredJSONScheme.decodeToUnstructured` (from the watch stream: `StreamWatcher.receive` -> `rest/watch.(*Decoder).Decode`).
- **Evidence:** `idle_l.heap.space.txt` (inuse after `gc=1`): 778 MB live, 718.7 MB (92.4%) under `decodeToUnstructured`.
  `objectInterface` 576 MB flat, `unquote` 87 MB (string keys/values), 11.6M of 12.5M live objects (`idle_l.heap.objects.txt`).
  Holders (`heap_holders.txt`, `heap_per_pod.txt`):

  | holder | l live | per pod | m per pod |
  |---|---|---|---|
  | unstructured informer cache | 718.7 MB | 36.5 KB | 30.3 KB |
  | model1 rows (rendered `Fields`, deltas) | 40.9 MB | 2.1 KB | 2.6 KB |
  | ui/tview cells | 28.6 MB | 1.45 KB | 2.1 KB |
  | render scratch | 12 MB | 0.6 KB | — |

  For reference, the same pod as compact JSON is 5.3 KB, of which `managedFields` is 1.9 KB (`pod_json_size_l.txt`). The map form costs ~7x the JSON size.
  RSS 1.73 GB (`idle_l.rss.txt`) vs 778 MB live: the rest is GOGC=100 headroom (`NextGC` 1.72 GB, `HeapAlloc` 1.24 GB at sample time, `idle_l.memstats.txt`), filled by the per-tick garbage from findings 1–2.
- **User metric:** idle_rss (roughly 40% is the cache itself, 55% is GC headroom driven by churn). Also startup (finding 4).
- **Likely fix:** an informer transform (`SetTransform`) that drops `metadata.managedFields` (~35% of the payload) and annotations k9s doesn't show. Longer term, typed pod informers with protobuf. Cutting per-tick garbage (finding 1) shrinks the headroom.
- **Risk:** low–medium for stripping managedFields: first check whether the YAML/describe views read from the informer cache. High for typed informers.

### 4. Startup at l: about half of CPU samples is `runtime.madvise` (heap growth on darwin)
- **What:** `runtime.(*mheap).allocSpan` -> `sysUsed` -> `madvise(MADV_FREE_REUSE)` while the heap grows from 0 to ~1.4 GB during the initial watch/list decode.
- **Evidence:** two independent startup profiles (curl at launch, 8s): `runtime.madvise` 49.3% (`startup_l.stages.txt`) and 52.2% (`startup_l.run2.stages.txt`).
  In the same profiles, JSON stream decode is 9.0% (`StreamWatcher.receive`; `decodeToUnstructured` alone 4.0%), Pod.Render 6.9% / 5.6% (FromUnstructured 5.8% / 4.5%), GC mark 16.7% / 13.6%, scheduler wakeups ~17%, and UpdateUI and draw ~1%.
  The stack stops at `systemstack` (`startup_l.madvise.traces.txt`), so the triggering allocator can't be attributed from pprof. In steady idle it is 0% (`madvise_watch_by_profile.txt`).
  It returns when the heap regrows: 17.8% during filter typing, 6% during sort.
  Only 14 GCs happened by the end of the window (`startup_l.run2.memstats.txt`).
  The LIST itself shows no `Reflector.list` samples. With client-go WatchList, the initial state arrives through the watch stream, so "LIST decode" is the 9% `StreamWatcher.receive`.
- **User metric:** startup_full_table (4.8s) and context_switch (4.8s). The CPU share is clear, but the wall-clock share is not proven: the profile's 8.4–9.0 CPU-s over 8s means several cores, and madvise runs inside the allocating goroutines, so it serializes decode.
- **Likely fix:** allocate less during the initial sync (finding 3: a transform that drops fields before storing doesn't avoid decoding them; protobuf/typed avoids the map explosion).
  Untested idea: a higher `debug.SetGCPercent` (or `GOMEMLIMIT`-based tuning) during the initial sync, to cut scavenge/regrow cycles. Needs measurement.
- **Risk:** medium. This is runtime/OS behaviour, and a fix needs measurement on Linux too (madvise costs there differ).

### 5. Filter typing: regex over a freshly joined string for every row, on the UI goroutine
- **What:** `model1.(*TableData).rxFilter` (`internal/model1/table_data.go:181-191`). For each of 20k rows it builds `ff`, runs `strings.Join`, then `rx.MatchString` with a `(?i)(...)` backtracking regex. It is called from `view.(*Table).BufferCompleted` -> `QueueUpdateDraw` -> `ui.(*Table).Filter`, i.e. on the tview event loop.
- **Evidence:** `filter_l.stages.txt` (84 keys at 150 ms, so every key passes the 100 ms debounce): `TableData.Filter` 28.0% of CPU, `regexp` 17.5%. Line 189 (Join+Match) = 4.72s of 5.57s (`filter_l.list_rxFilter.txt`), `strings.Join` 6.7%, `unicode.SimpleFold` 5.6% (case-insensitive matching).
  The UI loop (`tview.Application.Run`) spent 6.74s in 20s = 80 ms per key, 5.49s of it in `ui.Table.Filter` = 65 ms per key (`filter_l.eventloop.topcum.txt`).
  Background Pod.Render still takes 20.8%, and madvise 17.8%.
- **User metric:** filter_key_echo p90 (~111 ms at l). Echo is blocked when a filter pass is queued in front of the key event. Est. 60–80% of the p90 tail. Also filter_apply.
- **Likely fix:** precompute a lowercased joined "filter string" per row once at render time (or cache it per row ID+RV). Use `strings.Contains` on lowercased text when the query has no regex metacharacters. Run the filter off the UI goroutine and post only the result.
- **Risk:** low (pure function; keep regex fallback).

### 6. Sort toggle: full sort + full row rebuild + deep clone on the UI goroutine
- **What:** `ui.(*Table).SortColCmd.func1` -> `ui.(*Table).Refresh` -> `Peek` (deep Clone) -> `filtered` -> `UpdateUI` (Sort + `buildRow` for all 20k rows).
- **Evidence:** `sort_l.sortcmd.topcum.txt` (50 presses in 20s): `SortColCmd` 2.93s = 59 ms CPU per press. Of that: `buildRow` 1.27s (25 ms/press), `RowEvents.Sort` 1.11s (22 ms; `model1.Less` -> `lessDuration`/`durationToSeconds` 0.43s, `sortorder.NaturalLess` 0.55s, `sort_l.list_less.txt`), `Peek`/Clone 0.29s (6 ms).
  `GetFilteredData` also calls `Peek` (a second deep clone, 0.14s, from the peek in the step above).
  Whole-profile share: UpdateUI 19.6%, Sort 8.7% (`sort_l.stages.txt`).
- **User metric:** sort_toggle (105–116 ms at l). Est. ~55% is CPU on the UI loop; the rest is draw plus contention with the background render and GC (Pod.Render 35.6%, GC mark 22.8% in the same profile).
- **Likely fix:** build tview cells only for visible rows (virtual table / `tview.TableContent`). Parse AGE/duration once into a numeric sort key at render time instead of `durationToSeconds` in every `Less`. Drop the redundant `Peek` clones on the UI path.
- **Risk:** medium for the virtual table (selection, marks and cell coloring rely on materialized cells). Low for the precomputed sort keys.

### 7. Minor: things that look expensive but are not
- `ComputeMaxColumns` 0.2%, `UpdateTitle`/`AsThousands` 0%, tview `SetCell` 0.4%, draw 0.5% at idle (`idle_l.stages.txt`).
- `TableData.Update`/deltas 0.5%, lister List 1.8%.
- Per-tick `Clone`: 0.6% CPU and 11 MB/tick. It is cheap compared with render; remove it only as part of finding 6.

---

## Startup breakdown (question 3)
`startup_l.stages.txt` / `startup_l.run2.stages.txt` (8s CPU profile from process start, two runs):

| bucket | run1 | run2 |
|---|---|---|
| `runtime.madvise` (heap growth) | 49.3% | 52.2% |
| scheduler wake/park | 16.9% | 17.5% |
| GC mark | 16.7% | 13.6% |
| watch stream decode (JSON -> unstructured) | 9.0% | 9.0% |
| Pod.Render (FromUnstructured) | 6.9% (5.8%) | 5.6% (4.5%) |
| mallocgc | 8.0% | 7.3% |
| UpdateUI + draw | 0.9% | 1.3% |

(Buckets overlap: madvise sits under mallocgc paths.)

## Micro-benchmarks (question 5, `bench.txt`, M-series 10 cores)
| bench | ns/op | B/op | allocs/op |
|---|---|---|---|
| BenchmarkPodRender | 26,685 | 8,929 | 178 |
| BenchmarkMaxColumn | 32 | 32 | 1 |
| BenchmarkParallelRender | 64,929 | 722 | 14 |

The PodRender bench matches the profile: 27 µs × 20,170 pods = 538 ms of single-core CPU, vs 670 ms/tick measured for Hydrate (the difference is GC/cache effects at scale).
178 allocs/op × 20k = 3.6M allocations per tick from render, vs 5.84M/tick total measured.

## PGO (question 6)
`default.pgo` = `pprof -proto idle_l.cpu.pb.gz sort_l.cpu.pb.gz filter_l.cpu.pb.gz` (70s, 49.9 CPU-s; `default.pgo.top.txt`, `default.pgo.stages.txt`).
Mix: Pod.Render 37.5%, GC mark 20.1%, Filter 11.2%, UpdateUI 8.0%, madvise 8.9%.
It is representative of a large-cluster pods view with interaction, but the time weights are arbitrary (30s idle, 20s sort, 20s filter).
It does not cover startup, other resource views, logs, or describe/YAML.
The hot code is mostly reflection in apimachinery and the regexp/runtime packages, so PGO inlining/devirtualization gains are likely small (a few %). Fixing finding 1 removes most of the profile PGO would optimize.

## Method notes
- Stage % uses `-focus` cum, so nested stages overlap and don't sum to 100.
- Idle profiles have no user input. Clone/Filter counts come from code paths plus alloc deltas, not instrumentation.
- Tick counts for m reuse the l tick period (the m trace was not captured), so treat the m per-tick numbers as ±5%.
- Other agents shared the machine, so absolute ms/tick values are noisy; shares and counts are the robust numbers.
