package semantic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// domainDeleteBackends runs a domain-delete check against both backends: a
// real sqlite file and Qdrant through the in-process fake.
func domainDeleteBackends(t *testing.T, run func(t *testing.T, storage Storage)) {
	t.Run("sqlite", func(t *testing.T) { run(t, newFileStorage(t)) })
	t.Run("qdrant", func(t *testing.T) {
		storage, _ := newFakeQdrantStorage(t, "domain_delete", 4)
		run(t, storage)
	})
}

// TestDeleteByDomain_LeavesNoStaleFileHash defends debt row 7977dfb848821a26:
// `collection delete --domain` removed the chunks but kept each file's hash
// row, so index-status still listed a fallen-back file whose chunks were gone,
// and Update skipped it as unchanged, so it could never be re-indexed. The
// code-domain file proves the delete does not touch another domain's record.
func TestDeleteByDomain_LeavesNoStaleFileHash(t *testing.T) {
	domainDeleteBackends(t, func(t *testing.T, storage Storage) {
		ctx := context.Background()
		dir := t.TempDir()
		docsDir := filepath.Join(dir, "docs")
		srcDir := filepath.Join(dir, "src")
		for path, body := range map[string]string{
			filepath.Join(docsDir, "bad.fb"):  "BROKEN",
			filepath.Join(docsDir, "good.fb"): "fine",
			filepath.Join(srcDir, "code.fb"):  "BROKEN code",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		factory := NewChunkerFactory()
		factory.Register("fb", fallbackStub{})
		mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, factory)

		if _, err := mgr.Index(ctx, docsDir, IndexOptions{Domain: "docs"}); err != nil {
			t.Fatalf("Index docs: %v", err)
		}
		if _, err := mgr.Index(ctx, srcDir, IndexOptions{Domain: "code"}); err != nil {
			t.Fatalf("Index code: %v", err)
		}
		badPath := filepath.Join(docsDir, "bad.fb")
		goodPath := filepath.Join(docsDir, "good.fb")
		codePath := filepath.Join(srcDir, "code.fb")

		removed, err := storage.DeleteByDomain(ctx, "docs")
		if err != nil {
			t.Fatalf("DeleteByDomain: %v", err)
		}
		if removed != 2 {
			t.Errorf("DeleteByDomain = %d, want 2 (the docs chunks only)", removed)
		}

		// index-status reads ListFallbackFiles.
		fallbacks, err := storage.(FallbackStore).ListFallbackFiles(ctx)
		if err != nil {
			t.Fatalf("ListFallbackFiles: %v", err)
		}
		if _, ok := fallbacks[badPath]; ok {
			t.Errorf("ListFallbackFiles still names the deleted docs file: %v", fallbacks)
		}
		if fallbacks[codePath] != FallbackParseFailed {
			t.Errorf("code fallback reason = %q, want %q: another domain's record was touched", fallbacks[codePath], FallbackParseFailed)
		}
		for _, path := range []string{badPath, goodPath} {
			if hash, err := storage.GetFileHash(ctx, path); err != nil || hash != "" {
				t.Errorf("GetFileHash(%s) = %q, %v; want no hash row left", path, hash, err)
			}
		}
		if hash, err := storage.GetFileHash(ctx, codePath); err != nil || hash == "" {
			t.Errorf("GetFileHash(code) = %q, %v; want the code hash kept", hash, err)
		}
		indexed, err := storage.ListIndexedFiles(ctx)
		if err != nil {
			t.Fatalf("ListIndexedFiles: %v", err)
		}
		if indexed[badPath] || indexed[goodPath] || !indexed[codePath] {
			t.Errorf("ListIndexedFiles = %v, want only %s", indexed, codePath)
		}

		// Update must see the docs files as new and index them again.
		result, err := mgr.Update(ctx, docsDir, UpdateOptions{Domain: "docs"})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if result.FilesUpdated != 2 {
			t.Errorf("Update FilesUpdated = %d, want 2: the deleted docs files were skipped as unchanged", result.FilesUpdated)
		}
		chunks, err := storage.List(ctx, ListOptions{FilePath: badPath})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(chunks) != 1 {
			t.Errorf("chunks for %s after Update = %d, want 1", badPath, len(chunks))
		}
		if fallbacks, err := storage.(FallbackStore).ListFallbackFiles(ctx); err != nil || fallbacks[badPath] != FallbackParseFailed {
			t.Errorf("ListFallbackFiles after Update = %v, %v; want %s re-recorded", fallbacks, err, badPath)
		}
	})
}

