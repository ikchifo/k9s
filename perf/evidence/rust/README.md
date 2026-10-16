# Rust vs Go watch decode

Same input for both: 20,000 lines of `{"type":"ADDED","object":<pod>}`, the pod
being `internal/render/testdata/po_full.json` compacted (79MB).

```sh
P=$(jq -c . internal/render/testdata/po_full.json)
awk -v p="$P" 'BEGIN{for(i=0;i<20000;i++) printf "{\"type\":\"ADDED\",\"object\":%s}\n", p}' > stream.jsonl

(cd perf/rust-proto && cargo build --release &&
  ./target/release/watch-decode ../../stream.jsonl value 10 &&
  ./target/release/watch-decode ../../stream.jsonl typed 10)
(cd perf/rust-proto/gobench &&
  WATCH_STREAM=$PWD/../../../stream.jsonl go test -run '^$' -bench . -benchtime=1x -count=10 -cpu 4 -benchmem)
```

- `rust.txt`: serde_json into `serde_json::Value` and `k8s_openapi` `Pod`, release + LTO.
- `go.txt`: `sigs.k8s.io/json` (what k9s uses) into `map[string]any` and `v1.Pod`.
- M1 Max, rustc 1.94.0, go1.27.1, run one after another on an idle machine.

| target | Go | Rust |
|---|---|---|
| generic map | 663.8ms ± 4% | 325.5ms ± 2% |
| typed Pod | 878.3ms ± 4% | 259.2ms ± 1% |
