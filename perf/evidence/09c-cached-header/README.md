# 09c: column cursor and sort keys read the displayed header

`initSelectedColumn`, `moveSelectedColumn` (shift-left/right) and
`SortSelectedColumn` called `GetFilteredData()`, i.e. a deep clone of the
whole model plus a filter pass, only to read the header. A sort toggle did
this twice (once in `SortColCmd`, once in `doUpdate`).

`UpdateUI` now keeps the header it just drew; the column cursor paths use
it, and `doUpdate` passes the header of the data it already holds. Filtering
never changes the header (`NewTableDataFromTable` shares it), so the column
positions are the same. Header is only touched on the UI goroutine.

Column move at 20k rows: 3.6ms/12MB -> 3us/2KB. Sort toggle: one clone fewer.
