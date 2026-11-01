package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// seedRetrievals writes retrieval-log rows straight into the index, because
// the stats table is only populated as a side effect of searching and the
// stats command has to be testable without one.
func seedRetrievals(t *testing.T, id, query string, score float32, times int) {
	t.Helper()
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	defer storage.Close()
	for i := 0; i < times; i++ {
		err := storage.TrackMemoryRetrievalBatch(context.Background(),
			[]semantic.MemoryRetrieval{{MemoryID: id, Score: score}}, query)
		if err != nil {
			t.Fatalf("TrackMemoryRetrievalBatch: %v", err)
		}
	}
}

// ===== runMemoryStore =====

// TestMemoryStoreRoundTripsThroughTheIndex pins the whole store path: the entry
// reaches the database, and the ID it printed is the ID you can read back.
func TestMemoryStoreRoundTripsThroughTheIndex(t *testing.T) {
	newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)

	out := captureStdout(t, func() {
		err := runMemoryStore(context.Background(), memoryStoreOpts{
			question: "How are auth tokens handled?",
			answer:   "JWT with a 24h expiry",
			tags:     " auth , security ",
			source:   "sprint-42",
			sprints:  "sprint-42, sprint-43",
			files:    "src/auth.go , src/db.go",
		})
		if err != nil {
			t.Fatalf("runMemoryStore: %v", err)
		}
	})

	if !strings.Contains(out, "Memory stored:") {
		t.Fatalf("store did not confirm the write:\n%s", out)
	}
	id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "Memory stored:"))

	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	entry, err := storage.GetMemory(context.Background(), id)
	if err != nil {
		t.Fatalf("the reported id %q is not in the index: %v", id, err)
	}
	if entry.Answer != "JWT with a 24h expiry" {
		t.Errorf("answer = %q, want the stored answer", entry.Answer)
	}
	// Whitespace around a comma-separated value is the caller's shell, not part
	// of the tag.
	if len(entry.Tags) != 2 || entry.Tags[0] != "auth" || entry.Tags[1] != "security" {
		t.Errorf("tags = %v, want [auth security] with the padding trimmed", entry.Tags)
	}
	if len(entry.Sprints) != 2 || entry.Sprints[1] != "sprint-43" {
		t.Errorf("sprints = %v, want both references trimmed", entry.Sprints)
	}
	if len(entry.Files) != 2 || entry.Files[1] != "src/db.go" {
		t.Errorf("files = %v, want both references trimmed", entry.Files)
	}
	if entry.Source != "sprint-42" {
		t.Errorf("source = %q, want sprint-42", entry.Source)
	}
	if fe.callCount() == 0 {
		t.Error("the entry was stored without ever being embedded")
	}
}

// TestMemoryStoreWritesTheMarkdownFile covers --file-path, including the parent
// directory it has to create first.
func TestMemoryStoreWritesTheMarkdownFile(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	target := filepath.Join(dir, "memories", "nested", "auth.md")

	captureStdout(t, func() {
		err := runMemoryStore(context.Background(), memoryStoreOpts{
			question: "Where does the session live?",
			answer:   "In redis, keyed by user id",
			filePath: target,
		})
		if err != nil {
			t.Fatalf("runMemoryStore: %v", err)
		}
	})

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the memory file was not written: %v", err)
	}
	if !strings.Contains(string(content), "In redis, keyed by user id") {
		t.Errorf("memory file does not carry the answer:\n%s", content)
	}
}

// TestMemoryStoreFailsWhenTheFileCannotBeWritten pins the fail-fast ordering:
// the file is written before the database row, so an unwritable path must be
// an error rather than a half-done store.
func TestMemoryStoreFailsWhenTheFileCannotBeWritten(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	// A regular file where a directory has to go.
	blocker := writeFile(t, filepath.Join(dir, "blocked"), "not a directory")

	err := runMemoryStore(context.Background(), memoryStoreOpts{
		question: "q", answer: "a",
		filePath: filepath.Join(blocker, "memory.md"),
	})
	if err == nil {
		t.Fatal("an unwritable --file-path was accepted")
	}
	if !strings.Contains(err.Error(), "memory file") && !strings.Contains(err.Error(), "directory") {
		t.Errorf("error = %v, want it to blame the memory file path", err)
	}
}

// TestMemoryStoreJSONAndMinDisagreeOnDetail: --min is the token-optimized
// shape, so it drops the question and answer that --json carries. Emitting the
// same payload for both would make --min pointless.
func TestMemoryStoreJSONAndMinDisagreeOnDetail(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	full := captureStdout(t, func() {
		if err := runMemoryStore(context.Background(), memoryStoreOpts{
			question: "full question", answer: "full answer", jsonOutput: true,
		}); err != nil {
			t.Fatalf("runMemoryStore: %v", err)
		}
	})
	minimal := captureStdout(t, func() {
		if err := runMemoryStore(context.Background(), memoryStoreOpts{
			question: "min question", answer: "min answer", minOutput: true,
		}); err != nil {
			t.Fatalf("runMemoryStore: %v", err)
		}
	})

	var fullPayload, minPayload map[string]any
	decodeJSON(t, full, &fullPayload)
	decodeJSON(t, minimal, &minPayload)

	if fullPayload["status"] != "stored" {
		t.Errorf("--json status = %v, want stored", fullPayload["status"])
	}
	if fullPayload["question"] != "full question" {
		t.Errorf("--json dropped the question: %v", fullPayload)
	}
	if _, present := minPayload["question"]; present {
		t.Errorf("--min carried the question it exists to omit: %v", minPayload)
	}
	if minPayload["id"] == nil {
		t.Errorf("--min dropped the id, which is the only thing it must keep: %v", minPayload)
	}
}

