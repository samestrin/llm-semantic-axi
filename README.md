# llm-semantic

> Semantic code search for agents. Local embeddings, three stores, one token-dense output format.

![Go](https://img.shields.io/github/go-mod/go-version/samestrin/llm-semantic-axi)
![License](https://img.shields.io/github/license/samestrin/llm-semantic-axi)

A semantic search CLI built to the [AXI](https://axi.md) principles, so its output is cheap for a model to read and hard for a model to misread. It indexes a codebase into an embedding store and answers questions like "where is the auth logic" that grep cannot, and it tracks call edges so it can answer "what calls this" without a full language server.

Two binaries:

- `llm-semantic` — the CLI. This is the product.
- `llm-semantic-mcp` — an MCP server that shells out to the CLI. A fallback for environments that cannot run a CLI, not the primary surface.

Extracted from [samestrin/llm-tools](https://github.com/samestrin/llm-tools), which carried three unrelated tools in one Go module. This binary links sqlite, qdrant and tree-sitter and is around 38MB — a cost anyone wanting the 7MB file tool used to pay as well. That is why it now lives here.

## Install

```bash
git clone https://github.com/samestrin/llm-semantic-axi.git
cd llm-semantic-axi
sudo ./install.sh
```

Or build only:

```bash
make build     # binaries land in ./build/
```

## Usage

```bash
# index a repository
llm-semantic index .

# ask a question of it
llm-semantic search "where is the retry logic" --axi

# what calls this symbol, and what does it reference
llm-semantic callers Decode
llm-semantic refs Decode

# incremental update after a commit
llm-semantic index-update .
```

Full command list: `callers`, `code-search`, `collection`, `docs-search`, `index`, `index-status`, `index-update`, `memory`, `memory-search`, `multisearch`, `refs`, `search`.

### Three stores, not one

The `--profile` flag selects which index is being addressed: `code`, `docs`, `memory` or `sprints`. They are separate collections with separate include/exclude scopes, so a docs question is not answered from source and a stored decision is not buried under a thousand functions.

### Storage backends

`--storage sqlite` (default) keeps everything in `.index/semantic.db`, pure Go, no server. `--storage qdrant` points at a Qdrant instance for larger corpora.

### Embeddings

Any OpenAI-compatible embedding endpoint works — TEI, Ollama, vLLM, OpenAI, Azure. Set `--api-url` and `--model`, or the `LLM_SEMANTIC_API_URL` / `LLM_SEMANTIC_MODEL` environment variables. `--embedder` additionally selects the Cohere, HuggingFace or OpenRouter request shapes.

## Output modes (AXI)

| Flag | Output |
|---|---|
| `--axi` | TOON — token-dense, the format to give a model |
| `--json` | JSON |
| `--min` | minimal / token-optimized |
| *(none)* | human-readable text |

TOON encoding goes through [go-axi](https://github.com/samestrin/go-axi), which guards against the failure mode the raw codec has: a type it cannot encode does not error, it emits **empty output**, and a command then silently prints nothing.

## Languages

Every extension `RegisterAllChunkers` registers, and what indexes it. A test (`TestReadmeLanguageTable_MatchesRegisteredExtensions`) fails when this table and the registered extensions disagree.

| Language | Extensions | Chunker | Call edges |
|---|---|---|---|
| Go | `.go` | Go (`go/ast`) | yes |
| C | `.c` | tree-sitter | no |
| C++ | `.cpp` `.cxx` `.cc` `.hpp` `.hxx` `.hh` `.h` | tree-sitter | no |
| C# | `.cs` | tree-sitter | no |
| Java | `.java` | tree-sitter | no |
| JavaScript | `.js` `.jsx` `.mjs` `.cjs` | tree-sitter | no |
| Kotlin | `.kt` `.kts` | tree-sitter | no |
| PHP | `.php` `.php5` `.php7` `.phtml` | tree-sitter | no |
| Python | `.py` `.pyi` `.pyw` | tree-sitter | no |
| Ruby | `.rb` | tree-sitter | no |
| Rust | `.rs` | tree-sitter | no |
| TypeScript | `.ts` `.tsx` `.mts` `.cts` | tree-sitter | no |
| Markdown | `.md` `.markdown` | markdown | no |
| HTML | `.html` `.htm` | HTML | no |
| Other text | `.adoc` `.bash` `.cfg` `.conf` `.csv` `.diff` `.fish` `.ini` `.json` `.log` `.patch` `.rst` `.sh` `.text` `.toml` `.txt` `.xml` `.yaml` `.yml` `.zsh` | generic text windows | no |

## Known limit: call edges are Go-only

`callers` and `refs` read call edges from the index, and only the Go chunker emits them (see the table above). **An empty `callers` result in a Python, JavaScript or any other non-Go file means "not indexed for edges", not "no callers."** Do not read it as proof.

## Development

```bash
make test        # go test ./...
make test-race   # race detector — CI runs this
make lint        # go vet + gofmt check
make test-cover  # coverage profile and total
make hooks       # opt in to the pre-commit hook
```

CI gates coverage at 80% and fails below it. See `AGENTS.md` for house rules.

## License

MIT
