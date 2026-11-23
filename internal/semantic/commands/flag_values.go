package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic/config"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// embedderNames are the providers createEmbedder knows.
var embedderNames = []string{"openai", "cohere", "huggingface", "openrouter"}

// flagValueChecks validate a flag's value by its name, on whichever command
// declares it. Each returns what the flag takes when the value is bad, or "".
// They run in PersistentPreRunE, so a bad value exits 2 before an index
// directory is created or an embedding request is sent; checked later, in
// RunE, `index --embedder bogus` created .index and exited 1.
var flagValueChecks = map[string]func(f *pflag.Flag) string{
	"embedder":          oneOf(embedderNames),
	"profile":           oneOf(config.ValidProfiles()),
	"profiles":          eachOneOf(config.ValidProfiles()),
	"output":            oneOf(semantic.ValidOutputFormats()),
	"top":               intAtLeast(1),
	"threshold":         unitInterval,
	"rerank-threshold":  unitInterval,
	"older-than":        intBetween(1, 36500),
	"batch-size":        intAtLeast(0),
	"parallel":          intAtLeast(0),
	"embed-batch-size":  intAtLeast(0),
	"overlap-lines":     intAtLeast(0),
	"rerank-candidates": intAtLeast(0),
	"question":          nonBlank,
	"answer":            nonBlank,
}

// checkFlagValues rejects the first flag set on the command line whose value
// its check refuses, as a usage error naming the flag, the value and what it
// takes. Defaults are not checked: they are valid, and a flag left unset is
// not the caller's mistake.
func checkFlagValues(cmd *cobra.Command) error {
	var bad error
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		check, ok := flagValueChecks[f.Name]
		if bad != nil || !ok || !f.Changed {
			return
		}
		if takes := check(f); takes != "" {
			bad = usageError(cmd, fmt.Sprintf("invalid value %s for --%s: it takes %s", quoteValue(f), f.Name, takes),
				exampleHelp(cmd, "--"+f.Name)...)
		}
	})
	return bad
}

func quoteValue(f *pflag.Flag) string {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		return strconv.Quote(strings.Join(sv.GetSlice(), ","))
	}
	return strconv.Quote(f.Value.String())
}

func oneOf(valid []string) func(*pflag.Flag) string {
	return func(f *pflag.Flag) string {
		if slices.Contains(valid, f.Value.String()) {
			return ""
		}
		return "one of " + strings.Join(valid, ", ")
	}
}

func eachOneOf(valid []string) func(*pflag.Flag) string {
	return func(f *pflag.Flag) string {
		sv, ok := f.Value.(pflag.SliceValue)
		if !ok {
			return ""
		}
		for _, v := range sv.GetSlice() {
			if !slices.Contains(valid, v) {
				return "a comma-separated list of " + strings.Join(valid, ", ")
			}
		}
		return ""
	}
}

func intBetween(lo, hi int) func(*pflag.Flag) string {
	return func(f *pflag.Flag) string {
		n, err := strconv.Atoi(f.Value.String())
		if err == nil && n >= lo && n <= hi {
			return ""
		}
		return fmt.Sprintf("a whole number from %d to %d", lo, hi)
	}
}

func intAtLeast(lo int) func(*pflag.Flag) string {
	return func(f *pflag.Flag) string {
		n, err := strconv.Atoi(f.Value.String())
		if err == nil && n >= lo {
			return ""
		}
		return fmt.Sprintf("a whole number, %d or more", lo)
	}
}

func unitInterval(f *pflag.Flag) string {
	x, err := strconv.ParseFloat(f.Value.String(), 64)
	if err == nil && x >= 0 && x <= 1 {
		return ""
	}
	return "a number from 0.0 to 1.0"
}

func nonBlank(f *pflag.Flag) string {
	if strings.TrimSpace(f.Value.String()) != "" {
		return ""
	}
	return "a non-empty value"
}

// expandHome expands a leading ~ or ~/ in a path flag the shell did not
// expand (it was quoted), so `--index-dir '~/idx'` does not create a
// directory literally named ~ in the working directory.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand %q: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}