// TestMemoryStoreSurfacesAnEmbedderFailure: a memory that cannot be embedded
// cannot be searched, so storing it anyway would be worse than failing.
func TestMemoryStoreSurfacesAnEmbedderFailure(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newBrokenEmbedder(t))

	err := runMemoryStore(context.Background(), memoryStoreOpts{question: "q", answer: "a"})
	if err == nil {
		t.Fatal("storing against a failing embedder returned nil")
	}
	if !strings.Contains(err.Error(), "failed to generate embedding") {
		t.Errorf("error = %v, want it to name the embedding step", err)
	}
}

// ===== runMemorySearch =====

// TestMemorySearchRanksTheRelevantEntryFirst is the point of the command. A
// search that returns rows in insertion order would still "work".
func TestMemorySearchRanksTheRelevantEntryFirst(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t,
		newMemory("mem-auth", "How should authentication tokens be validated?", "Verify the signature"),
		newMemory("mem-invoice", "How is an invoice rendered for billing?", "Through the pdf template"),
	)

	out := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "authentication tokens validated", topK: 5,
		}); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})

	authIdx := strings.Index(out, "mem-auth")
	invoiceIdx := strings.Index(out, "mem-invoice")
	if authIdx < 0 {
		t.Fatalf("the matching memory is missing from the results:\n%s", out)
	}
	if invoiceIdx >= 0 && invoiceIdx < authIdx {
		t.Errorf("the unrelated memory outranked the matching one:\n%s", out)
	}
	if !strings.Contains(out, "Q: How should authentication tokens be validated?") {
		t.Errorf("result body is missing the question:\n%s", out)
	}
}

// TestMemorySearchRecordsTheRetrieval pins the tracking side effect. Without
// it, `memory stats` has nothing to report and promotion candidates never
// surface.
func TestMemorySearchRecordsTheRetrieval(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-track", "How is caching configured?", "Redis, 5 minute ttl"))

	captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "caching configured", topK: 5,
		}); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})

	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	stats, err := storage.GetAllMemoryStats(context.Background())
	if err != nil {
		t.Fatalf("GetAllMemoryStats: %v", err)
	}
	if len(stats) == 0 {
		t.Fatal("searching recorded no retrieval at all")
	}
	if stats[0].MemoryID != "mem-track" || stats[0].RetrievalCount != 1 {
		t.Errorf("stats = %+v, want one retrieval of mem-track", stats[0])
	}
}

// TestMemorySearchFiltersByTag proves the tag filter reaches the query rather
// than being parsed and dropped.
func TestMemorySearchFiltersByTag(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t,
		newMemory("mem-tagged", "How should sessions expire?", "After 24 hours", "auth"),
		newMemory("mem-untagged", "How should sessions expire on mobile?", "After 24 hours", "mobile"),
	)

	out := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "sessions expire", topK: 10, tags: " auth ",
		}); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})

	if !strings.Contains(out, "mem-tagged") {
		t.Errorf("the tagged memory was filtered out:\n%s", out)
	}
	if strings.Contains(out, "mem-untagged") {
		t.Errorf("--tags did not exclude the other tag:\n%s", out)
	}
}

// TestMemorySearchHybridReturnsResults covers the RRF fusion branch, which is a
// different code path from the dense-only search below it.
func TestMemorySearchHybridReturnsResults(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-hybrid", "How is rate limiting enforced?", "Token bucket per api key"))

	out := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "rate limiting enforced", topK: 5, hybrid: true,
		}); err != nil {
			t.Fatalf("hybrid search: %v", err)
		}
	})

	if !strings.Contains(out, "mem-hybrid") {
		t.Errorf("hybrid search returned nothing:\n%s", out)
	}
}

// TestMemorySearchRejectsAnInvalidDecayHalfLife: a zero half-life makes the
// decay maths divide by zero, so it has to be caught before scoring.
func TestMemorySearchRejectsAnInvalidDecayHalfLife(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-decay", "How old is too old?", "Ninety days"))

	for _, hybrid := range []bool{false, true} {
		err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "how old", topK: 5, decay: true, decayHalfLife: 0, hybrid: hybrid,
		})
		if err == nil {
			t.Fatalf("hybrid=%v: a zero decay half-life was accepted", hybrid)
		}
		if !strings.Contains(err.Error(), "decay config") {
			t.Errorf("hybrid=%v: error = %v, want it to name the decay config", hybrid, err)
		}
	}
}

// TestMemorySearchAppliesTemporalDecay pins that --decay actually rescores.
// Without it the flag is accepted and ignored.
func TestMemorySearchAppliesTemporalDecay(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	old := newMemory("mem-old", "How is the cache invalidated?", "On write")
	old.CreatedAt = "2015-01-01T00:00:00Z"
	old.UpdatedAt = old.CreatedAt
	seedMemories(t, old)

	plain := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "cache invalidated", topK: 5, jsonOutput: true,
		}); err != nil {
			t.Fatalf("plain search: %v", err)
		}
	})
	decayed := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{
			query: "cache invalidated", topK: 5, jsonOutput: true,
			decay: true, decayHalfLife: 30,
		}); err != nil {
			t.Fatalf("decayed search: %v", err)
		}
	})

	var plainResults, decayedResults []struct {
		Score float64 `json:"score"`
	}
	decodeJSON(t, plain, &plainResults)
	decodeJSON(t, decayed, &decayedResults)
	if len(plainResults) == 0 || len(decayedResults) == 0 {
		t.Fatalf("expected a result from both searches; plain=%s decayed=%s", plain, decayed)
	}
	if decayedResults[0].Score >= plainResults[0].Score {
		t.Errorf("a decade-old memory scored %v with decay and %v without; --decay had no effect",
			decayedResults[0].Score, plainResults[0].Score)
	}
}

// TestMemorySearchWithoutAnIndexPointsAtTheFix: the error has to say what to
// run, because "not found" alone leaves the caller guessing.
func TestMemorySearchWithoutAnIndexPointsAtTheFix(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runMemorySearch(context.Background(), memorySearchOpts{query: "anything"})
	if err == nil {
		t.Fatal("searching with no index returned nil")
	}
	if !strings.Contains(err.Error(), "memory store") {
		t.Errorf("error = %v, want it to name the command that creates the index", err)
	}
}

