# 09: UI table refresh, sort and selection at 20k rows

Benchmarks in `internal/ui/table_bench_test.go` (20k synthetic pod rows,
real Pod header and colorer, model that clones on `Peek` like
`model.Table`):

- `TableRefresh`: `ui.Table.Refresh` = Peek + Update + UpdateUI (a tick).
- `TableSortToggle`: `SortColCmd("AGE")` key (sort toggle).
- `TableSelectedRow`: `GetSelectedRow(GetSelectedItem())` (plugin env, etc).
- `TableColumnMove`: `SelectNextColumn` (left/right column cursor).

`bench-base.txt` is the baseline at c3bc88f9 with `-count 10 -cpu 4`.
Each change has its own dir: 09a, 09b, ...
