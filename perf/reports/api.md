# k9s API-traffic report (area: Kubernetes API)

Method: rebuilt the prof binary (`perf/prof/build.sh` — the prebuilt `$T/k9s-prof`
had no prof hooks), drove k9s through tmux with client-go request logging at
klog v=9 (verb, URL, status, ms, serverProcessingMilliseconds, Accept +
Content-Length headers) while recording wall-clock times of each user action
(driver.sh, events-*.txt). All traffic artifacts under `perf/profiles/api/`.
klog line numbers below refer to files in that directory. Baseline numbers from
perf/results/baseline.json; wall-clock is noisy (other perf agents share the
machine/clusters), so counts/bytes/shares are the evidence.

## 1. API calls per user action

l = 20,170 pods (s numbers in parens). "Watches" use `sendInitialEvents=true`
(WatchList — see §2). Full parsed tables: `{s,l}-{main,ctx}.json`.

| action | reqs | breakdown (verb path × count) | bytes | client ms/req | serial/parallel |
|---|---|---|---|---|---|
| startup → full pods table (l 4.93s, s 0.65s) | 15 (15) | GET /api ×2, GET /apis ×2, GET /version ×3, GET /api/v1/namespaces ×1 (protobuf), POST selfSubjectAccessReviews ×4, WATCH nodes, WATCH crds, WATCH pods (WatchList) | ≈108.0MB (2.7MB) | all ≤7ms; pods stream ~2s+ | setup calls interleaved/parallel (0.08–0.10s burst); then one long stream |
| idle 20s | 1 (1) | GET /version ×1 | 379B | 2–8ms | — |
| `:deploy` (0.44s) | 2 (2) | WATCH deployments (WatchList) ×1 + SSAR ×1 | ≈0.7MB | 3–4ms | parallel |
| `:pods` (0.5s) | 0 (0) | — informer cache still alive | 0 | — | — |
| context switch m/s → l (7.2s this run, baseline 4.84s) | 15 (15) | identical set to startup: 2×/api, 2×/apis, 3×/version, namespaces, 4×SSAR, 3 watches | ≈108.0MB again | same | same |

Notes (Q1 sub-questions):
- **Duplicates**: /api and /apis are each fetched **twice** per startup/switch
  (two discovery clients: disk-cached + a fresh one from the metrics-support
  check in `InitConnection`, internal/client/client.go:74). /version is hit 3×.
  No /openapi (aggregated discovery v2 serves everything — good).
- **SelfSubjectAccessReviews**: 4 at startup, +1 per new resource view; cached
  5 min per (ns,gvr,verbs) in an LRU (`CanI`, internal/client/client.go:155–216,
  `cacheExpiry` line 32). Verified: zero SSAR repeats during idle and view
  switches within a session.
- **Periodic polling while idle**: exactly one thing polls — `GET /version`
  every **15s** (`clusterRefresh`, internal/view/app.go:39,373 → `refreshCluster`
  → `CheckConnectivity`, internal/client/client.go:306). `CheckConnectivity`
  builds a **new clientset every time** (client.go:328) and calls ServerVersion,
  bypassing the 5-min `serverVersion` LRU cache (client.go:225) — so the cache
  never helps this path. No namespaces/metrics/SSAR polling. No metrics-server
  on the kwok clusters, so no metrics.k8s.io traffic at all.
- No pagination anywhere: no `limit=` params ever appear; discovery is
  aggregated; the pods "LIST" is one WatchList stream (§2).

## 2. Informers & WatchList (Q2)

- Informers are created by `view/browser.go:92` (`factory.CanForResource`) and
  `dao.Resource.List` → `internal/watch/factory.go:286`
  (`NewFilteredDynamicSharedInformerFactory`, resync 10min, **unstructured JSON**).
  Observed informers: pods, nodes, CRDs at startup; deployments on `:deploy`;
  events on `:events`; one per visited GVR.
- **client-go v0.37.1 WatchListClient gate is ON by default** (beta since
  v1.35: `$(go env GOMODCACHE)/k8s.io/client-go@v0.37.1/features/known_features.go:141`).
  Confirmed twice: klog boot line `Feature gate default state
  feature="WatchListClient" enabled=true` (klog-l-main.log:6) and the actual
  request `GET /api/v1/pods?allowWatchBookmarks=true&resourceVersionMatch=
  NotOlderThan&sendInitialEvents=true&timeoutSeconds=432&watch=true → 200 OK`
  (klog-l-main.log:558–560). The apiserver (v1.36.1, default feature gates)
  supports it — the stream ends with a BOOKMARK annotated
  `k8s.io/initial-events-end=true`.
