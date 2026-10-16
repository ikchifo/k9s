# k9s wait report: where wall time goes when it isn't CPU

Area: sleeps, timers, polls, network, and serialization on the UI goroutine.
All paths are relative to `perf/profiles/wait/`.

## Method

- Own binary via `perf/prof/build.sh` (go1.27.1). The shared `$T/k9s-prof` was built from an older `prof.go.in` (`K9S_PPROF_ADDR`/`K9S_FLIGHT_FILE`), so `K9S_PROF_*` had no effect for agents using it. `FlightRecorder` works: every `*.trace` except `startup.trace` came from `GET /debug/flight`.
- Driver `tools/drv-main.go` runs the harness scenarios and stamps title/first-row changes (`*/marks.json`); klog v=6 in `*/klog.log`.
- Analyzer `tools/ta-main.go` (`golang.org/x/exp/trace`): wall-clock-mapped goroutine creations, UI-goroutine run spans with wakers, per-family run/wait totals. `tools/tl.py`, `tools/kl.py` build `*.timeline.txt`.
- s = 514 pods, m = 5,050, l = 20,170. Session runs in `{s,m,l}/`, context-switch runs in `ctx-{s,m,l}/`.
- UI spans showing `cpu 0.0` with no blocking site are an analyzer artifact; totals below count only spans with CPU.

## Headline

Most of the latency users see on every size is **timer phase**, not work.
The model updater (`internal/model/table.go:203-226`) only shows data on its own
clock: the first refresh 300ms after `Watch`, then every 2s.

- Filter results appear only on the next 2s tick.
- A new view waits for the 300ms first tick even when the informer synced long
  before.
- Startup and context switch wait for the next 2s tick after the pods cache is
  populated. At m that costs about 1.3s.

The second theme is the UI goroutine doing data work synchronously: per-key
`Filter` (a deep clone plus regex over all rows), the first `refresh` on a view
switch, and context-switch config and discovery I/O.

Lock contention is **not** a factor (see F7).

---

## F1. Filter results wait for the 2s refresh tick. Hits `filter_apply`, about 95% of it at every size

- **What.** `internal/view/table.go:198` `BufferCompleted` calls
  `QueueUpdateDraw(t.Filter)`.
  `internal/ui/table.go:387` `Filter()` = `ClearSelection` + `doUpdate(filtered(Peek()))`
  + `UpdateTitle` + `SelectFirstRow`. It never calls `UpdateUI`/`buildRow`, so
  the rows and the title count (`GetRowCount`) don't change.
  The filtered rows show only when `model.Table.updater` fires
  (`refreshRate`=2s), which runs `Browser.TableDataChanged:355` and then `UpdateUI`.
- **Evidence.** `{s,m,l}/filter.timeline.txt` and `{s,m,l}/filter.uispans.txt`
  (ms from the last key).

  | size | prompt echo | `[100]` rows+title | span that changes it |
  |---|---|---|---|
  | s | 16.5 | 759.6 | QUD from `TableDataChanged` at 754.5, +5.4ms |
  | m | 35.5 | 806.3 | QUD from `TableDataChanged` at 801.5, +5.6ms |
  | l | 82.3 | 887.9 | QUD from `TableDataChanged` at 882.5, +6.2ms |

  In each trace, `TableDataChanged` QUD creations are exactly 2s apart
  (s: -1253.8 → 754.5; m: -1304.5 → 801.4; l: -1427.6 → 882.5).
  - **s.** The per-key `Filter` spans (−13 +28.7ms, and the `CmdBuff` timer QUD
    at 56.7 +7.3ms) leave the count unchanged.
- **Why the ~750ms is constant.** The harness waits `settleGap=1.2s` after the
  previous output, which is itself a tick, then types for ~50ms. The next tick is
  ~2000 − 1250 ≈ 750ms away. This is phase-locking, not work. A real user waits
  anywhere from 0 to 2s.
- **Breakdown** (ms from the last key):

  | size | fixed timer (wait for 2s tick) | network | CPU on UI goroutine | wait for UI loop |
  |---|---|---|---|---|
  | s | ~740 | 0 | ~6 (`UpdateUI`) | 0 |
  | m | ~760 (~195 of it overlaps the busy UI) | 0 | ~6 | 0 |
  | l | ~880 (overlaps ~650+323ms of useless per-key `Filter` CPU, `rxFilter.func1:189` + `Fields.Clone`) | 0 | ~6 | 0 |

  The 100ms `keyEntryDelay` (`internal/model/cmd_buff.go:15`) is not on the
  critical path, because its result is invisible too.
