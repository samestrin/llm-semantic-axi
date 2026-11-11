# Performance

<!-- Generated from results-performance.json by `make bench-perf`; do not edit. `make bench-perf-write` re-measures. -->

- **Version:** `v1.1.0-11-gc1ec2a4` (`llm-semantic --version`)
- **Date:** 2026-11-11
- **Machine:** Intel(R) Xeon(R) W-2295 CPU @ 3.00GHz, 36 threads, 141.2 GiB RAM, Debian GNU/Linux 13 (trixie) (linux/amd64), kernel 7.0.0-38-generic
- **Method:** each case runs its warmup runs untimed, then its timed runs: warmup 3 (cold start), 3 (typical command), 1 (large input); runs 20 (cold start), 20 (typical command), 5 (large input). Each timing is the wall clock of one `llm-semantic` process, from start to exit, on the release build. Every command also gets `--api-url`, `--model`, `--storage sqlite` and `--index-dir` pointing at the fake embedder and a temporary index; each `index` run starts from an empty index.
- **Embeddings:** a fixed-latency local fake: a loopback OpenAI-compatible server that waits 1.0 ms per request and answers with deterministic 256-dimension hash vectors, so the numbers measure the tool and not a model.
- **Large input:** 3,000 files, a corpus the program generates (Go, Python and Markdown files).

| Case | Command | Warmup | Runs | Min (ms) | Median (ms) | Mean (ms) | Max (ms) |
|---|---|---:|---:|---:|---:|---:|---:|
| cold start | `llm-semantic --version` | 3 | 20 | 3.8 | 4.0 | 4.1 | 4.9 |
| typical command | `llm-semantic search "load the config file and validate it" --json` | 3 | 20 | 85.8 | 88.6 | 88.8 | 93.2 |
| large input | `llm-semantic index <corpus> --json` | 1 | 5 | 32430.7 | 33200.5 | 33496.6 | 35090.8 |

Every timing is in [results-performance.json](results-performance.json). Measured by [`cmd/perf`](cmd/perf/main.go).
