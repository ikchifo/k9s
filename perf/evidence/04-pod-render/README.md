
## Harness (macOS, interleaved with master 7453f748, 5 runs)

`harness.txt` / `benchstat-harness.txt` (labels 02-master-i vs 04-pod-render):

| size | refresh_cpu | idle_cpu | switch_to_pods |
|---|---|---|---|
| s | 65.9 -> 49.9ms (-24%) | 3.3 -> 2.5% | ~ |
| m | 395 -> 298ms (-25%, p=0.008) | 19.8 -> 14.9% | 97.5 -> 83.2ms (p=0.032) |
| l | 1189 -> 905ms (-24%, p=0.008) | 59.5 -> 45.2% | 264 -> 194ms (p=0.008) |

Startup, context switch, filter and RSS: within noise.