- **Fix.** `Filter()` should rebuild rows: `Update` + `UpdateUI` with the
  filtered data, or `model.Refresh`.
  Do `Peek()` + `rxFilter` off the UI goroutine and queue only `UpdateUI`.
  Make `CmdBuff.Add` a trailing debounce: today it arms one timer per burst and
  fires 100ms after the **first** key. `FishBuff.Add`
  (`internal/model/fish_buff.go:116`) also fires `BufferCompleted` on every key
  through `SetText(..., true)`.
- **Expected.** filter_apply ≈ 100ms debounce + filter + `UpdateUI`:
  ~110ms at s and ~200ms at l.
- **Risk.** Low to medium, because `Filter` is shared by every table view.

## F2. Startup and context switch wait for the 2s tick after the cache is ready. Hits `startup_full_table` and `context_switch`

- **What.** `Browser.Start` → `model.Table.Watch` (`internal/model/table.go:121`)
  refreshes once right away. dao `List` passes `wait=false`, so that refresh gets
  0 rows. `updater` then fires at +300ms and every 2s after that. When the pods
  informer finishes its watch-list after the 300ms tick, the table stays empty
  until the next 2s tick.
- **Evidence.** `{s,m,l}/startup.timeline.txt`, `{s,m,l}/startup.analysis.txt`,
  `ctx-{s,m,l}/ctx.timeline.txt`, `ctx-*/ctx.analysis.txt`.
  The "updater created" time is when the `model.(*Table).updater` goroutine is
  created. "Next tick" is when `parallelRender` starts.

  | run | updater created | pods "Caches populated" | next tick | title shows count | idle after cache |
  |---|---|---|---|---|---|
  | startup s | 232.8 | 349.3 | 533.5 (300ms tick) | 555 | **184** |
  | startup m | 215.5 | 1246.9 | 2531.2 (2nd 2s tick) | 2624 | **1284** |
  | startup l | 201.4 | 4322.3 | 4517.6 | 4789 | **195** |
  | ctx s | 235.2 | 339.5 | 536.8 | 554 | **197** |
  | ctx m | 250.2 | 1289.4 | 2566.5 | 2656 | **1277** |
  | ctx l | 233.5 | 4297.0 | ~4534 | 4761 | **~237** |

  At m, about half of `startup_full_table` (2627) and `context_switch` (2654) is
  this wait. Across all sizes the expected wait is uniform in 0 to 2s.
  The l startup trace shows `TableNoData` QUDs at 2501.7: an empty tick while
  the 4.1s watch-list stream is still running.
- **Fix.** Trigger the first real refresh from the informer:
  - wait on `HasSynced` with ctx in a goroutine, or register an
    `AddEventHandler`/`ResourceEventHandler` that kicks the updater (coalesced);
  - or make `updater` use a short `select` on a "synced" channel before falling
    back to the 2s period.
- **Expected.** It saves about 190ms at s/l and about 1.28s at m per
  startup/ctx switch, on average ~1s at any size.
- **Risk.** Low. Coalesce kicks so watch churn can't cause a refresh storm.

## F3. Fixed 300ms `initRefreshRate` on view switch. Hits `switch_to_deploy` (about 80% of ~340ms)

- **What.** Same mechanism as F2. `internal/model/table.go:26`
  `initRefreshRate = 300ms`. The deployments informer syncs in ~30ms, but the
  table fills only at `Watch` + 300ms.
- **Evidence.** `l/deploy.timeline.txt`, `m/deploy.timeline.txt`, `l/deploy.trace`
  (ms from Enter):
  - **l:** SSAR `CanI` done at 29, updater created at 29.4, reflector start at
    29.5, watch-list GET at 31.7, **"Caches populated" totalItems=203 at 60.3**,
    `TableDataChanged` QUD at **334.0** (= 29.4 + 300), title `deployments(all)[202]`
    at 338.9.
  - **m:** caches populated at 40.7, updater created at 28.9, QUD at 332.2,
    title at 336.7.
- **Breakdown** (l):

  | fixed timer | network/API | CPU on UI goroutine | wait for UI loop |
  |---|---|---|---|
  | **~274** (idle after sync) | ~30 (watch-list) | ~28 (key handler incl. synchronous SSAR round trip) + ~5 (`UpdateUI`) | 0 |

- **Fix.** Same as F2: kick on sync/first event instead of the fixed 300ms.
- **Expected.** switch_to_deploy drops from ~340 to ~70ms.
- **Risk.** Low.

## F4. 100ms `WaitForCacheSync` poll on the CRD informer, serial on the startup/ctx path. Hits `startup_full_table` and `context_switch` (~100ms each)

- **What.** `internal/dao/registry.go:384` `loadCRDs` calls
  `f.List(CrdGVR, ..., wait=true)`. That goes to `internal/watch/factory.go:94/140`
  `waitForCacheSync`, then to client-go `WaitForCacheSync`, which polls
  (`syncedPollPeriod=100ms`, `PollImmediateUntil`).
  The immediate check runs before the CRD watch-list returns, so it always
  sleeps a full 100ms even though sync finishes ~1.3ms later.
