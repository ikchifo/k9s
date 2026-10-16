# 09b: reuse tview cells across refreshes

`UpdateUI` did `Clear()` and then `tview.NewTableCell` + `SetCell` for every
cell, so each tick at 20k rows x 20 cols threw away ~400k cells and grew the
row slices again (NewTableCell + SetCell were ~60% of tick allocs).

Now `UpdateUI` keeps the previous cells and `buildRow` resets each one in
place to `NewTableCell` defaults (so attributes like ShowDeleted's dim are
dropped exactly as before). The table is still cleared when the visible
column count changes, when it holds more than 2x the needed rows (frees
memory after a big shrink), or when any row has fewer fields than the
header (would leave stale trailing cells). Extra rows are removed from the
end; a row whose original can't be found is reset to empty, as before.

`TestTableUpdateReusesCells` checks reuse gives the same cells as a fresh
table for shrink, grow, fewer columns, short rows, same shape and empty,
after a ShowDeleted. Mutating each guard makes it fail.

Remaining allocs/tick are mostly `Pad` strings and the Peek clone.
