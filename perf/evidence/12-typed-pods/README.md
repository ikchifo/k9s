# Task 12m — typed pods informer (branch perf/typed-pods-m) evidence

Binaries: `$T/k9s-12m` (darwin/arm64), `$T/linux2/k9s-12m` (linux/amd64, CGO_ENABLED=0),
both `go build -ldflags "-w -s"` with default.pgo. Master reference: `$T/k9s-final`.

Cluster: kwok via `perf/.kube/k9s-{s,m,l}.yaml`, contexts `kind-kwok-k9s-{s,m,l}`
(5050 / 5k / 20k pods; m = 5x team-NN x 1000 + kube-system; l = 20k all-ns).

## Benchstat headline (Apple M1 Max, go 1.26)

PodRender master vs branch (count=10, `podrender-benchstat.txt`):

    PodRender-10      19.401µ ± 5%   ->   1.570µ ± 1%   -91.91%
    PodRenderTick-10  15.06m ± 7%    ->  12.53m ± 10%   -16.76%   (20k rows)
    allocs/op         128            ->   31            -75.78%

Informer sync + watch decode (new benches, count=10, `podinformer-benchstat.txt`):

    PodInformerSync/protobuf/5000    59.42m  ± 2%   84.15k pods/s
    PodInformerSync/json/5000       460.7m  ± 3%   10.85k pods/s   (7.8x slower)
    PodInformerSync/protobuf/20000  244.2m  ± 6%   81.93k pods/s
    PodInformerSync/json/20000      1.828s  ± 3%   10.94k pods/s   (7.5x slower)
    WatchStreamDecode/protobuf       37.20m  ± 1%  134.4k pods/s  258MiB/s
    WatchStreamDecode/fastjson       414.0m  ± 1%   12.08k pods/s  43MiB/s  (11.1x slower)
    allocs 20k sync: 3.66M (proto) vs 11.44M (json); heap 337Mi vs 589Mi

Synthetic pods ~3.7KB JSON / ~2.0KB protobuf each (kwok-sized). The fake server
serves WatchList streams (length-framed raw WatchEvent protobuf / JSON lines,
initial-events-end bookmark) and list fallbacks; benches assert the reflector
took the WatchList path (`watchlistReqs >= b.N`).

CPU profiles: `cpu-sync-{proto,json}.pb.gz`, diff in `pprof-sync-diff.txt`.

## Live smoke (tmux `k9s12m`, captures in `smoke/`)
- pods view team-01 (m): table ready 5s, all columns render.
- nodes view: PODS column counts 231/251/282... across 20 nodes (typed CountPods).
- pod YAML (`y`): byte-identical to master binary on the same pod
  (`yaml-{branch,master}.body` first page, `*-tail.body` last page; 39+38 lines,
  includes `lastProbeTime: null` parity — omitempty never drops structs).
- logs (`l`): opens, "Waiting for logs..." (kwok pods have no logs).
- xray deploy (`:xray dp`): deployments -> ns -> svc [100/100/0] -> pods -> containers.
- `:ctx` m -> s switch: pods render on s after switch (typed informer rebuilt;
  merged kubeconfig `$T/k9s-home/merged.yaml` needed since each perf kubeconfig
  holds a single context).
- startup to first pods rows, all-namespaces at l (20k), 3 runs each:
  master 4.8/6.0/7.9s, branch 6.4(cold)/2.6/2.3s -> ~2.7x faster warm.
  Namespace-scoped landing (team-01, 4k pods): ~equal 2.4-3.0s both (sync is a
  small slice of total startup at that scale).

## Probe (k11probe, kind-kwok-k9s-m, 3 runs each — `probe-watchlist-vs-list.txt`)
WatchList (gate on, default): wall 1.30/1.20/1.20s, sys 219/219/215MB
LIST (KUBE_FEATURE_WatchListClient=false): wall 1.40/1.50/1.40s, sys 371/367/371MB
Decision: keep WatchList gate on — faster, half the system memory.

## Verification
- `go test ./...` pass; `go vet ./...` clean; `golangci-lint run` 0 issues.
- `go test -race` (watch, client, render, dao, model, xray): only pre-existing
  failures TestUpdateLogs, TestFlash, TestFlashBurst (allowed list).
