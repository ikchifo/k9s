# Render path: model data -> terminal

Area: `browser.TableDataChanged` -> `ui.Table.Update/UpdateUI/buildRow` -> `model1` (Filter/Sort/Clone) -> tview `Table` -> tcell `Show`.
All artifacts are under `perf/profiles/render/`. Module paths are relative to `$(go env GOMODCACHE)/github.com/derailed/`.

## Setup

- Profiled binary: `perf/prof/build.sh $T/k9s-prof-render`. Harness rebuilt from `perf/harness`, because the prebuilt `$T/harness` doesn't forward `K9S_PROF_*`.
- Live profile at l: harness session run with `-idle 60s`, pprof on 127.0.0.1:6064. Artifacts: CPU 30s (`idle_l_cpu30s.pb.gz`), allocs t0/t1 (`idle_l_allocs_delta_cum.txt`), heap, 6s trace.
- Microbench at 20k rows x 26 cols: 1000 real team-01 pods from the l cluster, replicated across 20 namespaces and rendered with `render.Pod`. Colorer is `Pod.ColorerFunc`, the sim screen is 200x50, and the run is M1 Max with `-benchtime=20x -count=3`. The bench was injected with `go test -overlay` (nothing added to the repo). Source: `bench_render_test.go.txt`. Results: `bench_l20k.txt`, `bench_sort_order.txt`. Bench CPU/alloc profiles: `bench_tick_*`.
- Harness metrics: `harness_l_session.log` (l, profiled, 1 run), `harness_s_session_x5.log`, `harness_s_refresh6s_x3.log` (harness copy with refresh 6s).

## Headline numbers

| what | value | artifact |
|---|---|---|
| l idle CPU / refresh_cpu | 61.1% / 1221 ms per tick | harness_l_session.log |
| UpdateUI share of live idle CPU at l | 5.34% (0.85s of 15.92s) | idle_l_cpu_top_cum.txt |
| Pod hydration share (other area, for scale) | 56.85% | idle_l_cpu_top_cum.txt |
| UpdateUI share of live idle allocs at l | 9.30% (430 MB / 30s) | idle_l_allocs_delta_cum.txt |
| tview `Application.draw` share | 0.5% | idle_l_cpu_top_cum.txt |
| refresh_bytes | 33.8 B/tick (l), 43.9 B (s) | harness logs |
| UpdateUI, all 20k rows | 33.5 ms, 34.1 MB, 460k allocs | bench_l20k.txt |
| UpdateUI, only 48 visible rows | 0.081 ms, 85 KB, 1235 allocs (~410x less) | bench_l20k.txt |
| Draw+Show, 20k vs 48 rows loaded | 2.7 ms vs 2.5 ms (independent of N) | bench_l20k.txt |

Answer to Q1: about 99.8% of the cell-building work per tick is for rows nobody sees (20k built, ~45 shown). Q4: the screen is not redrawn in full. tcell diffs the output and draw is cheap, so the cost sits upstream in building cells.

---

## Findings, ranked by share

### 1. `UpdateUI` rebuilds every row as tview cells on every tick (Q1, Q5)

**What**
- `internal/ui/table.go:472-492` `UpdateUI`:
  - `t.Clear()` (473) drops all cells.
  - `ComputeMaxColumns` (477) runs over all rows.
  - `RowsRange` -> `buildRow` (479-488) runs for every row.
- `buildRow` (494-548) does this per cell:
  - `Deltas` (524)
  - `Decorator`
  - `formatCell`/`Pad` (531)
  - `tview.NewTableCell` (534), one heap object per cell
  - colorer (537), which is called per **cell**, not per row
  - `SetCell` (545)
- tview: `NewTableCell` is at `tview@v0.8.5/table.go:61`, `Clear` at :318 (sets `t.cells=nil`, so everything becomes garbage), `SetCell` at :472 (grows slices with append).

**Evidence**
- Live l (`idle_l_cpu_updateui_focus.txt`): UpdateUI is 5.34% cum.
  - buildRow 2.64%
  - SetCell 0.94%
  - formatCell 0.57%
  - NewTableCell 0.44%
  - Pad 0.38%
  - Pod.ColorerFunc 0.31%
