# 09a: call the row colorer once per row

`buildRow` called the colorer (and the mark check) for every cell, though
the result only depends on the row. Pod's colorer looks up STATUS and VALID
by name each call, so 26 lookups per row became 1. Colorers in
internal/render are pure (no writes to the row event), so output is
identical. ~10-12% off a tick and a sort toggle; allocs unchanged.
