# Real-cluster startup: round 3 — last serial chunks (parallel agents)

Code: `06f6d044`, `9f2b2ca0` on 00-perf (base 7dc8cf96 = round 2).
Config: `disablePodCounting: true` (user k9s config; was false).

## Changes

1. **Namespace fetch speculative parallel** (`06f6d044`): `ValidNamespaceNames`
   fired its CanI review, waited, then listed namespaces — two sequential
   round trips on the first-frame path. The List now starts concurrently
   with the review (discarded on deny; deny semantics unchanged), and the
   whole body shares a singleflight keyed on the cache entry.
2. **Latest-rev check off the header path** (`9f2b2ca0`): the GitHub
   releases probe ran after version+metrics and gated the header draw.
   Now a third WaitGroup branch; result persisted to
   `os.UserCacheDir()/k9s/latest-rev.json` (1h TTL, atomic write) so
   restarts skip GitHub entirely.
3. **Probe seeds the version cache** (`06f6d044`): the 15s connectivity
   probe shares its /version result into the LRU, so the first cluster
   info refresh no longer refetches /version through discovery.

## Milestones (tmux first-paint; warm creds+disk)

| round | first_frame | rows | cluster_info |
|---|---|---|---|
| 2 (baseline) | ~2.3 | ~2.9 | ~3.1 |
| 3           | 1.19-1.63 | 1.19-1.63 | 1.19-2.02 |
| 3, cold token | 5.67 | =first | =first |

Warm-run API timeline (klog v=6):

```
0.20-2.11  probe /version (exec-cred first token inside)
0.20-2.15  ns-list CanI SSAR
0.20-2.17  namespaces list          <- all three parallel now
2.19-2.41  /version (15s refreshCluster probe)
2.19-2.44  3x SSAR parallel + CRD CanI
2.42-2.96  nodes watch + node metrics + ns pods informer + pod metrics
3.12      first_frame = rows = cluster_info
```

No CachedDiscovery /version, no live /api+/apis, no duplicate SSARs or
metrics lists. Remaining floor: exec-credential first token (~2.4s cold)
plus one RTT; everything else overlaps.

## Measurement note

klog buffers 5s; kill the tmux session ≥6s after milestones or fast runs
flush nothing (only the 188-byte header lands). tl.sh now sleeps 6.