// TestDeleteByDomain_KeepsHashOfAPathWithChunksLeft defends the "no chunks
// left" condition: a path with chunks in two domains is still indexed after
// one domain is deleted, so its hash must stay, or Update would re-embed a file
// that is already in the index.
func TestDeleteByDomain_KeepsHashOfAPathWithChunksLeft(t *testing.T) {
	domainDeleteBackends(t, func(t *testing.T, storage Storage) {
		ctx := context.Background()
		emb := []float32{0.1, 0.2, 0.3, 0.4}
		for _, c := range []Chunk{
			{ID: "shared-docs", FilePath: "/r/shared.md", Name: "a", Content: "a", Type: ChunkFile, Language: "md", Domain: "docs"},
			{ID: "shared-code", FilePath: "/r/shared.md", Name: "b", Content: "b", Type: ChunkFile, Language: "md", Domain: "code"},
			{ID: "only-docs", FilePath: "/r/only.md", Name: "c", Content: "c", Type: ChunkFile, Language: "md", Domain: "docs"},
		} {
			if err := storage.Create(ctx, c, emb); err != nil {
				t.Fatalf("Create(%s): %v", c.ID, err)
			}
		}
		// A path with no chunks at all, in any domain: a delete of docs did
		// not remove anything of it, so its record is not the delete's to drop.
		for _, path := range []string{"/r/shared.md", "/r/only.md", "/r/empty.md"} {
			if err := storage.SetFileHash(ctx, path, "h-"+path); err != nil {
				t.Fatalf("SetFileHash(%s): %v", path, err)
			}
		}

		if _, err := storage.DeleteByDomain(ctx, "docs"); err != nil {
			t.Fatalf("DeleteByDomain: %v", err)
		}

		for path, want := range map[string]string{
			"/r/shared.md": "h-/r/shared.md",
			"/r/only.md":   "",
			"/r/empty.md":  "h-/r/empty.md",
		} {
			if got, err := storage.GetFileHash(ctx, path); err != nil || got != want {
				t.Errorf("GetFileHash(%s) = %q, %v; want %q", path, got, err, want)
			}
		}
	})
}