- So the pods table load is **not** a LIST and **not paginated**: it is a
  single WATCH that streams 20,171 events (20,170 ADDED + 1 bookmark),
  **108,003,328 B** of compact JSON (watchlist-events-count.txt). s: ≈2.75MB.
- Deployment switch `:deploy` likewise: one deployments WatchList (~700KB at l).
- `:pods` switch-back makes **zero** API calls — 100% client-side (informer
  cache), yet still measures 258ms at l (baseline) — i.e. the 258ms is render,
  not API.

## 3. Encoding: JSON vs protobuf vs Table (Q3)

Same 20,170 pods, one request each via kubectl proxy (encoding-sizes.txt):

| encoding | wire bytes | transfer | decode (bench, median) | allocs |
|---|---|---|---|---|
| WatchList JSON events (k9s today) | 108.0MB | ~2.0s best case (see caveat) | **2.04s** (bench-final.txt) | 919MB / 15.3M allocs |
| JSON LIST (compact) | 106.8MB | 0.94s | 2.80s (UnstructuredList) | 1.70GB / 29.5M allocs |
| **protobuf LIST (typed)** | **67.7MB** | 0.46s | **0.11s** (v1.PodList) | 423MB / 3.3M allocs |
| as=Table includeObject=Object | 247.5MB | 2.96s | 3.65s (+row decodes) | 1.08GB / 15.8M allocs |
| as=Table includeObject=Metadata | 124.7MB | 1.24s | 1.78s | 538MB / 7.6M allocs |
| as=Table, cells only (hypothetical) | 124.7MB | 1.24s | 0.23s | 150MB / 0.6M allocs |

Benchmark: `api-bench/` (client-go v0.37.1, Go 1.27, M1 Max, `-benchtime=3x
-count=3`, raw output bench-final.txt).

- **Typed protobuf decodes 18.5x faster than the unstructured JSON k9s uses**
  and moves 1.58x fewer bytes. The per-event JSON→`map[string]interface{}`
  decode of the initial burst is 2.04s of pure client CPU — the single largest
  API-side cost of startup.
- The `as=Table` route is **not** a win at this scale: bigger wire (124.7MB)
  and slower decode (1.78s) than compact JSON, unless k9s trusted server-side
  cells only (0.23s decode, 150MB allocs — 9x less than today's path, but loses
  object access for describe/yaml).
- Gotcha for anyone measuring: with `Accept: application/json` and a curl
  User-Agent the apiserver **pretty-prints** (226.7MB, 2.12x). k9s/kubectl/
  client-go UAs get compact JSON. k9s's dynamic client sends bare
  `application/json` (klog), so its LIST fallbacks would be compact, not pretty.

## 4. Where as=Table is used vs informers (Q4)

Two DAO resolution systems:
- `internal/model/table.go:273` → `resourceMeta()` (internal/model/helpers.go:32):
  Registry hit with DAO → informer path (pods, deploys, svc, nodes, ...).
  Registry hit without DAO → `dao.Resource` (informer). **Registry miss →
  `dao.Table`** (as=Table direct request per refresh, internal/dao/table.go:59).
- `view/browser.go:92` always creates an informer via `factory.CanForResource`
  regardless of which DAO the model uses.

Consequences:
- **pods and deployments views: informers** (watch + cache), refresh ticks make
  zero API calls.
- **events and every resource absent from the Registry — i.e. ALL CRDs — use
  `dao.Table`**: a full as=Table LIST every refresh tick (2s), no watch, no
  cache. Live evidence at l: `:events` issued 5 `GET
  /apis/events.k8s.io/v1/events?includeObject=Metadata` as=Table requests in
  10.9s (~one per tick), each **25,590,759B / 246–322ms server time**
  (klog-l-events.log, events-l-events.txt).
- **Worse: those views still start an informer too** (browser.go:92) — the
  events view also opened a WatchList on events (~21MB initial stream decoded
  into the informer store) whose data the as=Table path never reads. Double
  data path: full watch stream + full table poll every 2s.
- `internal/dao/dynamic.go` (as=Table via kubectl builder) is unreferenced —
  dead code.

## 5. Server-side vs client-side time for the startup pods load at l (Q5)

- Total user metric: startup_full_table = 4.93s (this run; baseline median 4.86s).
- Server-side, single controlled LIST (metrics-l-listexp-{pre,post}.txt,
  apiserver_request_duration_seconds deltas): JSON 106.8MB lands in bucket
  le=1.0 (**0.8–1.0s server processing+serialization**); protobuf 67.7MB in
  le=0.6 (~0.46s). The WatchList handshake itself is 7ms (klog ms=7,
  serverProcessing=7).
