package treesitter

import (
	"fmt"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// fallbackMaxChunkSize mirrors genericMaxChunkSize in commands/index.go, so a
// fallen-back file is windowed like any other text file.
const fallbackMaxChunkSize = 1500

// Fallback wraps a tree-sitter chunker. When the inner chunker fails (a parse
// tree with an error node, a parser error, or a panic), the file is chunked
// into text windows by semantic.GenericChunker and reported as a fallback, so
// it is neither dropped nor counted as parsed.
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

// Chunk chunks with the inner chunker, or with the generic chunker if that fails.
func (f *Fallback) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	chunks, _, err := f.ChunkWithFallback(path, content)
	return chunks, err
}

// ChunkWithFallback is Chunk, and also reports whether the generic chunker
// produced the result.
func (f *Fallback) ChunkWithFallback(path string, content []byte) ([]semantic.Chunk, bool, error) {
	if chunks, err := f.chunkInner(path, content); err == nil {
		return chunks, false, nil
	}
	chunks, err := f.generic.Chunk(path, content)
	return chunks, true, err
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
