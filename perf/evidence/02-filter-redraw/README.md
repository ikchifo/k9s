# 02: show filter results right away

Before: `Filter()` updated the model copy but never rebuilt the table rows,
so results only showed on the next 2s refresh. Typing also ran a full
clone and regex pass over every row on each key, then threw the result away.

Change:
- `ui.Table.Filter` calls `Refresh()` (filter + `UpdateUI`).
- `view.Table.BufferCompleted` waits 100ms after the last key (trailing
  debounce), so the rebuild runs once when typing stops instead of mid-typing.

A first try without the trailing debounce made key echo worse at l
(33 -> 106ms): `CmdBuff` fires on the first key of a burst, so the full
rebuild landed between keys.

## Harness (5 runs, interleaved with 01-pgo; `harness.txt`)

| metric | size | before | after | delta |
|---|---|---|---|---|
| filter_apply | s | 731ms | 177ms | -76% |
| filter_apply | m | 784ms | 218ms | -72% |
| filter_apply | l | 831ms | 315ms | -62% |
| filter_key_echo | l | 34ms (p90 149) | 7.4ms (p90 15) | -79% |
| filter_key_echo | m | 14ms (p90 33) | 7.4ms (p90 16) | -49% |
| others | all | | | within noise |

`filter_apply` raw at l: before 805-925, after 301-320ms. No overlap.

## benchstat (`benchstat-harness.txt`)

Harness samples as benchmark lines (`perf/tobench.sh`), `filter_key_echo`
n=30, `filter_apply` n=5: all filter rows p<=0.008, everything else `~`.

## pprof diff (`pprof-diff.txt`)

CPU while typing the filter at l, 2 x 5s per binary, profiling builds of
01 and 02 (`before.filter.pb.gz`, `after.filter.pb.gz`).
Total samples 7.88s -> 5.67s (-28%). `rxFilter` 1.74s -> 0.92s:
the per-key regex passes are gone, one remains per filter.

## Take

Filter now lands ~100ms debounce + one rebuild after the last key.
At l, the remaining ~200ms is the regex pass and `UpdateUI` over 20k rows
(changes 05 and later).

Race: `go test -race ./...` fails the same 5 tests (TestUpdateLogs, TestFlash x2,
TestFlashBurst, TestShowPrompt) before and after; those races are pre-existing.
