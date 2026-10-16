# 03b factory-sync

`watch.Factory` List/Get with `wait=true` waited for cache sync with
`WaitForCacheSync`, which polls every informer in the namespace factory every 100ms.
CRD loading at startup (`dao/registry.go:384`) always paid at least one 100ms tick.
Now `waitForSync` selects on the informer's `HasSyncedChecker().Done()`, with the same 500ms cap.

`Done()` is closed by `sharedIndexInformer` after the controller's queue pops the
whole initial list (client-go v0.37.1 `the_real_fifo.go` `checkSynced_locked`, deferred after
processing), so the indexer holds every object when it fires.
`TestWaitForSyncSeesFullStore` checks this with a real `SharedIndexInformer` and 5,000 pods.

## Timings (`test-timings.txt`, `go test -count=10 -run TestWaitForSync ./internal/watch/`)

Fake ListWatch, 20ms list latency, 5,000 pods:

| | wait returns after |
|---|---|
| old `cache.WaitForCacheSync` (100ms poll), one-off comparison run 5x | 100.9-101.1ms |
| new `waitForSync` | 29-42ms (list 20ms + store fill) |
| never synced (cap) | 500.7-501.1ms, unchanged |

Latency, not CPU, so no benchstat and no pprof diff. The lead measures startup with the harness.
