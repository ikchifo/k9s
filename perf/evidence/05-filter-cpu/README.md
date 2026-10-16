# 05: cheap filter pass over rows

`model1.TableData.rxFilter` joined the visible fields of every row into a
fresh string and ran a case-insensitive regex per row: 2 allocs/row, ~40k
allocs and 8-12MB per pass at 20k rows, 40-130ms on the UI goroutine.

Change (internal/model1/table_data.go):
- Queries with no regex metacharacters and no non-ASCII bytes (the common
  pod-name style query) are matched with `bytes.Contains` against a
  case-folded copy of the row, built in one buffer reused across rows.
  ASCII is lowercased; the only two non-ASCII runes `(?i)` treats as ASCII
  letters (U+017F, U+212A) are folded too, so results are byte-identical
  to the old `(?i)` regex. Queries with metachars or non-ASCII runes still
  compile a regex, but match the reused joined buffer (`rx.Match`) instead
  of a fresh `strings.Join` per row.
- Space no-op, `!` inverse, empty and invalid-regex behaviour unchanged.

Proof of equal behaviour:
- `TestTableDataFilter` (table_data_test.go) covers plain, case both ways,
  regex, inverse, regex+inverse, `-`/`=` special chars, AGE/hidden column
  exclusion, wide column inclusion, space no-op, label-selector and fuzzy
  dispatch, and the U+017F/U+212A fold cases. It was written first and
  passes unchanged before and after.
- A local differential fuzz (old algorithm vs new over 3k random tables x
  47 queries, random field soups with metachars/unicode/empty fields,
  inverse and compile-fail queries) found zero mismatches; not committed.

`ui.Table.FilterInput` still runs `doUpdate(filtered(Peek()))` per rune,
but it has no callers anywhere (`rg 'FilterInput'` finds only the
definition), so there is no per-key filter pass to remove; left untouched.

## Benchmarks (internal/model1, 20k pod-like rows, 13 cols, -cpu 4, n=10)

| sub-bench | before | after | sec/op | B/op | allocs/op |
|---|---|---|---|---|---|
| plain "nginx-7" | 99.4ms | 7.0ms | -93% | -86% | 40074 -> 54 |
| upper "NGINX-7" | 129.2ms | 6.9ms | -95% | -86% | 40074 -> 54 |
| inverse "!running" | 38.8ms | 6.4ms | -83% | -86% | 40074 -> 54 |
| regex "ngin[x]" | 44.1ms | 37.8ms | -14% | -56% | 40146 -> 144 |
| miss "no-such-pod" | 56.9ms | 7.5ms | -87% | -86% | 40075 -> 54 |

geomean sec/op 65.98m -> 10.22m (-85%). See `benchstat.txt`,
`bench-before.txt`, `bench-after.txt`.

## pprof diff (pprof-diff.txt, before/after.pb.gz)

Regex work collapses: `tryBacktrack` -1.9s, `MatchRunePos` -1.0s,
`SimpleFold` -0.6s, `inputString.step` -0.5s; replaced by `appendFold`
3.4s and `bytes.Index`/`IndexByteString` ~1.5s across the profiled runs.

## Harness (macOS, interleaved with master 7453f748, 5 runs)

`harness.txt` / `benchstat-harness.txt` (labels 02-master-i vs 05-filter-cpu):
filter_apply l 319 -> 213ms (-33%, p=0.008), m 216 -> 184ms (-15%, p=0.008),
s 184 -> 173ms (~). Other metrics within noise.
