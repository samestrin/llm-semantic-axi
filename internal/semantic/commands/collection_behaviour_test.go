package commands

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// chunkCount reports how many chunks survive in the index, which is the only
// honest way to check a delete actually deleted.
func chunkCount(t *testing.T) int {
	t.Helper()
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	stats, err := storage.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	return stats.ChunksTotal
}

// ===== runCollectionDelete =====

// TestCollectionDeleteRemovesOnlyTheNamedDomain is the whole point: profiles
// share one database, so a delete that ignored the domain would wipe the code
// index when you asked it to clear docs.
func TestCollectionDeleteRemovesOnlyTheNamedDomain(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("index code: %v", err)
		}
	})
	before := chunkCount(t)
	if before == 0 {
		t.Fatal("nothing was indexed, so the delete would pass vacuously")
	}

	out := captureStdout(t, func() {
		if err := runCollectionDelete(context.Background(), collectionDeleteOpts{
			domain: "docs", force: true,
		}); err != nil {
			t.Fatalf("delete docs: %v", err)
		}
	})
	if !strings.Contains(out, "No chunks found for domain 'docs'") {
		t.Errorf("deleting an unused domain printed %q", out)
	}
	if chunkCount(t) != before {
		t.Errorf("deleting the docs domain removed code chunks: %d -> %d", before, chunkCount(t))
	}

	out = captureStdout(t, func() {
		if err := runCollectionDelete(context.Background(), collectionDeleteOpts{
			domain: "code", force: true,
		}); err != nil {
			t.Fatalf("delete code: %v", err)
		}
	})
	if !strings.Contains(out, "Deleted") || !strings.Contains(out, "domain 'code'") {
		t.Errorf("the delete was not reported:\n%s", out)
	}
	if after := chunkCount(t); after != 0 {
		t.Errorf("%d chunks survived the delete", after)
	}
}

// TestCollectionDeleteJSONReportsTheCount so a script can tell a no-op from a
// real delete without parsing prose.
func TestCollectionDeleteJSONReportsTheCount(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("index: %v", err)
		}
	})
	before := chunkCount(t)

	out := captureStdout(t, func() {
		if err := runCollectionDelete(context.Background(), collectionDeleteOpts{
			domain: "code", force: true, jsonOutput: true,
		}); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})

	var payload map[string]any
	decodeJSON(t, out, &payload)
	if payload["domain"] != "code" || payload["status"] != "deleted" {
		t.Errorf("payload = %v, want a deleted code domain", payload)
	}
	if payload["deleted"] != float64(before) || payload["count"] != float64(before) {
		t.Errorf("payload counts = %v, want %d in both deleted and count", payload, before)
	}
}

// TestCollectionDeleteWithoutForceRefusesAndNeverPrompts defends AXI
// principle 6 and the repo rule that nothing reports success it did not
// achieve. The command used to prompt on stdin and return nil when the answer
// was "n" or stdin was closed, so a delete that never happened exited 0.
// Whatever is on stdin, it must now refuse with an error naming --force, print
// no prompt, and leave every chunk in place.
func TestCollectionDeleteWithoutForceRefusesAndNeverPrompts(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("index: %v", err)
		}
	})
	before := chunkCount(t)
	if before == 0 {
		t.Fatal("nothing was indexed, so the refusal would pass vacuously")
	}

	// "y" is included: a yes on stdin must not be read as consent either.
	for _, stdin := range []string{"n\n", "", "y\n"} {
		withStdin(t, stdin)
		var err error
		out := captureStdout(t, func() {
			err = runCollectionDelete(context.Background(), collectionDeleteOpts{domain: "code"})
		})
		if err == nil {
			t.Fatalf("stdin %q: delete without --force returned nil (exit 0)", stdin)
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("stdin %q: error = %v, want it to name --force", stdin, err)
		}
		var he *helpError
		if !errors.As(err, &he) || len(he.AXIHelp()) == 0 {
			t.Errorf("stdin %q: error carries no --axi next step: %#v", stdin, err)
		}
		if strings.Contains(out, "Are you sure?") {
			t.Errorf("stdin %q: a confirmation prompt was shown:\n%s", stdin, out)
		}
		if got := chunkCount(t); got != before {
			t.Fatalf("stdin %q: a refused delete removed chunks: %d -> %d", stdin, before, got)
		}
	}
}

// TestCollectionDeleteCmdWithoutForceExitsNonZero runs the cobra command, so
// the refusal is checked at the layer that sets the exit code.
func TestCollectionDeleteCmdWithoutForceExitsNonZero(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("index: %v", err)
		}
	})
	before := chunkCount(t)

	withStdin(t, "y\n")
	cmd := collectionDeleteCmd()
	cmd.SetArgs([]string{"--profile", "code"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	var err error
	captureStdout(t, func() { err = cmd.Execute() })
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("collection delete without --force: err = %v, want a refusal naming --force", err)
	}
	if chunkCount(t) != before {
		t.Error("the refused delete removed chunks")
	}
}

// TestCollectionDeleteWithoutAnIndexIsAnError rather than a cheerful zero.
func TestCollectionDeleteWithoutAnIndexIsAnError(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runCollectionDelete(context.Background(), collectionDeleteOpts{domain: "code", force: true})
	if err == nil {
		t.Fatal("deleting from a missing index returned nil")
	}
	if !strings.Contains(err.Error(), "semantic index not found") {
		t.Errorf("error = %v, want a missing-index message", err)
	}
}

// TestCollectionDeleteCmdRequiresAProfile: without it the command would have
// to guess which domain to destroy.
func TestCollectionDeleteCmdRequiresAProfile(t *testing.T) {
	newWorkspace(t)
	cmd := collectionDeleteCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("collection delete ran without --profile")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Errorf("error = %v, want it to name the missing flag", err)
	}
}