- Allocs over 30s idle (`idle_l_allocs_delta_cum.txt`):
  - NewTableCell 286 MB (6.19%)
  - SetCell 81.6 MB (1.76%)
  - Pad 42 MB (0.91%)
- Bench Tick profile (`bench_tick_cpu_k9s.txt`):
  - UpdateUI 33.8%, buildRow 17.3%
  - SetCell 4.1%, colorer 3.2%, NewTableCell 2.9%, formatCell 2.7%
  - `Header.IndexOf` 2.4%: `Pod.ColorerFunc` (`internal/render/pod.go:96-122`) looks up "STATUS" by name, and `IsValid` (`internal/model1/helpers.go:94-104`) looks up "VALID", per cell
  - `runtime.madvise` 30.8% plus about 14% GC, from the churn
- Alloc side (`bench_tick_alloc_cum.txt`): NewTableCell is 35.8% of bytes.
- `BuildRowsOnly`: 23.6 ms and ~18 allocs per row at 20k (`bench_l20k.txt`).

**Metrics hit:** idle_cpu, refresh_cpu, idle_rss (GC churn), and sort_toggle / switch_to_pods, since both go through `UpdateUI`. Direct share: about 5% of l idle CPU and 9% of allocs. The indirect GC share is larger: the bench shows ~45% runtime/GC per tick, which is mostly driven by these allocations.

