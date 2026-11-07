package commands

import (
	"strings"
	"testing"
)

// Defends the label a fallen-back file is reported under: a chunker that errors
// or panics windows a file without saying anything is wrong with its syntax, so
// it must not print "(parse failed)". Each group carries its own count, and a
// clean run prints nothing.
func TestPrintFallbackNamesEachReason(t *testing.T) {
	out := captureStdout(t, func() {
		printFallback(3,
			[]string{"/w/broken.py", "/w/panicked.ts", "/w/alsobroken.py"},
			[]string{"/w/panicked.ts"})
	})
	if !strings.Contains(out, "Chunked 2 files as text windows (parse failed):") {
		t.Errorf("parse-failure group missing or miscounted:\n%s", out)
	}
	if !strings.Contains(out, "Chunked 1 files as text windows (chunker failed):") {
		t.Errorf("chunker-failure group missing or miscounted:\n%s", out)
	}
	if strings.Contains(out, "panicked.ts\n  - ") {
		t.Errorf("panicked.ts is listed under the parse-failure group:\n%s", out)
	}

	if clean := captureStdout(t, func() { printFallback(0, nil, nil) }); clean != "" {
		t.Errorf("a clean run printed %q, want nothing", clean)
	}
}
