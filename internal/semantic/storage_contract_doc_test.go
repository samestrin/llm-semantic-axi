package semantic

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docLineCitationRE matches a line-number citation such as `storage_qdrant.go:1409`
// or a bare `:1453`.
var docLineCitationRE = regexp.MustCompile("\\.go:[0-9]|`:[0-9]")

// docSymbolRE matches a backticked identifier, optionally called or followed
// by a selector: `chunkToPoint`, `Stats`, `stringToUUID(s)`,
// `ChunkType.String()`. The test keeps those with both cases, so snake_case
// payload keys and all-caps words such as `HEAD` are not taken for symbols.
var docSymbolRE = regexp.MustCompile("`([A-Za-z][A-Za-z0-9]*)[.(`]")

// docExternalSymbols are names the doc cites from outside this repository.
var docExternalSymbols = map[string]bool{
	"SetPayload": true, // the Qdrant client call
}

// TestStorageContractDoc_CitesSymbolsThatExist defends the doc's citations
// against drift. They were line numbers, and the v1.2.0 release gate found
// them pointing at the wrong code (SetFileHash cited at :1409-1439 had moved
// to ~1531). A consumer such as atcr follows those citations to check a
// promise, so a stale one is a wrong answer. The doc now cites symbols: this
// fails on any line-number citation, and on any cited symbol no longer
// declared in internal/semantic or its commands package.
func TestStorageContractDoc_CitesSymbolsThatExist(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "storage-contract.md"))
	if err != nil {
		t.Fatalf("read storage contract: %v", err)
	}
	doc := string(raw)

	for i, line := range strings.Split(doc, "\n") {
		if docLineCitationRE.MatchString(line) {
			t.Errorf("docs/storage-contract.md:%d cites a line number, which drifts; cite the symbol: %s", i+1, line)
		}
	}

	declared := declaredIdents(t, ".", filepath.Join(".", "commands"))
	cited := 0
	for _, m := range docSymbolRE.FindAllStringSubmatch(doc, -1) {
		name := m[1]
		if name == strings.ToLower(name) || name == strings.ToUpper(name) {
			continue // a word such as `type` or `HEAD`, not a symbol
		}
		cited++
		if !declared[name] && !docExternalSymbols[name] {
			t.Errorf("docs/storage-contract.md cites `%s`, which is not declared in internal/semantic", name)
		}
	}
	if cited < 20 {
		t.Errorf("found %d cited symbols; the doc cites far more, so the pattern has stopped matching", cited)
	}
}

// declaredIdents returns every function, method, type, constant, variable and
// struct field name declared in the non-test Go files of dirs.
func declaredIdents(t *testing.T, dirs ...string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch d := n.(type) {
				case *ast.FuncDecl:
					out[d.Name.Name] = true
				case *ast.TypeSpec:
					out[d.Name.Name] = true
				case *ast.ValueSpec:
					for _, id := range d.Names {
						out[id.Name] = true
					}
				case *ast.Field:
					for _, id := range d.Names {
						out[id.Name] = true
					}
				}
				return true
			})
		}
	}
	return out
}
