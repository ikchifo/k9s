# 03 first-refresh

`model.Table.updater` showed informer data only on its own clock: 300ms after `Watch`, then every 2s.
On view switch, a synced informer still waited 300ms.
At startup or context switch, a pods informer that synced after the 300ms tick waited up to 2s (~1.28s at m, see `perf/reports/wait.md` F2/F3).

## Change

- `Watch` grabs the informer's `HasSyncedChecker().Done()` before the first refresh, and the updater also selects on it.
  When it fires, the updater refreshes right away and goes back to the regular rate.
  `syncedChan` returns nil, which keeps the old timer-only behaviour, for:
  - single-instance tables;
  - `dao.Table` (server-side table) resources;
  - non-k8s metas (k9s/helm);
  - a missing factory;
  - an informer that has already synced.
- The `inUpdate` CAS flag became a `sync.Mutex` with `TryLock`, so the old drop-if-busy behaviour stays.
  Sync can fire while the first `Watch` refresh (or a `Refresh` from the UI) is still in flight. That refresh may have listed before the store was filled.
  The updater now waits for it to finish (`awaitRefresh`) instead of having its refresh silently dropped, which would leave the table empty for another 2s.
- `initRefreshRate` (300ms) stays as the fallback when there is no sync channel, and as the retry backoff start.
- Context switch: `switchContext` -> `initFactory` (Terminate + Start) -> `gotoResource` -> `Browser.Start` -> `Watch` with a fresh ctx.
  `CanForResource` creates a new informer on the new factory, so its sync channel is fresh and unsynced.
- No leaks: the updater goroutine exits on ctx cancel as before.
  Nothing else is spawned, and the `time.After` timers are unchanged from master.

## Timings (`test-timings.txt`, `go test -count=10 -run TestTableWatch ./internal/model/`)

Fake informer whose sync channel the test closes. Deadline 150ms (< `initRefreshRate`):

| test | refresh with rows after sync close |
|---|---|
| `TestTableWatchRefreshesOnSync` | 0.16-2.3ms (old: 300ms tick, or the next 2s tick) |
| `TestTableWatchSyncWaitsInFlightRefresh` (refresh held in flight at sync) | 0.36-3.7ms; fails (no refresh in 150ms) without `awaitRefresh` |

Latency, not CPU, so no benchstat and no pprof diff. The lead measures startup, ctx switch and view switch with the harness.

## Harness (macOS, interleaved, 5 runs)

Binary with both 03b (factory sync) and 03 (this change) vs master 7453f748;
labels 02-master-i2 vs 03-first-refresh. `harness.txt`, `benchstat-harness.txt`:

| metric | s | m | l |
|---|---|---|---|
| startup_full_table | 552 -> 250ms (-55%) | 2643 -> 1253ms (-53%) | 4806 -> 4454ms (-7%) |
| context_switch | 557 -> 245ms (-56%) | 2642 -> 1245ms (-53%) | 4780 -> 4510ms (-6%) |
| switch_to_deploy | 359 -> 54ms (-85%) | 371 -> 72ms (-81%) | 362 -> 86ms (-76%) |

All p=0.008. At l, startup is still dominated by streaming 20k pods (see
perf/reports/api.md). CPU, RSS, filter and sort within noise.