// TestQdrantStorage_DeleteByDomainBatchesOrphanCheck defends against a domain
// delete that asks Qdrant about each file in turn: one Count per path made
// `collection delete --domain` N sequential round trips. Every fifth file also
// has a chunk in another domain, so the kept and deleted hashes prove the
// batched check still tells the two apart.
func TestQdrantStorage_DeleteByDomainBatchesOrphanCheck(t *testing.T) {
	for _, n := range []int{50, 250} {
		t.Run(fmt.Sprintf("%d files", n), func(t *testing.T) {
			storage, fq := newFakeQdrantStorage(t, "domain_delete_batch", 4)
			ctx := context.Background()
			emb := []float32{0.1, 0.2, 0.3, 0.4}
			var chunks []ChunkWithEmbedding
			for i := 0; i < n; i++ {
				path := fmt.Sprintf("/r/%03d.md", i)
				chunks = append(chunks, ChunkWithEmbedding{Chunk: Chunk{ID: fmt.Sprintf("docs-%d", i), FilePath: path, Name: "d", Content: "d", Type: ChunkFile, Language: "md", Domain: "docs"}, Embedding: emb})
				if i%5 == 0 {
					chunks = append(chunks, ChunkWithEmbedding{Chunk: Chunk{ID: fmt.Sprintf("code-%d", i), FilePath: path, Name: "c", Content: "c", Type: ChunkFile, Language: "md", Domain: "code"}, Embedding: emb})
				}
			}
			if err := storage.CreateBatch(ctx, chunks); err != nil {
				t.Fatalf("CreateBatch: %v", err)
			}
			for i := 0; i < n; i++ {
				path := fmt.Sprintf("/r/%03d.md", i)
				if err := storage.SetFileHash(ctx, path, "h-"+path); err != nil {
					t.Fatalf("SetFileHash(%s): %v", path, err)
				}
			}

			counts, scrolls := fq.callCount("Count"), fq.callCount("Scroll")
			if _, err := storage.DeleteByDomain(ctx, "docs"); err != nil {
				t.Fatalf("DeleteByDomain: %v", err)
			}
			if got := fq.callCount("Count") - counts; got >= n {
				t.Errorf("DeleteByDomain made %d Count calls for %d files, want fewer than %d", got, n, n)
			}
			if got := fq.callCount("Scroll") - scrolls; got >= n {
				t.Errorf("DeleteByDomain made %d Scroll calls for %d files, want fewer than %d", got, n, n)
			}

			for i := 0; i < n; i++ {
				path := fmt.Sprintf("/r/%03d.md", i)
				want := ""
				if i%5 == 0 {
					want = "h-" + path
				}
				if got, err := storage.GetFileHash(ctx, path); err != nil || got != want {
					t.Errorf("GetFileHash(%s) = %q, %v; want %q", path, got, err, want)
				}
			}
		})
	}
}

// TestQdrantStorage_DeleteByDomainOrphanCheckReadsPastAFullPage defends the
// batched orphan check against stopping at its first page: /r/busy.md has more
// chunks in another domain than one page holds, so /r/late.md's one chunk
// elsewhere comes after a full page. Reading that page alone would call
// /r/late.md orphaned and drop the hash of a file that is still indexed.
func TestQdrantStorage_DeleteByDomainOrphanCheckReadsPastAFullPage(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "domain_delete_full_page", 4)
	ctx := context.Background()
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	chunk := func(id, path, domain string) ChunkWithEmbedding {
		return ChunkWithEmbedding{Chunk: Chunk{ID: id, FilePath: path, Name: id, Content: id, Type: ChunkFile, Language: "md", Domain: domain}, Embedding: emb}
	}
	chunks := []ChunkWithEmbedding{
		chunk("busy-docs", "/r/busy.md", "docs"),
		chunk("late-docs", "/r/late.md", "docs"),
		chunk("gone-docs", "/r/gone.md", "docs"),
	}
	for i := 0; i < 1200; i++ {
		chunks = append(chunks, chunk(fmt.Sprintf("busy-code-%d", i), "/r/busy.md", "code"))
	}
	chunks = append(chunks, chunk("late-code", "/r/late.md", "code"))
	if err := storage.CreateBatch(ctx, chunks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	for _, path := range []string{"/r/busy.md", "/r/late.md", "/r/gone.md"} {
		if err := storage.SetFileHash(ctx, path, "h-"+path); err != nil {
			t.Fatalf("SetFileHash(%s): %v", path, err)
		}
	}

	if _, err := storage.DeleteByDomain(ctx, "docs"); err != nil {
		t.Fatalf("DeleteByDomain: %v", err)
	}

	for path, want := range map[string]string{
		"/r/busy.md": "h-/r/busy.md",
		"/r/late.md": "h-/r/late.md",
		"/r/gone.md": "",
	} {
		if got, err := storage.GetFileHash(ctx, path); err != nil || got != want {
			t.Errorf("GetFileHash(%s) = %q, %v; want %q", path, got, err, want)
		}
	}
}