// ===== outputMemoryJSON / outputMemoryText =====

// TestOutputMemoryTextReportsAnEmptyResultSet: no results is not an error, and
// printing nothing at all reads as a crash.
func TestOutputMemoryTextReportsAnEmptyResultSet(t *testing.T) {
	out := captureStdout(t, func() {
		if err := outputMemoryText("q", nil); err != nil {
			t.Fatalf("outputMemoryText: %v", err)
		}
	})
	if !strings.Contains(out, "No results found for") {
		t.Errorf("empty results printed %q, want an explicit message", out)
	}
}

// TestOutputMemoryTextListsTagsOnlyWhenPresent keeps the untagged case clean.
func TestOutputMemoryTextListsTagsOnlyWhenPresent(t *testing.T) {
	tagged := []semantic.MemorySearchResult{{
		Entry: newMemory("mem-1", "q1", "a1", "auth", "security"),
		Score: 0.9,
	}}
	untagged := []semantic.MemorySearchResult{{
		Entry: newMemory("mem-2", "q2", "a2"),
		Score: 0.5,
	}}

	withTags := captureStdout(t, func() {
		if err := outputMemoryText("q", tagged); err != nil {
			t.Fatalf("outputMemoryText: %v", err)
		}
	})
	withoutTags := captureStdout(t, func() {
		if err := outputMemoryText("q", untagged); err != nil {
			t.Fatalf("outputMemoryText: %v", err)
		}
	})

	if !strings.Contains(withTags, "Tags: auth, security") {
		t.Errorf("tags were not rendered:\n%s", withTags)
	}
	if !strings.Contains(withTags, "Score: 0.9000") {
		t.Errorf("score formatting changed:\n%s", withTags)
	}
	if strings.Contains(withoutTags, "Tags:") {
		t.Errorf("an empty tag list printed a Tags line:\n%s", withoutTags)
	}
}

// TestOutputMemoryJSONMinimalUsesShortKeys pins the token-optimized contract:
// id/q/s with a truncated question, not the full entry.
func TestOutputMemoryJSONMinimalUsesShortKeys(t *testing.T) {
	long := strings.Repeat("x", 80)
	results := []semantic.MemorySearchResult{{Entry: newMemory("mem-1", long, "a"), Score: 0.25}}

	out := captureStdout(t, func() {
		if err := outputMemoryJSON(results, true); err != nil {
			t.Fatalf("outputMemoryJSON: %v", err)
		}
	})

	var got []map[string]any
	decodeJSON(t, out, &got)
	if len(got) != 1 {
		t.Fatalf("minimal output = %v, want one entry", got)
	}
	q, _ := got[0]["q"].(string)
	if len(q) != 53 {
		t.Errorf("q = %d chars, want the question truncated to 50 plus an ellipsis", len(q))
	}
	if _, present := got[0]["answer"]; present {
		t.Errorf("minimal output carried the full entry: %v", got[0])
	}
}

// ===== runMemoryPromote =====

// TestMemoryPromoteAppendsUnderTheSectionAndMarksTheEntry pins both halves:
// the file edit and the status change that stops it being promoted twice.
func TestMemoryPromoteAppendsUnderTheSectionAndMarksTheEntry(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-promote", "Which database?", "Postgres", "infra"))
	target := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, target, "# Project\n\nSome existing prose.\n")

	out := captureStdout(t, func() {
		if err := runMemoryPromote(context.Background(), memoryPromoteOpts{
			id: "mem-promote", target: target, section: "Learned Clarifications",
		}); err != nil {
			t.Fatalf("runMemoryPromote: %v", err)
		}
	})

	if !strings.Contains(out, "Promoted memory mem-promote") {
		t.Errorf("promote did not confirm:\n%s", out)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "## Learned Clarifications") {
		t.Errorf("the section header was not created:\n%s", text)
	}
	if !strings.Contains(text, "**Q:** Which database? **A:** Postgres") {
		t.Errorf("the entry was not appended:\n%s", text)
	}
	if !strings.Contains(text, "_(Tags: infra)_") {
		t.Errorf("tags were dropped from the promoted entry:\n%s", text)
	}
	if !strings.Contains(text, "Some existing prose.") {
		t.Errorf("promote overwrote the existing file:\n%s", text)
	}

	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	entry, err := storage.GetMemory(context.Background(), "mem-promote")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if entry.Status != semantic.MemoryStatusPromoted {
		t.Errorf("status = %q, want promoted", entry.Status)
	}
}

// TestMemoryPromoteReusesAnExistingSection: appending a second header every
// time would leave the file with one section per promotion.
func TestMemoryPromoteReusesAnExistingSection(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-a", "First?", "Yes"), newMemory("mem-b", "Second?", "No"))
	target := writeFile(t, filepath.Join(dir, "CLAUDE.md"), "# Project\n\n## Notes\n\nexisting\n")

	for _, id := range []string{"mem-a", "mem-b"} {
		captureStdout(t, func() {
			if err := runMemoryPromote(context.Background(), memoryPromoteOpts{
				id: id, target: target, section: "Notes",
			}); err != nil {
				t.Fatalf("promote %s: %v", id, err)
			}
		})
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if n := strings.Count(string(content), "## Notes"); n != 1 {
		t.Errorf("the section header appears %d times, want 1:\n%s", n, content)
	}
	for _, want := range []string{"First?", "Second?"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("%q is missing from the section:\n%s", want, content)
		}
	}
}

