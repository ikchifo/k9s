perf evidence: API-call reduction — periodic connectivity check + duplicate discovery
=====================================================================================

Item: ranked.md #11 (GET /version every 15s via a throwaway clientset) and the
duplicate-discovery half of #10 (startup/context-switch /api+/apis fetched by two
discovery stacks).

Change (internal/client/client.go):
1. CheckConnectivity() reuses the shared clientset (a.getClient()) and only
   builds a fresh one (Timeout=CallTimeout, as before) when it is nil or a
   proxy is configured (SetProxy can be swapped mid-session by
   activateContext, so proxied configs keep rebuilding). The probe always
   calls Discovery().ServerVersion() on the wire — it is a liveness probe,
   never served from a cache.
2. Dial() sets cfg.Timeout=CallTimeout() on the shared clientset, matching the
   bound that already existed post-first-probe via CheckConnectivity's
   setClient.
3. supportsMetricsResources() uses the cached discovery client
   (CachedDiscovery()) instead of a plain Dial() client, with an Invalidate()
   first so the metrics-server check stays live at each 5-min LRU expiry.
   This merges the second discovery stack: startup/ctx-switch /api+/apis go
   from 2x to 1x (cold disk cache) or 0x (warm).

Live verification (65s interactive run each, small cluster kind-kwok-k9s-s,
isolated K9S_CONFIG_DIR + fresh KUBECACHEDIR per run, klog -v=6):
  klog-before.txt / klog-after.txt:  /version 6->6 (3 startup + 3 periodic —
  all live), /api 2->1, /apis 2->1, SSAR 4->4, namespaces/watches 1->1 each.

Benchmark (internal/client/client_bench_test.go, BenchmarkCheckConnectivity
against an httptest TLS server; -benchtime=100x -count=10 -cpu 4):
  benchstat.txt:
    sec/op    297.12µ ± 8%  ->   95.07µ ±24%   -68.00%
    B/op       98.25Ki ± 0%  ->   15.23Ki ± 1%  -84.50%
    allocs/op  1757.0  ± 0%  ->    162.0  ± 0%  -90.78%
    reqs/op   1.000 (both — every check still performs exactly one GET /version)
  pprof-diff.txt: alloc_space diff removes NewRESTClient, url.parse,
  NewClientNegotiator, clientcmd.mergeValues, yaml parse per probe.
  (CPU diff is noise at these sample counts; allocs carry the signal.)

Benchmark methodology note: iterations are capped at 100x because all k9s
clientsets share one token-bucket rate limiter (defaultQPS=50, Burst=100,
internal/client/config.go); the shared clientset saturates it beyond burst,
which would measure the limiter (~20ms/op), not the probe. Production cadence
is 1 probe/15s (0.067 QPS), far below the limit, so this is a bench-loop
artifact only. The throwaway clientset in the base benchmark each got a fresh
limiter burst — one more per-probe cost the change removes.

Real-cluster savings not visible on localhost: TLS handshake (2-3 RTT) and,
on exec-credential kubeconfigs, an auth-plugin exec per 15s probe.

Files: bench-before/after.txt, benchstat.txt, cpu-before/after.pb.gz,
mem-before/after.pb.gz, pprof-diff.txt, klog-before/after.txt.