- **Evidence.**
  - `*/startup.timeline.txt`: "Caches populated" for CRDs comes at
    99.4 (l) / 111.2 (m) / 123.9 (s), and the next API activity
    (nodes/pods reflector start) comes ~100ms later, at 198.8 / 210.7 / 232.7.
  - `ctx-*/ctx.timeline.txt`: the same gap of 128.6 → 231.5 (s),
    135.5 → 245.2 (m), 119.6 → 225.5 (l).
  - `ctx-s/ctx.analysis.txt`: creation of `waitForCacheSync.func1` and
    `wait.poller` from `loadCRDs < Meta.LoadResources` at 126.8.
- **Breakdown.** 100ms fixed poll, about 1ms of real network.
- **Fix.** Wait on the informer's `HasSynced` with a channel or event handler,
  or call `cache.WaitForNamedCacheSync` after a short-period poll. Better still,
  load CRDs asynchronously and let the initial view start in parallel, since
  pods don't depend on CRDs.
- **Expected.** −100ms on startup and on every context switch.
- **Risk.** Low.

## F5. Context switch runs synchronously on the UI goroutine. Hits `context_switch`; the UI is frozen ~235ms at every size

- **What.** `view.(*App).switchContext` runs inside the key handler: config
  load/save, kubeconfig reload, discovery with disk-cache writes, `CanI`, and the
  F4 CRD poll.
- **Evidence** (`ctx-*/ctx.analysis.txt`):
  - **UI span.** 0 → +101ms CPU (s 99ms, m 97ms, l 77ms CPU), then blocked in
    `trackedTransport.RoundTrip` and the CRD poll until ~235–250ms.
  - **UI event stacks (s).** 54 samples `disk.(*sumDiskCache).Set`
    (discovery cache to disk), 12 `clientcmd.LoadFromFile`, 16
    `config/data.(*Dir).loadConfig`/`Save`, 6 `Aliases.LoadFile`,
    4 `EnsureBenchmarksCfgFile`.
  - **Timeline (klog).** "Config loaded" comes at +80–100ms after Enter.
- **Breakdown** (ctx-s):

  | fixed poll | network | CPU on UI goroutine | timer wait (F2) | first render |
  |---|---|---|---|---|
  | 100 (F4) | ~15 (discovery, namespaces, SSAR) | ~100 (config + disk discovery cache) | ~197 | ~17 |

  At l, F2's ~237ms plus a 4.06s watch-list stream (network/decode, outside my
  area) dominate.
- **Fix.** Move `switchContext` to a goroutine with a "switching…" flash. Write
  the disk cache asynchronously, or skip rewrite when the content is unchanged.
  Avoid the save → load round trip of k9s config on the hot path.
- **Expected.** −100ms of frozen UI, and keys stay responsive.
- **Risk.** Medium, because of reentrancy around `factory` and `Config` state.

## F6. The UI goroutine runs the first `refresh` and `UpdateUI` synchronously. Hits `switch_to_pods` (m ~102, l ~335) and key latency

- **What.**
  - **Synchronous first refresh.** `Browser.Start:181` → `Watch` → `refresh`
    runs on the UI goroutine, which blocks on `model1.parallelRender:58` while
    workers `Hydrate` all rows.
  - **Per-tick `UpdateUI`.** Every 2s tick queues `UpdateUI`
    (`internal/ui/table.go:472`): Clear + Sort + buildRow for all rows + draw.
  - **Per-key `Filter` and backspace.** The filter prompt queues `Filter`
    (Clone + rxFilter over all rows) on the UI goroutine per key (F1).
- **Evidence.**
  - `{m,l}/pods.uispans.txt` (cache warm): m key 17.5ms → blocked on `parallelRender` until 74.4 → +33.4ms draw, title 102.6. l key 27.8ms → blocked until 244.8 (~215ms Hydrate, 1322ms worker CPU) → +94.7ms, title 334.9.
  - `{s,m,l}/idle.uispans.txt`: `UpdateUI` hold per 2s tick s 5.5–12.4ms, m 19.6–26.4ms, l **64–92ms**.
  - `l/echo.uispans.txt`: each key at 20k rows = ~30ms key span + ~28ms `BufferCompleted` span; after the burst 317/146/270ms spans in `rxFilter.func1:182`/`RowEvents.Add`, then **597ms**. Echo (`*/marks.json`, keys 173ms apart) p50/p90/max: s 4.6/4.7/13.3, m 7.5/10.6/13.1, l 4.8/18.4/57.6. Echo draws before the queued QUD, so the table freezes, not the prompt.
