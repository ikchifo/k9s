# 08 row cache

Pod rows are reused when UID and resourceVersion match the last tick.
AGE, LAST RESTART, VS and metric columns are recomputed every tick.

## Benchmark

`benchstat.txt`: `BenchmarkPodRenderTick`, 20k pods, second tick, nothing changed.

| | before | after |
|---|---|---|
| sec/op | 307.76ms | 16.52ms (-94.6%) |
| B/op | 426.18Mi | 23.53Mi (-94.5%) |
| allocs/op | 6610.2k | 160.2k (-97.6%) |

`pprof-diff.txt`: `structFromUnstructured` -10.7s (~48% of the old profile), GC down.

## Harness (Linux GKE pods, interleaved with master c3bc88f9 + 06-11, 5 runs)

`benchstat-harness-linux.txt`, results `perf/results/linux/<size>/{head-r4,08-row-cache}`:

| size | refresh_cpu | idle_cpu |
|---|---|---|
| s | 74 -> 23ms (-69%, p=0.008) | 3.7 -> 1.1% |
| m | 637 -> 123ms (-81%, p=0.008) | 31.8 -> 6.1% |
| l | 2379 -> 417ms (-82%, p=0.008) | 118.9 -> 20.8% |

RSS l 1681 -> 1511MiB. `switch_to_pods` l 352 -> 451ms, not significant
(p=0.69); the first render after a switch fills the cache.
Startup, context switch, filter and sort: within noise.
