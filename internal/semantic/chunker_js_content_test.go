package semantic

import (
	"strings"
	"testing"
)

// TestJSChunker_ExtractContent_Bounds pins the clamping in extractContent: a negative
// start is pulled to 0, an end past the last line is pulled in, and an inverted range
// yields the empty string rather than panicking on the slice.
func TestJSChunker_ExtractContent_Bounds(t *testing.T) {
	c := NewJSChunker()
	lines := []string{"one", "two", "three"}

	if got := c.extractContent(lines, 0, 2); got != "one\ntwo\nthree" {
		t.Errorf("extractContent(0,2) = %q, want the whole slice joined", got)
	}
	if got := c.extractContent(lines, -5, 1); got != "one\ntwo" {
		t.Errorf("extractContent(-5,1) = %q, want the start clamped to 0", got)
	}
	if got := c.extractContent(lines, 1, 99); got != "two\nthree" {
		t.Errorf("extractContent(1,99) = %q, want the end clamped to the last line", got)
	}
	if got := c.extractContent(lines, 2, 1); got != "" {
		t.Errorf("extractContent(2,1) = %q, want \"\" for an inverted range", got)
	}
	if got := c.extractContent(nil, 0, 0); got != "" {
		t.Errorf("extractContent(nil) = %q, want \"\"", got)
	}
}

// TestJSChunker_ExtractContent_Truncates pins the maxChunkSize guard that keeps a chunk
// inside the embedding model's context window: oversized content is cut back to a line
// boundary and marked, so an over-long file cannot silently blow the token budget.
func TestJSChunker_ExtractContent_Truncates(t *testing.T) {
	c := NewJSChunker()

	// 200 lines of 40 characters each is far past the 1500-character limit.
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = strings.Repeat("x", 39)
	}

	got := c.extractContent(lines, 0, len(lines)-1)
	if !strings.HasSuffix(got, "\n// ... truncated") {
		t.Fatalf("oversized content was not marked as truncated, tail = %q", got[max(0, len(got)-40):])
	}
	body := strings.TrimSuffix(got, "\n// ... truncated")
	if len(body) > 1500 {
		t.Errorf("truncated body is %d chars, want at most the 1500-char limit", len(body))
	}
	if strings.HasSuffix(body, "\n") {
		t.Errorf("truncation should cut at a line boundary without a trailing newline, tail = %q", body[len(body)-5:])
	}

	// Content under the limit is returned untouched.
	short := c.extractContent(lines[:3], 0, 2)
	if strings.Contains(short, "truncated") {
		t.Errorf("short content was truncated: %q", short)
	}
}

// TestSemanticError_FormatWithHint pins that the hint is appended only when one is set,
// so a hintless error does not gain a dangling "Hint:" line.
func TestSemanticError_FormatWithHint_NoHint(t *testing.T) {
	withHint := ErrIndexNotFound()
	got := withHint.FormatWithHint()
	if !strings.Contains(got, "semantic index not found") {
		t.Errorf("FormatWithHint() = %q, want it to carry the message", got)
	}
	if !strings.Contains(got, "Hint: Run 'llm-semantic index") {
		t.Errorf("FormatWithHint() = %q, want it to carry the hint", got)
	}

	bare := &SemanticError{Type: ErrTypeNotFound, Message: "plain failure"}
	if got := bare.FormatWithHint(); got != bare.Error() {
		t.Errorf("FormatWithHint() with no hint = %q, want it identical to Error() %q", got, bare.Error())
	}
	if strings.Contains(bare.FormatWithHint(), "Hint:") {
		t.Error("a hintless error must not emit a Hint line")
	}

	hinted := (&SemanticError{Type: ErrTypeNotFound, Message: "plain failure"}).WithHint("try again")
	if !strings.Contains(hinted.FormatWithHint(), "Hint: try again") {
		t.Errorf("WithHint() did not attach the hint: %q", hinted.FormatWithHint())
	}
}