// TestQdrantStorage_DeleteByDomainFailures defends against a domain delete that
// reports success when part of it failed. Each failing call must surface as an
// error. The hashes go before the chunks, so a failed chunk delete leaves files
// that Update will index again, rather than chunks with no hash to say so.
func TestQdrantStorage_DeleteByDomainFailures(t *testing.T) {
	boom := status.Error(codes.Unavailable, "boom")
	for name, arm := range map[string]func(fq *fakeQdrant){
		"scroll paths":         func(fq *fakeQdrant) { fq.setFailure("Scroll", boom) },
		"scroll other domains": func(fq *fakeQdrant) { fq.setFailureAfter("Scroll", 1, boom) },
		"delete hashes":        func(fq *fakeQdrant) { fq.setFailure("DeletePoints", boom) },
		"delete chunks":        func(fq *fakeQdrant) { fq.setFailureAfter("DeletePoints", 1, boom) },
	} {
		t.Run(name, func(t *testing.T) {
			storage, fq := newFakeQdrantStorage(t, "domain_delete_failures", 4)
			ctx := context.Background()
			chunk := Chunk{ID: "d1", FilePath: "/r/doc.md", Name: "d", Content: "d", Type: ChunkFile, Language: "md", Domain: "docs"}
			if err := storage.Create(ctx, chunk, []float32{0.1, 0.2, 0.3, 0.4}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := storage.SetFileHash(ctx, "/r/doc.md", "h1"); err != nil {
				t.Fatalf("SetFileHash: %v", err)
			}

			arm(fq)
			if _, err := storage.DeleteByDomain(ctx, "docs"); err == nil {
				t.Fatal("DeleteByDomain succeeded with a failing call")
			}
			fq.clearFailure("Scroll")
			fq.clearFailure("Count")
			fq.clearFailure("DeletePoints")

			chunks, err := storage.List(ctx, ListOptions{FilePath: "/r/doc.md"})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(chunks) != 1 {
				t.Errorf("chunks left = %d, want 1: the chunks went though the delete failed", len(chunks))
			}
			hash, err := storage.GetFileHash(ctx, "/r/doc.md")
			if err != nil {
				t.Fatalf("GetFileHash: %v", err)
			}
			wantHash := "h1"
			if name == "delete chunks" {
				wantHash = "" // so Update re-indexes the file
			}
			if hash != wantHash {
				t.Errorf("hash after a failed %s = %q, want %q", name, hash, wantHash)
			}
		})
	}
}

// TestSQLiteStorage_DeleteByDomainFailureKeepsChunks defends the transaction:
// when the hash cleanup fails the chunks must stay, and the error must reach
// the caller, or `collection delete --domain` reports a delete that did not
// happen.
func TestSQLiteStorage_DeleteByDomainFailureKeepsChunks(t *testing.T) {
	storage := newFileStorage(t)
	ctx := context.Background()
	chunk := Chunk{ID: "d1", FilePath: "/r/doc.md", Name: "d", Content: "d", Type: ChunkFile, Language: "md", Domain: "docs"}
	if err := storage.Create(ctx, chunk, []float32{0.1, 0.2, 0.3, 0.4}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := storage.DeleteByDomain(cancelled, "docs"); err == nil {
		t.Error("DeleteByDomain succeeded on a cancelled context")
	}

	if _, err := storage.db.Exec(`DROP TABLE file_hashes`); err != nil {
		t.Fatalf("drop file_hashes: %v", err)
	}
	if _, err := storage.DeleteByDomain(ctx, "docs"); err == nil {
		t.Error("DeleteByDomain succeeded with the hash cleanup failing")
	}
	chunks, err := storage.List(ctx, ListOptions{FilePath: "/r/doc.md"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("chunks left = %d, want 1: the chunk delete was not rolled back", len(chunks))
	}
}
