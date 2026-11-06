// Command count-tokens re-counts the checked-in benchmark outputs offline and
// checks the numbers in benchmarks/results-tokens.md against them.
//
//	go run ./benchmarks/cmd/count-tokens          # print, and fail on a mismatch
//	go run ./benchmarks/cmd/count-tokens -write   # rewrite the numbers in the file
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/samestrin/llm-semantic-axi/benchmarks"
)

func main() {
	dir := flag.String("dir", "benchmarks", "benchmark directory")
	write := flag.Bool("write", false, "rewrite the numbers in results-tokens.md")
	flag.Parse()
	if err := benchmarks.RunCount(*dir, *write, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "count-tokens:", err)
		os.Exit(1)
	}
}
