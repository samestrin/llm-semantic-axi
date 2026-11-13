package commands

import (
	"strings"
	"testing"
)

// TestResultHelpClaimsEveryCallEdgeLanguage defends the --axi next step after
// a search hit: it told an agent `callers` and `refs` show call sites "(Go
// only)", though they answer for Go, JavaScript, TypeScript, Python and Rust.
// An agent reading that stops asking about the other four, which is the same
// silent wrong answer as an empty result reported as "none exist".
func TestResultHelpClaimsEveryCallEdgeLanguage(t *testing.T) {
	var line string
	for _, h := range resultHelp(1) {
		if strings.Contains(h, "llm-semantic callers") {
			line = h
		}
	}
	if line == "" {
		t.Fatalf("resultHelp(1) = %q, want a step naming `llm-semantic callers`", resultHelp(1))
	}
	if strings.Contains(strings.ToLower(line), "go only") {
		t.Errorf("the callers/refs step still claims Go only: %s", line)
	}
	for _, lang := range []string{"Go", "JavaScript", "TypeScript", "Python", "Rust"} {
		if !strings.Contains(line, lang) {
			t.Errorf("the callers/refs step does not name %s: %s", lang, line)
		}
	}
}
