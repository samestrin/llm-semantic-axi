package semantic

import (
	"path/filepath"
	"strings"
	"sync"
)

// Chunker is the interface for language-specific code chunking
type Chunker interface {
	// Chunk breaks source code into semantic chunks
	Chunk(path string, content []byte) ([]Chunk, error)

	// SupportedExtensions returns the file extensions this chunker handles
	SupportedExtensions() []string
}

// FallbackReason says why a chunker stood text windows in for code it could not
// chunk. The empty value means it did not fall back at all.
type FallbackReason string

const (
	// FallbackNone means the chunker chunked the file with no text windows.
	FallbackNone FallbackReason = ""
	// FallbackParseFailed means the file's parse tree had an error, so the code
	// it could not parse became text windows. The file's fault.
	FallbackParseFailed FallbackReason = "parse_failed"
	// FallbackChunkerFailed means the chunker itself errored or panicked, so the
	// whole file became text windows. Not the file's fault, and the output must
	// not blame its syntax.
	FallbackChunkerFailed FallbackReason = "chunker_failed"
)

// FallbackChunker is a Chunker that can chunk a file some other way when its
// own parse fails, and says why it did. The index manager counts those files
// and their reasons, so the output can state them rather than pass them off as
// parsed.
type FallbackChunker interface {
	Chunker
	ChunkWithFallback(path string, content []byte) (chunks []Chunk, reason FallbackReason, err error)
}

// ChunkerFactory manages language-specific chunkers
type ChunkerFactory struct {
	mu       sync.RWMutex
	chunkers map[string]Chunker
}

// NewChunkerFactory creates a new chunker factory
func NewChunkerFactory() *ChunkerFactory {
	return &ChunkerFactory{
		chunkers: make(map[string]Chunker),
	}
}

// Register adds a chunker for a specific language/extension.
// Returns true if the extension was newly registered, false if it overwrote an existing registration.
// Callers can use this to detect and handle registration conflicts.
func (f *ChunkerFactory) Register(ext string, chunker Chunker) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, exists := f.chunkers[ext]
	f.chunkers[ext] = chunker
	return !exists
}

// GetChunker returns a chunker for the specified language/extension
func (f *ChunkerFactory) GetChunker(ext string) (Chunker, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	chunker, ok := f.chunkers[ext]
	return chunker, ok
}

// GetByExtension returns the appropriate chunker based on filename extension
func (f *ChunkerFactory) GetByExtension(filename string) (Chunker, bool) {
	ext := LanguageFromExtension(filename)
	if ext == "" {
		return nil, false
	}
	return f.GetChunker(ext)
}

// SupportedExtensions returns all registered extensions
func (f *ChunkerFactory) SupportedExtensions() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	exts := make([]string, 0, len(f.chunkers))
	for ext := range f.chunkers {
		exts = append(exts, ext)
	}
	return exts
}

// LanguageFromExtension extracts the language identifier from a filename
func LanguageFromExtension(filename string) string {
	ext := filepath.Ext(filename)
	if ext == "" {
		return ""
	}
	// Remove leading dot
	return strings.TrimPrefix(ext, ".")
}
