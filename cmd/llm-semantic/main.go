package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

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
		// A flag-parse error never reaches PersistentPreRunE, so --axi was not
		// published; read it from the arguments instead.
		if argsRequestAXI(os.Args[1:]) {
			output.SetDefaultAXI(true)
		}
		f := output.New(commands.GlobalJSONOutput, commands.GlobalMinOutput, os.Stdout)
		os.Exit(f.PrintError(err))
	}
}

// argsRequestAXI reports whether the arguments ask for --axi, ignoring anything
// after a "--" terminator and honouring --axi=false.
func argsRequestAXI(args []string) bool {
	requested := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--axi" {
			requested = true
		} else if v, ok := strings.CutPrefix(a, "--axi="); ok {
			b, err := strconv.ParseBool(v)
			requested = err == nil && b
		}
	}
	return requested
}
