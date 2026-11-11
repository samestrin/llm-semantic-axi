// Command perf times llm-semantic by wall clock and writes
// benchmarks/results-performance.json and the table rendered from it,
// benchmarks/results-performance.md.
//
//	go run ./benchmarks/cmd/perf -measure   # time the binary, write both files
//	go run ./benchmarks/cmd/perf            # regenerate the table from the JSON
//
// Measuring needs no network: the commands embed through a loopback fake.
// The large input is a generated corpus of 3,000 files unless BENCH_DIR
// names a directory of at least that many.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/samestrin/llm-semantic-axi/benchmarks"
)

func main() {
	dir := flag.String("dir", "benchmarks", "benchmark directory")
	measure := flag.Bool("measure", false, "time the binary and rewrite the JSON as well as the table")
	cli := flag.String("cli", "build/llm-semantic", "llm-semantic binary to time")
	warmup := flag.Int("warmup", 3, "untimed runs before the cold-start and typical cases")
	runs := flag.Int("runs", 20, "timed runs of the cold-start and typical cases")
	indexWarmup := flag.Int("index-warmup", 1, "untimed runs before the large-input case")
	indexRuns := flag.Int("index-runs", 5, "timed runs of the large-input case")
	latency := flag.Duration("latency", time.Millisecond, "the fake embedder's wait per request")
	flag.Parse()

	var err error
	if *measure {
		err = benchmarks.RunPerf(context.Background(), benchmarks.PerfConfig{
			CLI: *cli, OutDir: *dir, Corpus: os.Getenv("BENCH_DIR"),
			Files: benchmarks.PerfMinFiles, MinFiles: benchmarks.PerfMinFiles,
			Warmup: *warmup, Runs: *runs, IndexWarmup: *indexWarmup, IndexRuns: *indexRuns,
			Latency: *latency, Now: time.Now, Machine: benchmarks.ReadMachine,
			Log: func(s string) { fmt.Fprintln(os.Stderr, s) },
		}, os.Stdout)
	} else {
		err = benchmarks.WritePerfTable(*dir, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
}
