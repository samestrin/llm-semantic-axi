package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// thisReceiverFiles is a TypeScript repo where a `this.` call can only be
// resolved correctly inside its own class. Left and Right both have get and
// #drop, and get is also a top-level function, so Left.run's calls have one
// right answer: its own methods. Orphan calls this.fetch and this.#purge but
// has neither; the only chunks of those names are a top-level function and
// another class's private method, and linking to either is a false edge.
var thisReceiverFiles = map[string]string{
	"left.ts": `export class Left {
    get() {
        return 1;
    }

    #drop() {
        return 0;
    }

    run() {
        this.#drop();
        return this.get();
    }
}
`,
	"right.ts": `export class Right {
    get() {
        return 2;
    }

    #drop() {
        return 0;
    }
}
`,
	"top.ts": `export function get() {
    return 5;
}
`,
	"orphan.ts": `export class Orphan {
    load() {
        this.#purge();
        return this.fetch();
    }
}
`,
	"fetch.ts": `export function fetch() {
    return 3;
}
`,
	"other.ts": `export class Other {
    #purge() {
        return 4;
    }
}
`,
}

func thisReceiverWorkspace(t *testing.T) {
	t.Helper()
	indexFiles(t, thisReceiverFiles)
}

// indexFiles writes files into a fresh workspace and indexes it.
func indexFiles(t *testing.T, files map[string]string) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	runIndexIn(t, ".", indexOpts{skipCalibration: true})
}

type refsJSON struct {
	References []struct {
		Ref      string `json:"ref"`
		Resolved bool   `json:"resolved"`
		File     string `json:"file"`
	} `json:"references"`
}

func refsOf(t *testing.T, symbol string) refsJSON {
	t.Helper()
	out := captureStdout(t, func() {
		if err := runRefs(context.Background(), symbol, true, false); err != nil {
			t.Fatalf("runRefs(%s): %v", symbol, err)
		}
	})
	var got refsJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("refs %s is not JSON: %v\n%s", symbol, err, out)
	}
	return got
}

// TestThisCallNeverLinksOutsideItsClass defends against the last-dot guess for
// `this.` calls: this.fetch() was linked to any lone chunk named fetch in the
// JS/TS family, so a method became a "caller" of a top-level function or of
// another class's method it can never reach. Such a call must stay
// unresolved, and show in refs as unresolved, never as a caller.
func TestThisCallNeverLinksOutsideItsClass(t *testing.T) {
	thisReceiverWorkspace(t)

	refs := refsOf(t, "load")
	seen := map[string]bool{}
	for _, r := range refs.References {
		seen[r.Ref] = true
		if r.Resolved {
			t.Errorf("Orphan.load's %s resolved to %s; Orphan has no such method, so it must stay unresolved", r.Ref, r.File)
		}
	}
	for _, want := range []string{"this.fetch", "this.#purge"} {
		if !seen[want] {
			t.Errorf("refs from load lacks %s, so the test proves nothing: %+v", want, refs.References)
		}
	}

	for _, symbol := range []string{"fetch", "#purge"} {
		out := captureStdout(t, func() {
			if err := runCallers(context.Background(), symbol, false, false); err != nil {
				t.Fatalf("runCallers(%s): %v", symbol, err)
			}
		})
		if strings.Contains(out, "orphan.ts") {
			t.Errorf("Orphan.load is reported as a caller of %s it cannot reach:\n%s", symbol, out)
		}
	}
}

// TestThisCallResolvesToItsOwnClass defends the other half: with get and #drop
// in two classes and get also a top-level function, a `this.` call still has
// one right answer, the method of the caller's own class, and must link to it
// rather than to another class or the function.
func TestThisCallResolvesToItsOwnClass(t *testing.T) {
	thisReceiverWorkspace(t)

	refs := refsOf(t, "run")
	seen := map[string]bool{}
	for _, r := range refs.References {
		seen[r.Ref] = true
		if !r.Resolved || filepath.Base(r.File) != "left.ts" {
			t.Errorf("Left.run's %s = resolved %v in %q, want resolved in left.ts", r.Ref, r.Resolved, r.File)
		}
	}
	for _, want := range []string{"this.get", "this.#drop"} {
		if !seen[want] {
			t.Errorf("refs from run lacks %s: %+v", want, refs.References)
		}
	}

	for _, symbol := range []string{"get", "#drop"} {
		out := captureStdout(t, func() {
			if err := runCallers(context.Background(), symbol, false, false); err != nil {
				t.Fatalf("runCallers(%s): %v", symbol, err)
			}
		})
		if !strings.Contains(out, "1 caller(s) of "+symbol) || !strings.Contains(out, "left.ts") {
			t.Errorf("callers of %s should be Left.run alone:\n%s", symbol, out)
		}
	}
}

// TestThisCallToAClassOfTheSameNameStaysUnresolved defends the rule that a
// `this.` call targets a method: a class named get holds its own method m, so
// without that rule m's this.get() links to the class itself and m is
// reported as a caller of a class it never calls.
func TestThisCallToAClassOfTheSameNameStaysUnresolved(t *testing.T) {
	indexFiles(t, map[string]string{"cls.ts": `export class get {
    m() {
        return this.get();
    }
}
`})

	refs := refsOf(t, "m")
	if len(refs.References) != 1 || refs.References[0].Ref != "this.get" {
		t.Fatalf("refs from m should be this.get alone, so the test proves something: %+v", refs.References)
	}
	if refs.References[0].Resolved {
		t.Errorf("m's this.get resolved to %s; the only get is the class, not a method", refs.References[0].File)
	}

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "get", false, false); err != nil {
			t.Fatalf("runCallers(get): %v", err)
		}
	})
	if strings.Contains(out, "cls.ts") {
		t.Errorf("m is reported as a caller of the class get:\n%s", out)
	}
}

// TestGoReceiverNamedThisStillResolves defends Go from the JS/TS `this.` rule.
// Go records a call on a receiver named this as this.Method, and a Go method
// is declared outside its type, so the class rule would leave the edge
// unresolved and callers would answer empty while a caller exists.
func TestGoReceiverNamedThisStillResolves(t *testing.T) {
	indexFiles(t, map[string]string{"recv.go": `package sample

type T struct{}

func (this *T) Method() {}

func (this *T) Run() {
	this.Method()
}
`})

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Method", false, false); err != nil {
			t.Fatalf("runCallers(Method): %v", err)
		}
	})
	if !strings.Contains(out, "1 caller(s) of Method") || !strings.Contains(out, "Run") {
		t.Errorf("callers of Method should be Run:\n%s", out)
	}
}
