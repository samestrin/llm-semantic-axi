package commands

import (
	"context"
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

// TestCollectionDeleteRequiresConfirmation covers both prompt answers. This is
// the most destructive command in the package; defaulting to yes loses an
// entire index.
func TestCollectionDeleteRequiresConfirmation(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("index: %v", err)
		}
	})
	before := chunkCount(t)

	withStdin(t, "n\n")
	declined := captureStdout(t, func() {
		if err := runCollectionDelete(context.Background(), collectionDeleteOpts{domain: "code"}); err != nil {
			t.Fatalf("declined delete: %v", err)
		}
	})
	if !strings.Contains(declined, "Are you sure?") {
		t.Errorf("no confirmation prompt was shown:\n%s", declined)
	}
	if !strings.Contains(declined, "Aborted.") {
		t.Errorf("answering no did not abort:\n%s", declined)
	}
	if chunkCount(t) != before {
		t.Error("a declined delete removed chunks anyway")
	}

	withStdin(t, "")
	silent := captureStdout(t, func() {
		if err := runCollectionDelete(context.Background(), collectionDeleteOpts{domain: "code"}); err != nil {
			t.Fatalf("non-interactive delete: %v", err)
		}
	})
	if !strings.Contains(silent, "Aborted (non-interactive input)") {
		t.Errorf("a closed stdin did not abort:\n%s", silent)
	}
	if chunkCount(t) != before {
		t.Error("a non-interactive delete removed chunks anyway")
	}

	withStdin(t, "Y\n")
	confirmed := captureStdout(t, func() {
		if err := runCollectionDelete(context.Background(), collectionDeleteOpts{domain: "code"}); err != nil {
			t.Fatalf("confirmed delete: %v", err)
		}
	})
	if strings.Contains(confirmed, "Aborted") {
		t.Errorf("answering Y still aborted:\n%s", confirmed)
	}
	if chunkCount(t) != 0 {
		t.Error("a confirmed delete left chunks behind")
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