// TestMemoryPromoteRefusesARepeatWithoutForce stops the same entry being
// appended to CLAUDE.md on every run of a hook.
func TestMemoryPromoteRefusesARepeatWithoutForce(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	promoted := newMemory("mem-done", "Already?", "Yes")
	promoted.Status = semantic.MemoryStatusPromoted
	seedMemories(t, promoted)
	target := filepath.Join(dir, "CLAUDE.md")

	err := runMemoryPromote(context.Background(), memoryPromoteOpts{id: "mem-done", target: target})
	if err == nil {
		t.Fatal("a second promotion was accepted without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %v, want it to name the override flag", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Error("the refused promotion still wrote the target file")
	}
}

// TestMemoryPromoteForceOverridesTheGuard is the other half of the same rule.
func TestMemoryPromoteForceOverridesTheGuard(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	promoted := newMemory("mem-done", "Already?", "Yes")
	promoted.Status = semantic.MemoryStatusPromoted
	seedMemories(t, promoted)
	target := filepath.Join(dir, "CLAUDE.md")

	out := captureStdout(t, func() {
		if err := runMemoryPromote(context.Background(), memoryPromoteOpts{
			id: "mem-done", target: target, section: "Learned", force: true, jsonOutput: true,
		}); err != nil {
			t.Fatalf("forced promote: %v", err)
		}
	})

	var payload map[string]any
	decodeJSON(t, out, &payload)
	if payload["promoted"] != true || payload["id"] != "mem-done" {
		t.Errorf("payload = %v, want a promoted mem-done", payload)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the forced promotion did not create the target: %v", err)
	}
	if !strings.Contains(string(content), "Already?") {
		t.Errorf("the entry was not written:\n%s", content)
	}
}

// TestMemoryPromoteFailsOnAnUnknownID: promoting nothing must not create an
// empty section in CLAUDE.md.
func TestMemoryPromoteFailsOnAnUnknownID(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-real", "q", "a"))

	err := runMemoryPromote(context.Background(), memoryPromoteOpts{
		id: "mem-missing", target: filepath.Join(dir, "CLAUDE.md"),
	})
	if err == nil {
		t.Fatal("promoting an unknown id returned nil")
	}
	if !strings.Contains(err.Error(), "failed to get memory") {
		t.Errorf("error = %v, want it to name the lookup step", err)
	}
}

// ===== runMemoryImport =====

const sampleClarifications = `entries:
  - id: clar-1
    canonical_question: How should retries be bounded?
    current_answer: Three attempts with jitter
    context_tags: [resilience]
    sprint_id: sprint-7
    occurrences: 2
    status: pending
    created_at: "2024-01-01T00:00:00Z"
    updated_at: "2024-01-02T00:00:00Z"
  - id: clar-2
    canonical_question: Which log level for retries?
    current_answer: Warn
    status: promoted
`

// TestMemoryImportStoresEveryEntry pins the migration path, including the
// status mapping that decides whether an imported entry still needs promoting.
func TestMemoryImportStoresEveryEntry(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	source := writeFile(t, filepath.Join(dir, "clarifications.yaml"), sampleClarifications)

	out := captureStdout(t, func() {
		if err := runMemoryImport(context.Background(), memoryImportOpts{source: source}); err != nil {
			t.Fatalf("runMemoryImport: %v", err)
		}
	})

	if !strings.Contains(out, "2 imported, 0 skipped, 0 errors") {
		t.Errorf("import summary = %q, want two imports", strings.TrimSpace(out))
	}

	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	first, err := storage.GetMemory(context.Background(), "clar-1")
	if err != nil {
		t.Fatalf("clar-1 was not imported: %v", err)
	}
	if first.Status != semantic.MemoryStatusPending {
		t.Errorf("clar-1 status = %q, want pending", first.Status)
	}
	if first.Source != "sprint-7" {
		t.Errorf("clar-1 source = %q, want the sprint id", first.Source)
	}
	if first.Occurrences != 2 {
		t.Errorf("clar-1 occurrences = %d, want 2", first.Occurrences)
	}
	second, err := storage.GetMemory(context.Background(), "clar-2")
	if err != nil {
		t.Fatalf("clar-2 was not imported: %v", err)
	}
	if second.Status != semantic.MemoryStatusPromoted {
		t.Errorf("clar-2 status = %q, want promoted", second.Status)
	}
}

// TestMemoryImportSkipsEntriesThatAlreadyExist keeps a re-run idempotent, and
// --force is what overrides it.
func TestMemoryImportSkipsEntriesThatAlreadyExist(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	source := writeFile(t, filepath.Join(dir, "clarifications.yaml"), sampleClarifications)
	captureStdout(t, func() {
		if err := runMemoryImport(context.Background(), memoryImportOpts{source: source}); err != nil {
			t.Fatalf("first import: %v", err)
		}
	})

	again := captureStdout(t, func() {
		if err := runMemoryImport(context.Background(), memoryImportOpts{
			source: source, jsonOutput: true,
		}); err != nil {
			t.Fatalf("second import: %v", err)
		}
	})
	forced := captureStdout(t, func() {
		if err := runMemoryImport(context.Background(), memoryImportOpts{
			source: source, force: true, jsonOutput: true,
		}); err != nil {
			t.Fatalf("forced import: %v", err)
		}
	})

	var second, third map[string]any
	decodeJSON(t, again, &second)
	decodeJSON(t, forced, &third)
	if second["skipped"] != float64(2) || second["imported"] != float64(0) {
		t.Errorf("re-import = %v, want both entries skipped", second)
	}
	if third["imported"] != float64(2) || third["skipped"] != float64(0) {
		t.Errorf("--force import = %v, want both entries rewritten", third)
	}
}

// TestMemoryImportDryRunTouchesNothing: the preview must not open storage or
// write rows, or "preview" is a lie.
func TestMemoryImportDryRunTouchesNothing(t *testing.T) {
	dir := newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)
	source := writeFile(t, filepath.Join(dir, "clarifications.yaml"), sampleClarifications)

	out := captureStdout(t, func() {
		if err := runMemoryImport(context.Background(), memoryImportOpts{
			source: source, dryRun: true,
		}); err != nil {
			t.Fatalf("dry run: %v", err)
		}
	})

	if !strings.Contains(out, "would import 2 entries") {
		t.Errorf("dry run did not describe the work:\n%s", out)
	}
	if !strings.Contains(out, "[clar-1]") {
		t.Errorf("dry run did not list the entries:\n%s", out)
	}
	if fe.callCount() != 0 {
		t.Errorf("dry run made %d embedding calls, want none", fe.callCount())
	}
	if _, err := os.Stat(filepath.Join(dir, ".index", "semantic.db")); err == nil {
		t.Error("dry run created an index")
	}
}