- **Breakdown** (switch_to_pods, l):

  | fixed timer | network | CPU on UI goroutine | UI waiting on render workers |
  |---|---|---|---|
  | 0 | 0 (cache warm) | ~123 (key 28 + UpdateUI/draw 95) | ~215 (`Hydrate`) |

- **Fix.**
  - Run `Watch`'s first refresh in the updater goroutine; the view shows empty
    for a few ms.
  - Build cell content off the UI goroutine and only swap and draw on it.
  - Skip `UpdateUI` when the `TableData` didn't change.
  - Make filtering incremental, which also helps F1.
- **Risk.** Medium.

## F7. Lock contention: none worth fixing (share ~0)

- `l/mutex-idle-diff.txt`: 257ms mutex delay in ~10s idle at 20k pods, all `runtime.unlock` under render workers (`Pod.defaultRow` → `FromUnstructured`, `gatherPodMX → cosLimits`), i.e. heap/GC locks. No `TableData.mx`, `RowEvents`, `model.Table.mx` or tview lock.
- `l/block-idle-diff.txt`: only idle `selectgo`/`chanrecv` plus 0.65s `WaitGroup.Wait` (parallelRender join). `l/fgprof-idle.pb.gz`: 94% `gopark`.

## F8. Goroutines: flat, no leaks (share 0)

- `goroutine-counts.txt`: 40 idle at s/m/l; 48-49 after the first `:deploy`/`:pods`; 48 after six switches; ctx switch 40 → 48.
- The +8 is the cached deployments informer (reflector, decoder, FIFO, resync, http2 stream). Count is size-independent; `parallelRender` spawns 10 short-lived workers per tick.

---

## Per-metric stacked breakdown (ms)

| metric | fixed timer/poll | network/API | CPU on UI goroutine | UI waiting (render workers / sync I/O) | other |
|---|---|---|---|---|---|
| filter_apply s (~750) | ~740 (2s tick phase, F1) | 0 | ~35 (key + Filter + UpdateUI) | 0 | |
| filter_apply l (~830) | ~880 (F1), overlapping ~970 of useless UI `Filter` CPU | 0 | ~6 (final UpdateUI) | 0 | |
| switch_to_deploy (~340) | ~274 (300ms `initRefreshRate`, F3) | ~30 | ~33 | 0 | |
| switch_to_pods l (~335) | 0 | 0 | ~123 | ~215 (Hydrate) | |
| startup s (~555) | 100 (CRD poll F4) + 184 (tick F2) | ~55 (discovery/SSAR) + 116 (pods watch-list) | ~20 | | ~63 exec/config load |
| startup m (~2627) | 100 + **1284** | ~40 + 1031 | ~25 | ~70 Hydrate | ~80 |
| startup l (~4813) | 100 + 195 | ~30 + **4121** (pods watch-list) | ~138 | ~142 Hydrate | ~69 |
| ctx s (~554) | 100 + 197 | ~15 + 104 | ~100 (config + disk cache, F5) + 17 | | |
| ctx m (~2654) | 100 + **1277** | ~15 + 1039 | ~100 + 25 | ~70 | |
| ctx l (~4796) | 100 + 237 | ~15 + **4062** | ~77 + 79 | ~150 | |
| key echo l idle (p90 18) | 0 | 0 | 64–92 per 2s tick hold; 300–600 during filter edits | | |

The l startup/ctx watch-list stream (4.1s for 20k pods) is network plus
decode, and it is outside the wait area.

## Ranked top 5

1. **F2, the 2s tick after the cache syncs**
   (`internal/model/table.go:203-226`): ~1.28s of `startup`/`ctx` at m, ~190ms
   at s/l, uniform 0–2s in general. Fix: kick the first refresh on informer
   sync. Low risk.
2. **F1, filter waits for the 2s tick** (`internal/ui/table.go:387`,
   `internal/view/table.go:198`): about 95% of `filter_apply` at every size
   (~750ms, phase-locked by the harness). Fix: `Filter` → filtered
   `UpdateUI`, trailing debounce, filter off the UI goroutine. Low–medium risk.
3. **F3, 300ms `initRefreshRate`** on every view switch
   (`internal/model/table.go:26`): ~274 of ~340ms in `switch_to_deploy`. Same
   fix as F2. Low risk.
4. **F6, synchronous first refresh + `UpdateUI` on the UI goroutine**
   (`view/browser.go:181`, `ui/table.go:472`): ~340ms of `switch_to_pods` at l,
   64–92ms UI hold per 2s tick, 300–600ms freezes during filter edits. Medium
   risk.
5. **F4 + F5, 100ms CRD `WaitForCacheSync` poll + synchronous `switchContext`**
   (`dao/registry.go:384`, `watch/factory.go:140`, `view/app.go switchContext`):
   −100ms on startup and every ctx switch, plus ~100ms of frozen UI on ctx switch.
   Low/medium risk.
