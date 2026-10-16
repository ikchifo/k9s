# Real-cluster startup: parallel permission checks (round 2)

Cluster: user's GKE cluster (exec-credential plugin, 0.2-0.6s RTT,
~2.4s cold gcloud token). Identifiers redacted.
Default view: contexts. Machine load noisy; numbers are single-run medians
from interleaved runs with matched credential/disk-cache state.

Code: `ae1e9cbf`, `a7bae40e`, `27a282ff`, `7dc8cf96` on master.

## Root causes found

1. **Serial SSAR chain.** Cluster info fetched version -> nodes CanI ->
   node-metrics CanI -> nodes list -> metrics list, one round trip at a
   time (~250-650ms each). CRDs and metrics fired duplicate concurrent
   reviews (LRU only caches after completion).
2. **Three concurrent ClusterInfo.Refresh callers** at startup
   (Init goroutine, UpdateClusterInfo/Reset, clusterUpdater first pass),
   each running its own metrics chain -> up to three parallel
   node-metrics lists.
3. **Live `/api` + `/apis` on every warm start.** client-go skips caching
   empty discovery responses, so `external.metrics.k8s.io/v1beta1` never got
   a `serverresources.json`. The per-GV disk miss fell through to the
   memcache delegate, forcing a full live aggregated refresh (two
   sequential round trips) despite the 6h disk TTL.
4. **Concurrent `/version` duplication.** The connectivity probe, cluster
   info version and the 32s-config fetch each posted their own `/version`.

## Changes

- `singleflight` on CanI, ServerVersion/CheckConnectivity, ClusterInfo.Refresh
  and MetricsServer node/pod metrics fetches; concurrent callers share one
  round trip (LRU caches still serve repeats).
- `Cluster.Metrics` runs nodes and node-metrics fetches concurrently;
  `ClusterInfo.Refresh` runs version and metrics concurrently.
- Seed empty `serverresources.json` for group versions listed in a fresh
  `servergroups.json` but missing on disk, so warm starts never trigger the
  live refresh. Entries carry a fresh mtime, expire with the 6h TTL and are
  ignored after `Invalidate()`.
- Queue the cluster info panel Init before the first refresh (the fast
  refresh could beat the panel draw, which then cleared it).

## Timelines (tmux first-paint milestones)

| binary | first_frame | rows | cluster_info |
|---|---|---|---|
| base (morning, cold creds) | 4.79-4.93 | 5.47-5.59 | 5.72-6.13 |
| pre-round-2 (warm creds+disk) | 2.83-3.36 | 3.44-4.02 | 4.95-5.68 |
| round-2 (warm creds+disk)    | 1.43-2.32 | 2.04-2.93 | 3.10-3.17 |
| round-2 (cold creds)         | 4.65-5.32 | = first  | = first |

Wire deltas in matched runs: no live `/api`+`/apis`, 1 instead of 2-3
duplicated SSARs, 1 instead of 3 metrics lists, 1-2 instead of 3 `/version`.

API timeline, round-2 warm run (from klog v=6):

```
0.10-0.96  probe /version + ns-list SSAR (parallel)
0.96-1.36  namespaces list
1.39-2.04  3x SSAR + /version (all parallel: nodes, metrics-nodes, CRDs)
1.80-2.06  node metrics list
2.02-2.45  nodes + CRD informers (parallel)
rows at 2.04
```

Cold-cred runs are dominated by the ~2.4s exec-credential plugin first
token fetch (inside the first request); everything after it is parallel.