// TestMemoryImportRejectsABadSource covers the two failures a caller actually
// hits: a path typo and a malformed file.
func TestMemoryImportRejectsABadSource(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	missing := runMemoryImport(context.Background(), memoryImportOpts{source: "nope.yaml"})
	if missing == nil || !strings.Contains(missing.Error(), "read source file") {
		t.Errorf("missing source error = %v, want it to name the read", missing)
	}

	bad := writeFile(t, filepath.Join(dir, "bad.yaml"), "entries: [this: is: not: yaml")
	parseErr := runMemoryImport(context.Background(), memoryImportOpts{source: bad})
	if parseErr == nil || !strings.Contains(parseErr.Error(), "parse YAML") {
		t.Errorf("malformed source error = %v, want it to name the parse", parseErr)
	}
}

// TestMemoryImportReportsAnEmptySource: zero entries is not an error, but it
// has to say so rather than printing a success summary of nothing.
func TestMemoryImportReportsAnEmptySource(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	source := writeFile(t, filepath.Join(dir, "empty.yaml"), "entries: []\n")

	out := captureStdout(t, func() {
		if err := runMemoryImport(context.Background(), memoryImportOpts{source: source}); err != nil {
			t.Fatalf("runMemoryImport: %v", err)
		}
	})

	if !strings.Contains(out, "No entries found") {
		t.Errorf("empty source printed %q, want an explicit message", out)
	}
}

// TestMemoryImportCountsEmbeddingFailures: one unembeddable entry must not
// abort the migration, but it must be counted rather than reported as success.
func TestMemoryImportCountsEmbeddingFailures(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newBrokenEmbedder(t))
	source := writeFile(t, filepath.Join(dir, "clarifications.yaml"), sampleClarifications)

	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runMemoryImport(context.Background(), memoryImportOpts{
				source: source, jsonOutput: true,
			}); err != nil {
				t.Fatalf("runMemoryImport: %v", err)
			}
		})
	})

	var payload map[string]any
	decodeJSON(t, out, &payload)
	if payload["errors"] != float64(2) || payload["imported"] != float64(0) {
		t.Errorf("payload = %v, want both entries counted as errors", payload)
	}
	if !strings.Contains(stderr, "failed to embed entry") {
		t.Errorf("the failures were not reported on stderr:\n%s", stderr)
	}
}

// ===== runMemoryList =====

// TestMemoryListRendersEveryStoredEntry pins the human listing.
func TestMemoryListRendersEveryStoredEntry(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t,
		newMemory("mem-1", "First question?", "First answer", "alpha"),
		newMemory("mem-2", "Second question?", "Second answer"),
	)

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	for _, want := range []string{"mem-1", "First question?", "First answer", "Tags: alpha", "mem-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q:\n%s", want, out)
		}
	}
}

// TestMemoryListFiltersByStatus proves --status narrows the query rather than
// being accepted and ignored.
func TestMemoryListFiltersByStatus(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	promoted := newMemory("mem-promoted", "Promoted?", "Yes")
	promoted.Status = semantic.MemoryStatusPromoted
	seedMemories(t, newMemory("mem-pending", "Pending?", "Yes"), promoted)

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{
			limit: 50, status: "promoted",
		}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	if !strings.Contains(out, "mem-promoted") {
		t.Errorf("the promoted entry was filtered out:\n%s", out)
	}
	if strings.Contains(out, "mem-pending") {
		t.Errorf("--status promoted returned a pending entry:\n%s", out)
	}
}

// TestMemoryListMinimalKeepsOnlyTheIdentifyingFields pins the --min contract.
func TestMemoryListMinimalKeepsOnlyTheIdentifyingFields(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "A long answer nobody needs in min mode"))

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{
			limit: 50, minOutput: true,
		}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	var got []map[string]any
	decodeJSON(t, out, &got)
	if len(got) != 1 {
		t.Fatalf("min output = %v, want one entry", got)
	}
	if got[0]["id"] != "mem-1" || got[0]["status"] != "pending" {
		t.Errorf("min entry = %v, want id and status", got[0])
	}
	if _, present := got[0]["answer"]; present {
		t.Errorf("min output carried the answer: %v", got[0])
	}
}

// TestMemoryListSaysSoWhenEmpty: an empty index must not print a bare prompt.
func TestMemoryListSaysSoWhenEmpty(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t) // creates the index with no rows

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	if !strings.Contains(out, "No memories found.") {
		t.Errorf("empty listing printed %q", out)
	}
}

// TestMemoryListWithoutAnIndexIsAnError, not an empty list — an empty list
// would read as "you have no memories" when the index is simply missing.
func TestMemoryListWithoutAnIndexIsAnError(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runMemoryList(context.Background(), memoryListOpts{limit: 50})
	if err == nil {
		t.Fatal("listing with no index returned nil")
	}
	if !strings.Contains(err.Error(), "semantic index not found") {
		t.Errorf("error = %v, want a missing-index message", err)
	}
}

// ===== runMemoryDelete =====

// TestMemoryDeleteRemovesTheRow pins the delete and the confirmation line.
func TestMemoryDeleteRemovesTheRow(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-gone", "Delete me?", "Yes"), newMemory("mem-stays", "Keep me?", "Yes"))

	out := captureStdout(t, func() {
		if err := runMemoryDelete(context.Background(), memoryDeleteOpts{id: "mem-gone"}); err != nil {
			t.Fatalf("runMemoryDelete: %v", err)
		}
	})

	if !strings.Contains(out, "Deleted memory: mem-gone") {
		t.Errorf("delete did not confirm:\n%s", out)
	}
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	if _, err := storage.GetMemory(context.Background(), "mem-gone"); err == nil {
		t.Error("the entry is still in the index after delete")
	}
	if _, err := storage.GetMemory(context.Background(), "mem-stays"); err != nil {
		t.Errorf("delete removed an unrelated entry: %v", err)
	}
}

