package treesitter

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// fallbackMaxChunkSize is the size a fallen-back file's text windows use: the
// same number the index chunks plain text files at, shared rather than copied so
// the two cannot drift apart.
const fallbackMaxChunkSize = semantic.GenericMaxChunkSize

// Fallback wraps a tree-sitter chunker. When the parse tree has an error, it
// keeps the chunks the inner chunker still found and chunks every non-blank
// run of lines they do not cover into text windows with
// semantic.GenericChunker. When the inner chunker fails any other way (a
// parser error or a panic), the whole file becomes text windows. Either way
// the file is reported as a fallback, so it is neither dropped nor counted as
// parsed.
//
// The wrapper forwards semantic.RefExtractor to the inner chunker, and hides
// any other optional interface the inner chunker implements.
type Fallback struct {
	inner   semantic.Chunker
	generic *semantic.GenericChunker
}

var _ semantic.RefExtractor = (*Fallback)(nil)

// WithFallback wraps a chunker in the parse-error fallback.
func WithFallback(inner semantic.Chunker) *Fallback {
	return &Fallback{
		inner:   inner,
		generic: semantic.NewGenericChunker(fallbackMaxChunkSize),
	}
}

// Unwrap returns the wrapped chunker.
func (f *Fallback) Unwrap() semantic.Chunker { return f.inner }

// SupportedExtensions returns the inner chunker's extensions.
func (f *Fallback) SupportedExtensions() []string { return f.inner.SupportedExtensions() }

// Chunk chunks with the inner chunker, filling what it could not parse with
// text windows.
func (f *Fallback) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	chunks, _, err := f.ChunkWithFallback(path, content)
	return chunks, err
}

// ChunkWithFallback is Chunk, and also says why text windows stand in for code,
// if they do.
func (f *Fallback) ChunkWithFallback(path string, content []byte) ([]semantic.Chunk, semantic.FallbackReason, error) {
	chunks, err := f.chunkInner(path, content)
	if err == nil {
		return chunks, semantic.FallbackNone, nil
	}
	reason := semantic.FallbackChunkerFailed
	if errors.Is(err, ErrParseTree) {
		reason = semantic.FallbackParseFailed
	} else {
		chunks = nil
	}
	chunks = append(chunks, f.windowUncovered(path, content, chunks)...)
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].StartLine < chunks[j].StartLine })
	return chunks, reason, nil
}

// windowUncovered chunks each run of lines that no chunk covers into text
// windows, numbered by their lines in the file. Runs holding only whitespace
// are skipped: they are the gaps between definitions, not dropped code.
//
// It returns no error because the generic chunker cannot fail: it splits text
// by size and never rejects it. The error returns this used to have were
// unreachable.
func (f *Fallback) windowUncovered(path string, content []byte, chunks []semantic.Chunk) []semantic.Chunk {
	lines := strings.Split(string(content), "\n")
	covered := make([]bool, len(lines)+1) // 1-based
	for _, c := range chunks {
		for l := max(c.StartLine, 1); l <= c.EndLine && l <= len(lines); l++ {
			covered[l] = true
		}
	}

	var windows []semantic.Chunk
	for start := 1; start <= len(lines); start++ {
		if covered[start] {
			continue
		}
		end := start
		for end < len(lines) && !covered[end+1] {
			end++
		}
		text := strings.Join(lines[start-1:end], "\n")
		if strings.TrimSpace(text) != "" {
			got, _ := f.generic.Chunk(path, []byte(text))
			for _, w := range got {
				w.StartLine += start - 1
				w.EndLine += start - 1
				w.ID = w.GenerateID()
				windows = append(windows, w)
			}
		}
		start = end
	}
	return windows
}

// ExtractRefs returns the inner chunker's references, or none if it extracts
// none. A panic becomes an error, as it does in Chunk.
func (f *Fallback) ExtractRefs(path string, content []byte, chunks []semantic.Chunk) (refs []semantic.ChunkRef, err error) {
	re, ok := f.inner.(semantic.RefExtractor)
	if !ok {
		return nil, nil
	}
	defer func() {
		if r := recover(); r != nil {
			refs, err = nil, fmt.Errorf("ref extraction panicked: %v", r)
		}
	}()
	return re.ExtractRefs(path, content, chunks)
}

// chunkInner runs the inner chunker, turning a panic into an error so one
// malformed file cannot end the whole index run.
func (f *Fallback) chunkInner(path string, content []byte) (chunks []semantic.Chunk, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("chunker panicked: %v", r)
		}
	}()
	return f.inner.Chunk(path, content)
}
