// Command generate regenerates the checked-in benchmark outputs from the
// pinned commit, against a local stand-in embedder. It needs git and go, and
// no network. Re-counting the outputs does not need this; see count-tokens.
//
//	go run ./benchmarks/cmd/generate
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/samestrin/llm-semantic-axi/benchmarks"
)

func main() {
	repo := flag.String("repo", ".", "repository to check the commit out from")
	commit := flag.String("commit", benchmarks.PinnedCommit, "commit to index and build the CLI from")
	dir := flag.String("dir", "benchmarks", "benchmark directory")
	flag.Parse()
	logf := func(s string) { fmt.Fprintln(os.Stderr, s) }
	if err := benchmarks.Generate(context.Background(), *repo, *commit, *dir, logf); err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "outputs written; run `make bench-tokens-write` to update the numbers")
}
