# Should k9s move to Rust (kube-rs + ratatui)?

No. The remaining delay is mostly the data format and the amount of work,
not Go. A Rust rewrite would remove at most ~1/3 of the slowest remaining
path, and Go has a cheaper way to get more than that.

## What is left (final build, macOS M1 Max, l = 20k pods)

| path | now | Go CPU on the critical path |
|---|---|---|
| startup / context switch | 3.0 / 3.1s | decode goroutine Running 2.7s of ~3.0s (trace) |
| idle | 9.4% of a core | 7.9% in profile; 61% of it runtime (madvise 27%, GC 15%, malloc 5%) |
| sort toggle | 87ms | row build + sort |
| filter apply | 200ms | 100ms of it is the debounce, by design |
| key echo, scroll, `:deploy` | 7, 10, 81ms | within budget |

Only startup/context switch is dominated by Go CPU. Everything interactive is
already within or near budget, so Amdahl gives Rust little to work with there.

## Prototype: the single hottest path

`perf/rust-proto/` decodes a 20k-pod JSON watch stream (79MB, one `po_full.json`
pod per event). `gobench/` decodes the same bytes in Go with the same targets.
Raw output in `perf/evidence/rust/`. M1 Max, 10 runs each.

| decode target | Go | Rust (serde_json) | Go / Rust |
|---|---|---|---|
| generic map (what k9s needs today) | 664ms, 473MiB, 10.6M allocs | 326ms | 2.0x |
| typed `Pod` struct | 878ms, 310MiB, 4.3M allocs | 259ms | 3.4x |
| typed protobuf, Go (`perf/evidence/11-startup/bench`) | ~120ms | — | — |

## Amdahl on startup at l (3.0s)

- Wire floor: raw 108MB JSON WatchList takes ~1.9s with curl
  (`perf/evidence/11-startup/README.md`). No language change removes it.
- Decode is ~2.7s of goroutine CPU, overlapping the wire. Rust at 2x on the
  parse and with no GC assists brings that close to the wire time.
- Best case Rust (JSON, generic objects), estimated: ~2.0-2.2s. Saves ~0.8-1.0s (27-33%).
- Go with a typed protobuf pods informer: measured sync 0.80s in the probe
  (`perf/evidence/11-startup/README.md`), so roughly 1s to a full table.
  Saves ~2s (~65%), with no rewrite.

Linux numbers are ~2x macOS for this path (6.2s), because it runs on one
slower core; the ratios above hold.

## Amdahl on idle

Removing GC entirely would cut at most the runtime share: ~45% of 9.4%, so
~5% of a core at l. The same can be had in Go by dropping the per-tick sort
(id tie-breaks are 16% of idle CPU) and the per-tick table clone.

## Cost of a rewrite

- ~51.6k lines of non-test Go in `internal/` and `cmd/`.
- Go-only libraries with no Rust equivalent:
  - Helm v3 (helm view, releases, history, rollback);
  - kubectl (describe printers, drain, logs, exec, port-forward, cp, scale,
    rollout);
  - anchore grype/syft image vulnerability scan (VS column);
  - metrics-server client.
- User-facing compatibility:
  - `config.yaml`, skins, aliases, hotkeys, plugins, custom views and
    benchmarks config;
  - the exact keymap and screen layout of the tview widgets.
- client-go auth paths (exec plugins, OIDC, GKE/EKS/AKS) would need re-checking
  in kube-rs.

## Recommendation

1. Stay on Go.
2. Next big step: a typed protobuf informer for pods (~65% of l startup),
   behind the render layer. High risk, so it's a separate design.
3. Smaller Go items next: sort only when data or sort column changed; drop id
   tie-break cost; per-context factory cache for switching back.
