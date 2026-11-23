package commands

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// EST-8171 item 4: `search --min` under TOON printed {file,line,name,pr,r,score}
// rows, wider than the default {content,file,name,score}, while the README
// calls --min the minimal payload. Under TOON --min is never wider than the
// default, as refs --min already is (EST-6878).

var toonRowHeader = regexp.MustCompile(`results\[\d+\]\{([^}]*)\}`)

func toonRowFields(t *testing.T, out string) []string {
	t.Helper()
	m := toonRowHeader.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no tabular results header in:\n%s", out)
	}
	return strings.Split(m[1], ",")
}

func TestSearchMinTOONIsNoWiderThanTheDefault(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)

	def := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 5}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	min := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 5, minOutput: true}); err != nil {
			t.Fatalf("runSearch --min: %v", err)
		}
	})

	defFields, minFields := toonRowFields(t, def), toonRowFields(t, min)
	if len(minFields) > len(defFields) {
		t.Errorf("--min rows %v are wider than the default %v", minFields, defFields)
	}
	for _, f := range minFields {
		if f == "pr" || f == "r" || f == "content" {
			t.Errorf("--min rows carry %q: %v", f, minFields)
		}
	}
	if len(min) >= len(def) {
		t.Errorf("--min output (%d bytes) is not smaller than the default (%d bytes)", len(min), len(def))
	}
}