**Fix (pick one, in order of risk)**
- a. Call the colorer once per row instead of per cell, and resolve "STATUS"/"VALID" column indexes once per `UpdateUI`. Saves ~3-5% of tick CPU. Trivial risk.
- b. Reuse cells. Keep `t.cells` and, instead of `Clear()` plus `NewTableCell`, overwrite existing `*TableCell` in place (`GetCell(r,c)` then `SetText`/`SetTextColor`). Truncate extra rows with `RemoveRow` or by limiting the row count. This removes ~520k allocs per tick at 20k and most of the 35.8% alloc share. Low risk: tview supports mutating returned cells.
- c. Virtualize: build only rows in `[rowOffset, rowOffset+height]` plus a margin. The fork has **no** `TableContent`/`SetContent` (grep for `TableContent` in `tview@v0.8.5` finds nothing), so this needs either a fork change (port upstream tview's `TableContent`, ~150 LOC) or a k9s-side scroll-window proxy. Bench: 33.5 ms -> 0.08 ms per tick. Risk is medium: selection, marks, `GetRowCount`-based titles, and search/scroll offsets all assume every row exists as a cell.

### 2. Each tick re-sorts the freshly cloned model order (Q2)

**What**
- `UpdateUI` calls `cdata.Sort(...)` (`internal/ui/table.go:475`) every tick.
- The data comes from `model.Table.Peek()` (`internal/model/table.go:196-201`), a deep `Clone` in model order. So every tick sorts from model order, not from the already-sorted state.
- `RowEvents.Sort` (`internal/model1/row_event.go:267-283`): pdqsort, then `reindex` rewrites the id->index map for all rows.
- `Less` (`internal/model1/helpers.go:187-204`):
  - Strings go through `sortorder.NaturalCompare`, with a tie fallback to `NaturalLess(id1,id2)` (199-201).
  - Durations: `durationToSeconds` (134-164) parses both sides on every compare.
  - Capacity: `resource.MustParse` on both sides per compare (178-184).
  - Numbers: `lessNumber` does 2x `strings.ReplaceAll` per compare (217-221).
  - No heap allocs (1 alloc per Sort, `bench_l20k.txt`), but a parse on every compare.

**Evidence**
- `bench_sort_order.txt`:
  - Sort from model order after Clone, as each tick does: NAMESPACE ~21.8 ms, AGE ~21.6 ms. Shuffled: 23-26 ms.
  - Already sorted: 3.2 ms (20,011 Less calls).
  - Unsorted -> sorted: 281k (NAMESPACE) to 307k (AGE) Less calls.
  - Toggling asc/desc on sorted data: 3.7-4.9 ms.
- Live l: Sort 2.39% of idle CPU. Less 2.26%, NaturalCompare 2.01% (`idle_l_cpu_top_cum.txt`).
- Bench tick: Sort 15.2%, NaturalCompare 11.75% (`bench_tick_cpu_k9s.txt`).

**Metrics hit:** refresh_cpu and sort_toggle (98.5 ms at l, 13.1 ms at s). Sort is ~22 ms of the ~55 ms CPU in the bench equivalent of `Refresh`. The rest of the l number is GC and contention with hydration on a 1.8 GiB heap.

**Fix**
- a. Precompute a sort key per row once per Sort (decorate-sort-undecorate): parse the duration, number, or quantity to `int64`/`float64` once, then compare keys. Cuts the ~280k parses to 20k. Expected: sort roughly 2-4x faster for AGE/RESTARTS/CPU/MEM columns. String columns still pay NaturalCompare. Low risk.
- b. Keep the model's `RowEvents` in the last sort order, so the per-tick Sort hits the already-sorted fast path (3.2 ms vs 21.8 ms). Low to medium risk: the model must not reorder rows in `reconcile`.

### 3. Deep `Peek()` clone on every tick and on every selection lookup (Q6)

**What**
- `model.Table.Peek` deep-clones `TableData` (`internal/model1/table_data.go:389-399`, `Fields.Clone` per row).
- Callers:
  - every refresh (`internal/model/table.go:229-247`)
  - `BufferActive` (`internal/view/browser.go:230-253`)
  - `ui.Table.Refresh` (`internal/ui/table.go:586`), used by the sort commands
  - `Filter` (389)
  - `GetFilteredData` (406)
  - `GetSelectedRow` (596), which clones 20k rows to find one row

**Evidence**
- `PeekClone`: 3.6 ms, 12.1 MB, 20,071 allocs (`bench_l20k.txt`).
- Live l allocs: Clone 155 MB in 30s, 3.36% (`idle_l_allocs_delta_cum.txt`).
- Bench tick: Clone 19.7% of bytes (`bench_tick_alloc_cum.txt`).

**Metrics hit:** idle_rss / GC and refresh_cpu, ~2-4%. Also a 3.6 ms CPU spike plus 12 MB per key/selection action at l.

**Fix**
- Make `GetSelectedRow` look up under read lock without cloning (add a `model.Table.FindRow(id)`).
- Treat the published `TableData` as immutable (copy-on-write in `reconcile`) so `Peek` can return a shared pointer.

Risk: low for the first, medium for the second (data races must be checked with `-race`).

### 4. The filter result waits for the next refresh tick; the regex filter is expensive (Q3)

**What**
- Typing fills the cmd buffer. The 100 ms debounce (`internal/model/cmd_buff.go:15,155-160`) then calls `BufferCompleted` -> `Filter` (`internal/view/table.go:198-202`, `internal/ui/table.go:387-392`).
- `Filter` only calls `doUpdate(filtered(Peek()))`. Neither calls `UpdateUI`, so the tview cells and the title count (`styleTitle` reads `GetRowCount()`, 677-681) stay stale until the next `TableDataChanged` tick.
- `rxFilter` (`internal/model1/table_data.go:166-198`) allocates a `[]string` per row, then `strings.Join`, then `rx.MatchString`.
- With no filter active, `Filter` returns early with a shallow wrapper (149-151): about 0.8 us, no row copy. Q3: no copy on idle ticks.

**Evidence**
- `filter_apply` is 751 ms (s, p90 757, `harness_s_session_x5.log`) and 795 ms at l, almost the same despite 25x the rows.
- Harness copy with `refreshRate` 6s: `filter_apply` 4754 ms (p90 4763), while `filter_key_echo` is unchanged at 9.9 ms (`harness_s_refresh6s_x3.log`).
- So `filter_apply` is tick wait (uniform 0-refresh period after the last key), not compute.
- Compute at 20k rows (`bench_l20k.txt`):
  - rx filter "svc-1-": 128 ms, 14.6 MB, 40k allocs
  - fuzzy: 8.8 ms
  - none: ~0.8 us
- `FilterInput` has no callers (`rg 'FilterInput\('` finds only the definition). The filter runs once per debounced `BufferCompleted`, about 130 ms on the UI goroutine at l. When a key lands while it runs, the echo waits. That fits `filter_key_echo` p90 151 ms at l vs 16.5 ms at s (`harness_l_session.log`).

**Metrics hit:** filter_apply (~100% tick wait) and filter_key_echo p90 at l (~130 ms per filter pass on the UI goroutine).

**Fix**
- a. In `ui.Table.Filter` (387) call `UpdateUI(cdata, data)` after `doUpdate`, or have `BufferCompleted` trigger it. Expected filter_apply: ~750 ms -> ~100 ms debounce + build (~5 ms at 100 rows). Low risk.
- b. Run `rxFilter` off the UI goroutine and queue only the result. That removes the echo stalls.
- c. In `rxFilter`, reuse one `strings.Builder`/`[]byte` across rows, or match each visible field separately (keeping `spacer` semantics by checking the field sequence). Removes the 40k allocs; expected ~2x.

Risk: low for a and b. c needs care that cross-field regexes still match.

### 5. Draw and terminal output are already cheap (Q4)

**What**
- tview `Table.Draw` (`tview@v0.8.5/table.go:640`) only indexes visible rows: the `indexRow` loop is at :748, and fixed rows come first, then `rowOffset` until the height is full.
- Column widths are computed over the visible rows only. `evaluateAllRows` (:742, :805) is set only by `SetEvaluateAllRows` (:431), which k9s never calls.
- k9s pads to all-row max widths itself via `ComputeMaxColumns` (`internal/ui/padding.go:18-39`): 0.63 ms, 1 alloc at 20k.
- tcell `Show` (`tcell/v2@v2.3.1-rc.4/tscreen.go:777`) -> `draw` (:805) walks w*h cells but emits only dirty ones (`cell.go:97` Dirty).

**Evidence**
- Draw+Show takes 2.7 ms with 20k rows loaded vs 2.5 ms with 48 (`bench_l20k.txt`).
- Live `Application.draw` is 0.5% (`idle_l_cpu_top_cum.txt`).
- refresh_bytes is 33.8 B per tick at l.

**Metrics hit:** none material.

**Fix:** none needed. The only nit is the per-draw `cellsByBackgroundColor` map (:980).

### 6. Title and colors (Q5)

**What**
- `render.AsThousands` (`internal/render/helpers.go:61-64`) calls `message.NewPrinter` on every call; `styleTitle` (`internal/ui/table.go:677-730`) uses it twice per update.

**Evidence**
- AsThousands: 1.5 us, 10 allocs. With a cached printer: 1-2 us, 7 allocs. styleTitle: 3 us, 33 allocs (`bench_l20k.txt`).
- The colorer lookups per cell are covered in finding 1 (IndexOf 2.4% plus IsValid 1.9% of bench tick CPU). tcell.Style construction doesn't show up in the profiles.

**Metrics hit:** negligible (<0.01% per tick).

**Fix:** none worth the churn, except the per-row colorer from finding 1a.

---

## Per-tick cost model at 20k rows (bench, M1 Max)

| step | ms | allocs |
|---|---|---|
| Peek clone | 3.6 | 20k |
| Filter (none) | ~0 | 7 |
| Sort: already sorted / from model order | 3.2 / ~21.8 | 1 |
| ComputeMaxColumns | 0.6 | 1 |
| buildRow x20k (cells, pad, color, SetCell) | 23.6 | 365k |
| UpdateUI total (incl. Clear, Sort, buildRow) | 33.5 | 460k |
| Draw + Show (separate frame) | 2.7 | 9.5k |
| **Tick total (Clone+Update+UpdateUI)** | **52.9** | **480k** |

The rows overlap and don't add up. Sort cost depends on input order: the bench Tick sorts a clone, which falls between the two Sort figures.

At l, live `UpdateUI` plus the refresh plumbing is ~8% of idle CPU and ~15% of allocs. Hydration (other area) is ~57%. Fixing render alone won't fix the l idle CPU, but it removes most of the GC churn on the UI goroutine. It also makes sort_toggle and filter_apply interactive.

## Notes / gotchas

- The prebuilt `$T/k9s-prof` reads `K9S_PPROF_ADDR`, not `K9S_PROF_ADDR`. I rebuilt it as `$T/k9s-prof-render`.
- The harness resolves `-kube perf/.kube` relative to the CWD. Run it from the repo root.
