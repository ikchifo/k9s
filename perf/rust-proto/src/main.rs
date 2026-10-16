// Decodes a k8s JSON watch stream the way k9s's informer must, to compare with
// internal/client BenchmarkWatchDecode on the same bytes.
//
// usage: watch-decode <stream.jsonl> <value|typed> [rounds]
use std::time::Instant;

use k8s_openapi::api::core::v1::Pod;
use serde::Deserialize;

#[derive(Deserialize)]
struct Event<T> {
    #[serde(rename = "type")]
    kind: String,
    object: T,
}

fn decode<T: for<'de> Deserialize<'de>>(data: &[u8]) -> usize {
    let mut n = 0;
    for line in data.split(|b| *b == b'\n').filter(|l| !l.is_empty()) {
        let ev: Event<T> = serde_json::from_slice(line).expect("decode");
        if ev.kind == "ADDED" {
            n += 1;
        }
        std::hint::black_box(&ev.object);
    }
    n
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let data = std::fs::read(&args[1]).expect("read stream");
    let mode = args[2].as_str();
    let rounds: usize = args.get(3).map_or(10, |r| r.parse().expect("rounds"));

    for _ in 0..rounds {
        let start = Instant::now();
        let n = match mode {
            "value" => decode::<serde_json::Value>(&data),
            "typed" => decode::<Pod>(&data),
            _ => panic!("mode must be value or typed"),
        };
        let ns = start.elapsed().as_nanos();
        println!("BenchmarkRustDecode/{mode} 1 {ns} ns/op {n} events");
    }
}