// TestMemoryDeleteJSONReportsTheID so a script can confirm which row went.
func TestMemoryDeleteJSONReportsTheID(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-gone", "Delete me?", "Yes"))

	out := captureStdout(t, func() {
		if err := runMemoryDelete(context.Background(), memoryDeleteOpts{
			id: "mem-gone", jsonOutput: true,
		}); err != nil {
			t.Fatalf("runMemoryDelete: %v", err)
		}
	})

	var payload map[string]any
	decodeJSON(t, out, &payload)
	if payload["id"] != "mem-gone" || payload["deleted"] != true || payload["status"] != "deleted" {
		t.Errorf("payload = %v, want a deleted mem-gone", payload)
	}
}

// TestMemoryDeleteFailsOnAnUnknownID: silently succeeding would hide a typo.
func TestMemoryDeleteFailsOnAnUnknownID(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-real", "q", "a"))

	err := runMemoryDelete(context.Background(), memoryDeleteOpts{id: "mem-missing"})
	if err == nil {
		t.Fatal("deleting an unknown id returned nil")
	}
	if !strings.Contains(err.Error(), "failed to delete memory") {
		t.Errorf("error = %v, want it to name the delete", err)
	}
}

// ===== runMemoryStats =====

// TestMemoryStatsRendersTheTable pins the default table, its summary counts and
// the per-row retrieval figures.
func TestMemoryStatsRendersTheTable(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	promoted := newMemory("mem-hot", "Hot question?", "Hot answer", "auth")
	promoted.Status = semantic.MemoryStatusPromoted
	seedMemories(t, promoted, newMemory("mem-cold", "Cold question?", "Cold answer", "billing"))
	seedRetrievals(t, "mem-hot", "hot query", 0.8, 3)
	seedRetrievals(t, "mem-cold", "cold query", 0.2, 1)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	if !strings.Contains(out, "Total: 2 | Pending: 1 | Promoted: 1") {
		t.Errorf("summary line is wrong:\n%s", out)
	}
	if !strings.Contains(out, "Retrievals") || !strings.Contains(out, "Avg Score") {
		t.Errorf("the table header is missing:\n%s", out)
	}
	if !strings.Contains(out, "mem-hot") || !strings.Contains(out, "mem-cold") {
		t.Errorf("rows are missing:\n%s", out)
	}
	if !strings.Contains(out, "0.8000") {
		t.Errorf("the average score was not rendered:\n%s", out)
	}
}

// TestMemoryStatsFilters covers each filter independently, because a filter
// that is parsed but never applied still returns plausible-looking output.
func TestMemoryStatsFilters(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	promoted := newMemory("mem-hot", "Hot question?", "Hot answer", "auth")
	promoted.Status = semantic.MemoryStatusPromoted
	seedMemories(t, promoted, newMemory("mem-cold", "Cold question?", "Cold answer", "billing"))
	seedRetrievals(t, "mem-hot", "hot query", 0.8, 3)
	seedRetrievals(t, "mem-cold", "cold query", 0.2, 1)

	tests := []struct {
		name string
		opts memoryStatsOpts
		want string
		deny string
	}{
		{"by id", memoryStatsOpts{id: "mem-hot"}, "mem-hot", "mem-cold"},
		{"by status", memoryStatsOpts{status: "pending"}, "mem-cold", "mem-hot"},
		{"by tag", memoryStatsOpts{tagsFilter: "BILL"}, "mem-cold", "mem-hot"},
		{"by retrieval count", memoryStatsOpts{minRetrievals: 2}, "mem-hot", "mem-cold"},
		{"by limit", memoryStatsOpts{limit: 1}, "mem-hot", "mem-cold"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if err := runMemoryStats(context.Background(), tt.opts); err != nil {
					t.Fatalf("runMemoryStats: %v", err)
				}
			})
			if !strings.Contains(out, tt.want) {
				t.Errorf("filter dropped %q:\n%s", tt.want, out)
			}
			if strings.Contains(out, tt.deny) {
				t.Errorf("filter kept %q:\n%s", tt.deny, out)
			}
		})
	}
}

// TestMemoryStatsDistinguishesEmptyFromFilteredEmpty: "no memories" and "none
// matched your filters" are different diagnoses.
func TestMemoryStatsDistinguishesEmptyFromFilteredEmpty(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))
	seedRetrievals(t, "mem-1", "query", 0.5, 1)

	filtered := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{id: "mem-nope"}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})
	if !strings.Contains(filtered, "No memories found matching filters.") {
		t.Errorf("filtered-empty printed %q", filtered)
	}

	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t)
	bare := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})
	if !strings.Contains(bare, "No memories found.") || strings.Contains(bare, "matching filters") {
		t.Errorf("unfiltered-empty printed %q", bare)
	}
}

// TestMemoryStatsRejectsANegativeMinRetrievals: a negative floor matches
// everything, which silently turns the promotion-candidate filter off.
func TestMemoryStatsRejectsANegativeMinRetrievals(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))

	err := runMemoryStats(context.Background(), memoryStatsOpts{minRetrievals: -1})
	if err == nil {
		t.Fatal("a negative --min-retrievals was accepted")
	}
	if !strings.Contains(err.Error(), "min-retrievals") {
		t.Errorf("error = %v, want it to name the flag", err)
	}
}

