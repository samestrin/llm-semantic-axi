package treesitter

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// fallbackMaxChunkSize mirrors genericMaxChunkSize in commands/index.go, so a
// fallen-back file is windowed like any other text file.
const fallbackMaxChunkSize = 1500

// Fallback wraps a tree-sitter chunker. When the parse tree has an error, it
// keeps the chunks the inner chunker still found and chunks every non-blank
// run of lines they do not cover into text windows with
// semantic.GenericChunker. When the inner chunker fails any other way (a
// parser error or a panic), the whole file becomes text windows. Either way
// the file is reported as a fallback, so it is neither dropped nor counted as
// parsed.
//
// The wrapper hides any optional interface the inner chunker implements (for
// example semantic.RefExtractor); none of the tree-sitter chunkers has one.
type Fallback struct {
	inner   semantic.Chunker
	generic *semantic.GenericChunker
}

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

// ChunkWithFallback is Chunk, and also reports whether any text windows
// stand in for code the inner chunker could not parse.
func (f *Fallback) ChunkWithFallback(path string, content []byte) ([]semantic.Chunk, bool, error) {
	chunks, err := f.chunkInner(path, content)
	if err == nil {
		return chunks, false, nil
	}
	if !errors.Is(err, ErrParseTree) {
		chunks = nil
	}
	windows, err := f.windowUncovered(path, content, chunks)
	if err != nil {
		return nil, true, err
	}
	chunks = append(chunks, windows...)
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].StartLine < chunks[j].StartLine })
	return chunks, true, nil
}

// windowUncovered chunks each run of lines that no chunk covers into text
// windows, numbered by their lines in the file. Runs holding only whitespace
// are skipped: they are the gaps between definitions, not dropped code.
func (f *Fallback) windowUncovered(path string, content []byte, chunks []semantic.Chunk) ([]semantic.Chunk, error) {
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
			got, err := f.generic.Chunk(path, []byte(text))
			if err != nil {
				return nil, err
			}
			for _, w := range got {
				w.StartLine += start - 1
				w.EndLine += start - 1
				w.ID = w.GenerateID()
				windows = append(windows, w)
			}
		}
		start = end
	}
	return windows, nil
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
