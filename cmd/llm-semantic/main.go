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
		// A flag-parse or argument-count error never reaches PersistentPreRunE,
		// so --axi was not published and a subcommand's local --json and --min
		// were not copied to the globals; read them from the arguments instead.
		args := os.Args[1:]
		if argsRequest(args, "axi") {
			output.SetDefaultAXI(true)
		}
		if argsRequest(args, "json") {
			commands.GlobalJSONOutput = true
		}
		if argsRequest(args, "min") {
			commands.GlobalMinOutput = true
		}
		f := output.New(commands.GlobalJSONOutput, commands.GlobalMinOutput, os.Stdout)
		os.Exit(f.PrintError(err))
	}
}

func isVersionFlag(a string) bool {
	return a == "-v" || a == "-V" || a == "--version"
}

// argsRequest reports whether the arguments ask for the boolean flag --name,
// ignoring anything after a "--" terminator and honouring --name=false.
func argsRequest(args []string, name string) bool {
	requested := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--"+name {
			requested = true
		} else if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			b, err := strconv.ParseBool(v)
			requested = err == nil && b
		}
	}
	return requested
}
