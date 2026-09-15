package main

import (
	"fmt"
	"os"

	"github.com/samestrin/llm-semantic-axi/internal/semantic/commands"
	"github.com/samestrin/llm-semantic-axi/pkg/output"
)

// Both are stamped at build time with -X main.version / -X main.commit, so the
// binary reports the tag it was built from. They were hardcoded at "1.5.0"
// inside llm-tools, which had no semantic build targets at all.
var (
	version = "dev"
	commit  = "dev"
)

func main() {
	rootCmd := commands.RootCmd()
	rootCmd.Version = fmt.Sprintf("%s (%s)", version, commit)

	if err := rootCmd.Execute(); err != nil {
		f := output.New(commands.GlobalJSONOutput, commands.GlobalMinOutput, os.Stdout)
		os.Exit(f.PrintError(err))
	}
}