- Wire delivery of the initial-events burst: 2.04s best case through the local
  proxy (watchlist-raw-timing.txt). Under load from the other perf agents the
  identical stream took 28s and 230s (APF/queue contention with anonymous
  proxy clients) — spread recorded, not representative of k9s's own UA.
- Client-side decode of the same burst: **2.04s** (bench-final.txt) — it runs
  concurrently with the stream, so wire+decode ≈ max(2.0, 2.0) ≈ 2.0–2.5s.
- The remaining ~1.5–2s of the 4.93s is informer store/DeltaFIFO + first
  render (~1.4s matches baseline refresh_cpu 1376ms) plus ~0.2s of setup calls.
- **Caveat**: the apiserver runs locally in docker, so transfer cost is near
  zero. On a real cluster the 108MB JSON stream needs ~9–18s at 50–100Mbps vs
  5.4–10.8s for the 67.7MB protobuf LIST, and each of the 15 setup requests
  pays real RTT. The JSON tax is larger in the wild.

## Findings, ranked by share of user-facing delay

**F1 — startup/context-switch pull the entire pod table as unstructured JSON
WatchList events** (what: factory.go:286 dynamic informer + client-go
WatchListClient default; evidence: klog-l-main.log:558, 108.0MB stream,
bench 2.04s decode/919MB alloc vs protobuf 0.11s/423MB). Hits
startup_full_table (≈50% of the 4.9s is decode+wire; the other half render)
and context_switch (≈100% — see F2); feeds idle_rss 1817MB (20k pods stored as
maps — mem agent's area). Fix: typed protobuf informers for the hot built-ins
(pods) — 18.5x faster decode, 1.58x less wire; risk: med (render layer reads
unstructured maps; pod renderer must read typed structs).

**F2 — context switch rebuilds the whole client + re-streams the cluster**
(evidence: klog-l-ctx.log = same 15 requests + 108MB again; ctx_switch 7.2s
this run vs baseline 4.84s ≈ startup). Fix: keep per-context factory/informer
caches (LRU by context) so switching back is as cheap as `:pods` (0 requests,
258ms); risk: med (memory per cached context; eviction policy needed).

**F3 — CRD/events views: as=Table poll every 2s tick + informer that's never
read** (evidence: §4; 25.6MB/300ms per tick on 21k events; dead informer
stream). Hits: refresh CPU + server load on CRD-heavy clusters (not in the
baseline scenario, but any `:crd` view). Fix: either route registry-miss GVRs
through the informer the browser already creates, or skip informer creation
when the DAO is dao.Table; risk: low-med.

**F4 — idle polls /version every 15s through a throwaway clientset**
(app.go:39,373; client.go:306–342; evidence: 1 request per 20s idle window,
379B, 2–8ms). Negligible locally; on real clusters it's an extra round trip
every 15s and a clientset rebuild. Fix: reuse the cached ServerVersion or ping
/readyz with the shared client; risk: low.

**F5 — duplicated discovery + 3× /version per startup/switch** (evidence: URL
counts in §1). Small (few KB, parallel, sub-10ms each), but it's 4 avoidable
round trips per context switch on real clusters. Fix: single shared
disk-cached discovery client; risk: low.

**F6 — SSARs are cached correctly** (client.go:155–216; evidence: zero repeats
within a session) — no action needed. Note for real clusters: the first view of
each new resource still pays one SSAR round trip, and CanI runs serially before
informer creation; with a remote authz webhook this adds latency to every new
view.

## Artifacts (perf/profiles/api/)

- klog-{s,l}-{main,ctx}.log, klog-l-events.log, klog-s-smoke-v9.log — full
  client-go request/response logs at v=9 (klog file per session).
- events-{s,l}.txt, events-l-events.txt — action timestamps for window slicing.
- {s,l}-{main,ctx}.json — parsed per-window request tables (parse.py).
- bench-final.txt, api-bench/ — decode benchmark source + raw output.
- encoding-sizes.txt, watchlist-events-count.txt, watchlist-raw-timing.txt —
  wire size/latency measurements.
- metrics-{s,l}-{pre,post}.txt, metrics-l-listexp-{pre,post}.txt — apiserver
  /metrics snapshots; controlled LIST-duration experiment.
- driver.sh, events_demo.sh, parse.py, watchlist_*.py, metrics_diff.py —
  tooling.
