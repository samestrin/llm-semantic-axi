package treesitter

import (
	"fmt"
	"sync"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// Registry collects tree-sitter chunker constructors. Each chunker file adds
// itself from init(), so adding a language never edits commands/index.go.
// Constructors run only in RegisterInto, so commands that never index do not
// load any grammar.
type Registry struct {
	mu   sync.Mutex
	news []func() semantic.Chunker
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Add records a chunker constructor.
func (r *Registry) Add(newChunker func() semantic.Chunker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.news = append(r.news, newChunker)
}

// RegisterInto builds every chunker and registers it in the factory under each
// extension it supports. It panics if an extension is claimed twice, either by
// two chunkers here or by a chunker the factory already holds: a silent
// overwrite would chunk one language's files with another's grammar.
func (r *Registry) RegisterInto(factory *semantic.ChunkerFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, newChunker := range r.news {
		c := newChunker()
		for _, ext := range c.SupportedExtensions() {
			if prev, exists := factory.GetChunker(ext); exists {
				panic(fmt.Sprintf("treesitter: extension %q claimed by %T is already registered to %T", ext, c, prev))
			}
			factory.Register(ext, c)
		}
	}
}

var defaultRegistry = NewRegistry()

// Register adds a chunker constructor to the package registry. Call it from
// init() in the chunker's own file.
func Register(newChunker func() semantic.Chunker) {
	defaultRegistry.Add(newChunker)
}

// RegisterAll registers every tree-sitter chunker in the package registry
// with the factory.
func RegisterAll(factory *semantic.ChunkerFactory) {
	defaultRegistry.RegisterInto(factory)
}