// TestMemoryStatsJSONAndMinShapes pins both machine formats. --min uses short
// keys and stays compact; --json uses full keys and a summary block.
func TestMemoryStatsJSONAndMinShapes(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer", "alpha"))
	seedRetrievals(t, "mem-1", "query", 0.42, 2)

	full := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{jsonOutput: true}); err != nil {
			t.Fatalf("runMemoryStats --json: %v", err)
		}
	})
	minimal := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{minOutput: true}); err != nil {
			t.Fatalf("runMemoryStats --min: %v", err)
		}
	})

	var fullPayload struct {
		TotalMemories int `json:"total_memories"`
		Pending       int `json:"pending"`
		Results       []struct {
			ID             string `json:"id"`
			RetrievalCount int    `json:"retrieval_count"`
		} `json:"results"`
	}
	decodeJSON(t, full, &fullPayload)
	if fullPayload.TotalMemories != 1 || fullPayload.Pending != 1 {
		t.Errorf("--json summary = %+v, want one pending memory", fullPayload)
	}
	if len(fullPayload.Results) != 1 || fullPayload.Results[0].RetrievalCount != 2 {
		t.Errorf("--json results = %+v, want two retrievals of mem-1", fullPayload.Results)
	}

	if strings.Contains(strings.TrimSpace(minimal), "\n") {
		t.Errorf("--min output is not compact:\n%s", minimal)
	}
	var minPayload struct {
		T int `json:"t"`
		R []struct {
			ID string `json:"id"`
			R  int    `json:"r"`
		} `json:"r"`
	}
	decodeJSON(t, minimal, &minPayload)
	if minPayload.T != 1 || len(minPayload.R) != 1 || minPayload.R[0].R != 2 {
		t.Errorf("--min payload = %+v, want the abbreviated shape", minPayload)
	}
}

// TestMemoryStatsWithoutAnIndexIsAnError keeps the missing-index diagnosis
// consistent across the memory commands.
func TestMemoryStatsWithoutAnIndexIsAnError(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runMemoryStats(context.Background(), memoryStatsOpts{})
	if err == nil {
		t.Fatal("stats with no index returned nil")
	}
	if !strings.Contains(err.Error(), "semantic index not found") {
		t.Errorf("error = %v, want a missing-index message", err)
	}
}

// ===== runMemoryHistory =====

// TestMemoryHistoryListsEveryRecordedQuery pins the --history table, including
// the query text that says why a memory was retrieved.
func TestMemoryHistoryListsEveryRecordedQuery(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "Which cache?", "Redis"))
	seedRetrievals(t, "mem-1", "which cache do we use", 0.77, 2)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			id: "mem-1", showHistory: true,
		}); err != nil {
			t.Fatalf("history: %v", err)
		}
	})

	if !strings.Contains(out, "Memory: Which cache?") {
		t.Errorf("history did not identify the memory:\n%s", out)
	}
	if !strings.Contains(out, "Retrieval History (2 entries)") {
		t.Errorf("history count is wrong:\n%s", out)
	}
	if !strings.Contains(out, "which cache do we use") {
		t.Errorf("the recorded query is missing:\n%s", out)
	}
	if !strings.Contains(out, "0.7700") {
		t.Errorf("the recorded score is missing:\n%s", out)
	}
}

// TestMemoryHistoryReportsAnEmptyLog separately from a missing memory.
func TestMemoryHistoryReportsAnEmptyLog(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "Never retrieved?", "Correct"))

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			id: "mem-1", showHistory: true,
		}); err != nil {
			t.Fatalf("history: %v", err)
		}
	})

	if !strings.Contains(out, "No retrieval history found.") {
		t.Errorf("an empty log printed %q", out)
	}
}

// TestMemoryHistoryFailsOnAnUnknownMemory and says how to find a real ID.
func TestMemoryHistoryFailsOnAnUnknownMemory(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))

	err := runMemoryStats(context.Background(), memoryStatsOpts{id: "mem-nope", showHistory: true})
	if err == nil {
		t.Fatal("history for an unknown memory returned nil")
	}
	if !strings.Contains(err.Error(), "memory list") {
		t.Errorf("error = %v, want it to point at the listing command", err)
	}
}

// TestMemoryHistoryJSONAndMinShapes pins both machine formats for --history.
func TestMemoryHistoryJSONAndMinShapes(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "Which cache?", "Redis"))
	seedRetrievals(t, "mem-1", "which cache do we use", 0.77, 2)

	full := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			id: "mem-1", showHistory: true, jsonOutput: true,
		}); err != nil {
			t.Fatalf("history --json: %v", err)
		}
	})
	minimal := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			id: "mem-1", showHistory: true, minOutput: true,
		}); err != nil {
			t.Fatalf("history --min: %v", err)
		}
	})

	var fullPayload struct {
		MemoryID     string `json:"memory_id"`
		HistoryCount int    `json:"history_count"`
		History      []struct {
			Query string `json:"query"`
		} `json:"history"`
	}
	decodeJSON(t, full, &fullPayload)
	if fullPayload.MemoryID != "mem-1" || fullPayload.HistoryCount != 2 {
		t.Errorf("--json history = %+v, want two entries for mem-1", fullPayload)
	}
	if len(fullPayload.History) != 2 || fullPayload.History[0].Query != "which cache do we use" {
		t.Errorf("--json history entries = %+v", fullPayload.History)
	}

	if strings.Contains(strings.TrimSpace(minimal), "\n") {
		t.Errorf("--min history is not compact:\n%s", minimal)
	}
	var minPayload struct {
		I  string `json:"i"`
		RC int    `json:"rc"`
	}
	decodeJSON(t, minimal, &minPayload)
	if minPayload.I != "mem-1" || minPayload.RC != 2 {
		t.Errorf("--min history = %+v, want the abbreviated shape", minPayload)
	}
}

// ===== runMemoryPrune =====

