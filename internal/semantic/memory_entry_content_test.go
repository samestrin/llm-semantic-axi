package semantic

import (
	"strings"
	"testing"
)

// TestMemoryFileContent_Frontmatter pins the YAML frontmatter MemoryFileContent emits:
// every key must be present and the arrays must render as bracketed, comma-joined lists.
// Catches a regression that drops a key or emits a bare (unbracketed) array.
func TestMemoryFileContent_Frontmatter(t *testing.T) {
	entry := &MemoryEntry{
		ID:        "mem-2026-01-02-abcdef",
		Question:  "Why does the watchdog re-arm?",
		Answer:    "Because a live stream must not be cancelled.",
		Tags:      []string{" streaming ", "watchdog"},
		Sprints:   []string{"7.0_content_safety"},
		Files:     []string{" pkg/llmapi/streaming.go "},
		Source:    "sprint",
		CreatedAt: "2026-01-02T03:04:05Z",
	}

	got := entry.MemoryFileContent()

	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("content must open with a frontmatter fence, got %q", truncate(got, 40))
	}

	wantLines := []string{
		"id: mem-2026-01-02-abcdef",
		`question: "Why does the watchdog re-arm?"`,
		"created: 2026-01-02",
		`last_retrieved: ""`,
		"sprints: [7.0_content_safety]",
		"files: [pkg/llmapi/streaming.go]",
		"tags: [streaming, watchdog]",
		"retrievals: 0",
		"status: active",
		"type: sprint",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("frontmatter missing line %q\nfull output:\n%s", want, got)
		}
	}
}

// TestMemoryFileContent_TypeDerivation pins the three-way precedence for the `type:` key:
// a non-"manual" Source wins, otherwise the first tag, otherwise the literal "project".
func TestMemoryFileContent_TypeDerivation(t *testing.T) {
	tests := []struct {
		name   string
		source string
		tags   []string
		want   string
	}{
		{"source wins over tags", "decision", []string{"arch"}, "type: decision"},
		{"manual source falls through to tag", "manual", []string{"arch"}, "type: arch"},
		{"empty source falls through to tag", "", []string{"arch"}, "type: arch"},
		{"no source and no tags defaults to project", "manual", nil, "type: project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &MemoryEntry{
				Question:  "q",
				Answer:    "a",
				Source:    tt.source,
				Tags:      tt.tags,
				CreatedAt: "2026-01-02T03:04:05Z",
			}
			got := entry.MemoryFileContent()
			if !strings.Contains(got, tt.want+"\n") {
				t.Errorf("want %q in output, got:\n%s", tt.want, got)
			}
		})
	}
}

// TestMemoryFileContent_TitleTruncatedAndFlattened pins that the H1 title is cut to 60
// characters and has its newlines flattened, so a long multi-line question cannot break
// the generated markdown heading across lines.
func TestMemoryFileContent_TitleTruncatedAndFlattened(t *testing.T) {
	question := "line one\n" + strings.Repeat("x", 80)
	entry := &MemoryEntry{
		Question:  question,
		Answer:    "answer body",
		CreatedAt: "2026-01-02T03:04:05Z",
	}

	got := entry.MemoryFileContent()

	// Locate the H1 line in the body.
	var heading string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "# ") {
			heading = line
			break
		}
	}
	if heading == "" {
		t.Fatal("no H1 heading emitted")
	}
	title := strings.TrimPrefix(heading, "# ")
	if len(title) != 60 {
		t.Errorf("title length = %d, want 60 (truncation to 60 chars)", len(title))
	}
	if strings.Contains(title, "\n") {
		t.Errorf("title still contains a newline: %q", title)
	}
	if !strings.HasPrefix(title, "line one ") {
		t.Errorf("newline was not replaced by a space, title = %q", title)
	}
}

// TestMemoryFileContent_CodeReferenceSection pins the Code Reference body: a bullet per
// file (trimmed), or the literal "N/A" when the entry has no files.
func TestMemoryFileContent_CodeReferenceSection(t *testing.T) {
	withFiles := (&MemoryEntry{
		Question:  "q",
		Answer:    "the decision text",
		Files:     []string{"  a.go  ", "b.go"},
		CreatedAt: "2026-01-02T03:04:05Z",
	}).MemoryFileContent()

	idx := strings.Index(withFiles, "## Code Reference")
	if idx < 0 {
		t.Fatal("missing Code Reference section")
	}
	tail := withFiles[idx:]
	if !strings.Contains(tail, "- a.go\n") || !strings.Contains(tail, "- b.go\n") {
		t.Errorf("Code Reference should list each trimmed file, got:\n%s", tail)
	}
	if strings.Contains(tail, "N/A") {
		t.Error("Code Reference should not be N/A when files are present")
	}
	if !strings.Contains(withFiles, "## Decision\n\nthe decision text") {
		t.Errorf("answer should appear under ## Decision, got:\n%s", withFiles)
	}

	noFiles := (&MemoryEntry{
		Question:  "q",
		Answer:    "a",
		CreatedAt: "2026-01-02T03:04:05Z",
	}).MemoryFileContent()
	tail = noFiles[strings.Index(noFiles, "## Code Reference"):]
	if !strings.Contains(tail, "N/A") {
		t.Errorf("Code Reference should be N/A with no files, got:\n%s", tail)
	}
	if !strings.Contains(noFiles, "files: []\n") {
		t.Error("empty files should render as [] in frontmatter")
	}
}

// TestMemoryFileContent_ShortCreatedAt pins that a CreatedAt shorter than a full date is
// passed through verbatim rather than panicking on the [:10] slice.
func TestMemoryFileContent_ShortCreatedAt(t *testing.T) {
	entry := &MemoryEntry{Question: "q", Answer: "a", CreatedAt: "2026"}
	got := entry.MemoryFileContent()
	if !strings.Contains(got, "created: 2026\n") {
		t.Errorf("short CreatedAt should pass through unchanged, got:\n%s", got)
	}
}
