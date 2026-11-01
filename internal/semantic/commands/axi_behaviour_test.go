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
// "no index" from a healthy one. It drives the same path main does — the
// error out of runIndexStatus into PrintError — and asserts the exit code and
// the error text in plain, --json and --axi modes.
func TestIndexStatusWithNoIndexFailsInEveryMode(t *testing.T) {
	modes := []struct {
		name      string
		json, axi bool
	}{
		{"plain", false, false},
		{"json", true, false},
		{"axi", false, true},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Cleanup(func() { GlobalAXIOutput = false; GlobalJSONOutput = false })
			GlobalAXIOutput, GlobalJSONOutput = m.axi, m.json

			var runErr error
			captureStdout(t, func() { runErr = runIndexStatus(context.Background(), m.json) })
			if runErr == nil {
				t.Fatal("runIndexStatus returned nil with no index; the command would exit 0")
			}

			var buf strings.Builder
			f := output.New(GlobalJSONOutput, GlobalMinOutput, &buf)
			f.AXI = GlobalAXIOutput
			if code := f.PrintError(runErr); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if !strings.Contains(buf.String()+runErr.Error(), "index not found") {
				t.Errorf("error text missing %q: %v / %q", "index not found", runErr, buf.String())
			}
		})
	}
}