// TestMemoryPruneDeletesOldLogEntries pins the prune, which is the only
// destructive memory operation that runs without naming a row.
func TestMemoryPruneDeletesOldLogEntries(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))
	seedRetrievals(t, "mem-1", "query", 0.5, 2)

	// Age the log rows past the cutoff.
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	before, err := storage.GetMemoryRetrievalHistory(context.Background(), "mem-1", 50)
	if err != nil {
		t.Fatalf("GetMemoryRetrievalHistory: %v", err)
	}
	storage.Close()
	if len(before) != 2 {
		t.Fatalf("expected two log rows to prune, got %d", len(before))
	}

	// Nothing is old enough yet, so a prune must report zero rather than
	// deleting the fresh rows.
	fresh := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: 30, skipConfirm: true,
		}); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	if !strings.Contains(fresh, "No log entries to delete.") {
		t.Errorf("a prune with nothing to do printed %q", fresh)
	}

	reopened, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	after, err := reopened.GetMemoryRetrievalHistory(context.Background(), "mem-1", 50)
	reopened.Close()
	if err != nil {
		t.Fatalf("GetMemoryRetrievalHistory: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("the no-op prune deleted %d fresh rows", 2-len(after))
	}
}

// TestMemoryPruneValidatesTheWindow: a non-positive or absurd window would
// either delete everything or nothing, and both are silent.
func TestMemoryPruneValidatesTheWindow(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))

	for _, tt := range []struct {
		days int
		want string
	}{
		{0, "positive integer"},
		{-5, "positive integer"},
		{40000, "36500"},
	} {
		err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: tt.days, skipConfirm: true,
		})
		if err == nil {
			t.Fatalf("--older-than %d was accepted", tt.days)
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("--older-than %d error = %v, want it to mention %q", tt.days, err, tt.want)
		}
	}
}

// TestMemoryPruneAbortsWithoutConfirmation covers both interactive answers.
// Defaulting to yes on a destructive command is the bug this guards.
func TestMemoryPruneAbortsWithoutConfirmation(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))
	seedRetrievals(t, "mem-1", "query", 0.5, 1)

	withStdin(t, "n\n")
	declined := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: 30,
		}); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	if !strings.Contains(declined, "Aborted.") {
		t.Errorf("answering no did not abort:\n%s", declined)
	}
	if !strings.Contains(declined, "Are you sure?") {
		t.Errorf("no confirmation prompt was shown:\n%s", declined)
	}

	withStdin(t, "")
	silent := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: 30,
		}); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	if !strings.Contains(silent, "Aborted (non-interactive input)") {
		t.Errorf("a closed stdin did not abort:\n%s", silent)
	}
}

// TestMemoryPruneConfirmedProceeds is the accept half of the same prompt.
func TestMemoryPruneConfirmedProceeds(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))
	seedRetrievals(t, "mem-1", "query", 0.5, 1)

	withStdin(t, "y\n")
	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: 30,
		}); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})

	if strings.Contains(out, "Aborted") {
		t.Errorf("answering yes still aborted:\n%s", out)
	}
	if !strings.Contains(out, "No log entries to delete.") {
		t.Errorf("the confirmed prune did not run:\n%s", out)
	}
}

// TestMemoryPruneJSONAndMinShapes pins the machine formats for --prune.
func TestMemoryPruneJSONAndMinShapes(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "q", "a"))

	full := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: 30, skipConfirm: true, jsonOutput: true,
		}); err != nil {
			t.Fatalf("prune --json: %v", err)
		}
	})
	minimal := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{
			pruneMode: true, olderThan: 30, skipConfirm: true, minOutput: true,
		}); err != nil {
			t.Fatalf("prune --min: %v", err)
		}
	})

	var fullPayload struct {
		DeletedCount  int `json:"deleted_count"`
		OlderThanDays int `json:"older_than_days"`
	}
	decodeJSON(t, full, &fullPayload)
	if fullPayload.OlderThanDays != 30 {
		t.Errorf("--json prune = %+v, want the window echoed back", fullPayload)
	}
	if strings.Contains(strings.TrimSpace(minimal), "\n") {
		t.Errorf("--min prune is not compact:\n%s", minimal)
	}
	var minPayload struct {
		O int `json:"o"`
	}
	decodeJSON(t, minimal, &minPayload)
	if minPayload.O != 30 {
		t.Errorf("--min prune = %+v, want the abbreviated window", minPayload)
	}
}

// ===== formatDisplayDate =====

// TestFormatDisplayDate pins the ISO-to-readable conversion used in the stats
// table. A wrong slice offset here prints a stray T or drops the time.
func TestFormatDisplayDate(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "Unknown"},
		{"2024-03-01T14:25:36Z", "2024-03-01 14:25"},
		{"2024-03-01", "2024-03-01"},
		{"short", "short"},
	}
	for _, tt := range tests {
		if got := formatDisplayDate(tt.in); got != tt.want {
			t.Errorf("formatDisplayDate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ===== displayStatsTable =====

// TestDisplayStatsTableTruncatesLongQuestions keeps the columns aligned; an
// untruncated question wraps and destroys the table.
func TestDisplayStatsTableTruncatesLongQuestions(t *testing.T) {
	long := strings.Repeat("q", 120)

	out := captureStdout(t, func() {
		displayStatsTable([]displayRow{
			{ID: "mem-1", Question: long, Created: "2024-01-01 00:00", Retrievals: 3, LastAccessed: "Never", AvgScore: 0.5},
			{ID: "mem-2", Question: "short", Created: "2024-01-01 00:00", Retrievals: 0, LastAccessed: "Never"},
		})
	})

	if strings.Contains(out, long) {
		t.Errorf("a 120-character question was printed in full:\n%s", out)
	}
	if !strings.Contains(out, strings.Repeat("q", 57)+"...") {
		t.Errorf("the question was not truncated to 57 plus an ellipsis:\n%s", out)
	}
	if !strings.Contains(out, "0.5000") {
		t.Errorf("a non-zero score was not formatted:\n%s", out)
	}
	// A zero score prints blank rather than 0.0000, so an unscored row reads as
	// unscored rather than as a perfectly bad match.
	if strings.Contains(out, "0.0000") {
		t.Errorf("a zero score was rendered as a number:\n%s", out)
	}
}
