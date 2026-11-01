package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/pkg/output"
)

// captureStdout swaps os.Stdout for a pipe. emitJSON writes there rather than
// to an injectable writer, because the 21 sites it replaced did.
//
// The restore is registered with t.Cleanup BEFORE the swap, so a panic or
// t.Fatalf inside fn cannot leave later tests in this package writing to a
// closed pipe — the same defect a reviewer caught in llm-filesystem's copy.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	// Both the restore AND the pipe close are deferred. fn calls t.Fatalf on
	// failure, which unwinds through runtime.Goexit rather than returning, so
	// the straight-line `w.Close()` never ran — leaving the reader goroutine
	// blocked on a writer that is never closed, for the rest of the run.
	// Closing twice is harmless; not closing at all leaks.
	defer func() {
		_ = w.Close()
		os.Stdout = orig
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// withAXI turns TOON output on for one test and guarantees it is off again
// afterwards, so no test can leak the mode into another.
func withAXI(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { GlobalAXIOutput = false })
	GlobalAXIOutput = true
}

// emitStdout is the wrapper 21 of the 23 converted sites call, and coverage
// showed it at 0%: the behaviour tests reach emitJSON directly through
// index-status's COMPACT branches, while its indented branch needs a real
// index that no test creates. So the most-used path in the package was
// untested. These two close that.

func TestEmitStdoutWritesIndentedJSONByDefault(t *testing.T) {
	got := captureStdout(t, func() {
		if err := emitStdout(map[string]any{"a": 1, "b": "two"}); err != nil {
			t.Fatalf("emitStdout: %v", err)
		}
	})
	if !strings.Contains(got, "\n  ") {
		t.Errorf("expected indented JSON, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, got)
	}
	if payload["b"] != "two" {
		t.Errorf("b = %v, want two", payload["b"])
	}
}

func TestEmitStdoutHonoursAXI(t *testing.T) {
	withAXI(t)
	got := captureStdout(t, func() {
		if err := emitStdout(map[string]any{"a": 1, "b": "two"}); err != nil {
			t.Fatalf("emitStdout: %v", err)
		}
	})
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("--axi was ignored; JSON was emitted: %q", got)
	}
	if !strings.Contains(got, "b: two") {
		t.Errorf("expected TOON key/value, got %q", got)
	}
}

// TestIndexStatusWithNoIndexFailsInEveryMode defends the rule that nothing
// reports success when it did not succeed: index-status over an empty
// directory used to print "not found" and exit 0, so a script could not tell
// "no index" from a healthy one. It runs the real root command with each flag,
// then hands the error to the formatter exactly as cmd/llm-semantic/main.go
// does (no hand-set AXI: main does not publish --axi to the formatter, see
// docs/axi-audit.md 6b, so --axi errors are plain text on that path today).
// Each mode asserts a non-zero exit code and the text the formatter wrote.
func TestIndexStatusWithNoIndexFailsInEveryMode(t *testing.T) {
	modes := []struct {
		name string
		flag string
		want func(t *testing.T, out string)
	}{
		{"plain", "", func(t *testing.T, out string) {
			if !strings.Contains(out, "Error: semantic index not found") {
				t.Errorf("plain output = %q", out)
			}
		}},
		{"json", "--json", func(t *testing.T, out string) {
			var p map[string]any
			if err := json.Unmarshal([]byte(out), &p); err != nil {
				t.Fatalf("not JSON: %v (%q)", err, out)
			}
			if p["error"] != true || !strings.Contains(p["message"].(string), "index not found") {
				t.Errorf("payload = %v", p)
			}
		}},
		{"axi", "--axi", func(t *testing.T, out string) {
			if !strings.Contains(out, "semantic index not found") {
				t.Errorf("axi output = %q", out)
			}
		}},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Cleanup(func() { GlobalAXIOutput = false; GlobalJSONOutput = false })

			args := []string{"index-status"}
			if m.flag != "" {
				args = append(args, m.flag)
			}
			var runErr error
			captureStdout(t, func() { _, runErr = executeCommand(RootCmd(), args...) })
			if runErr == nil {
				t.Fatal("index-status returned nil with no index; the command would exit 0")
			}

			var buf strings.Builder
			f := output.New(GlobalJSONOutput, GlobalMinOutput, &buf)
			if code := f.PrintError(runErr); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			m.want(t, buf.String())
		})
	}
}

// The success-path counterparts of the no-index tests this change replaced:
// field parity between --json and --axi, and --json staying compact-free of
// reformatting, now over a real index.

func TestIndexStatusAXIAndJSONAgreeOnFieldNames(t *testing.T) {
	// --axi must carry the same field names --json does; a renamed or dropped
	// field would still "work" and still exit 0.
	indexedWorkspace(t)
	jsonOut := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), true); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	withAXI(t)
	// false: --axi ALONE is how a caller invokes it.
	axiOut := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})
	for field := range payload {
		if !strings.Contains(axiOut, field+":") {
			t.Errorf("--json field %q has no counterpart in --axi output:\n%s", field, axiOut)
		}
	}
}

func TestIndexStatusTextModeOverARealIndexIsHumanText(t *testing.T) {
	// Neither flag set must still print human text, or --axi handling has
	// swallowed the default path.
	indexedWorkspace(t)
	got := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})
	if !strings.Contains(got, "Semantic Index Status") || strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("text mode changed:\n%s", got)
	}
}

// TestIndexStatusWithNoIndexEmitsNoSuccessPayload: the old code printed
// {"indexed":false} to stdout, which a consumer reading stdout alone could
// parse as a status result. The failure must travel as an error instead.
func TestIndexStatusWithNoIndexEmitsNoSuccessPayload(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, jsonOut := range []bool{false, true} {
		out := captureStdout(t, func() {
			if err := runIndexStatus(context.Background(), jsonOut); err == nil {
				t.Error("want an error with no index")
			}
		})
		if strings.TrimSpace(out) != "" {
			t.Errorf("json=%v: stdout = %q, want empty", jsonOut, out)
		}
	}
}
