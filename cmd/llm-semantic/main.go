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
	// Only a lone -v/-V/--version asks for the version. Anywhere else -v is a
	// subcommand flag (`index -v` is --verbose), so it is matched on the sole
	// argument rather than registered on the root, where cobra's own -v would
	// collide with that.
	if len(os.Args) == 2 && isVersionFlag(os.Args[1]) {
		fmt.Println(version)
		return
	}

	rootCmd := commands.RootCmd()

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

func isVersionFlag(a string) bool {
	return a == "-v" || a == "-V" || a == "--version"
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
