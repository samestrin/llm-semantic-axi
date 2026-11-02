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
# no arguments: content-first — prints the index status for this directory
# (a missing index is an error with a non-zero exit, exactly like index-status)
llm-semantic

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

`llm-semantic setup` registers the tool with Claude Code for the current project: it merges a SessionStart hook into `.claude/settings.json` (other keys kept) and writes an Agent Skill to `.claude/skills/llm-semantic/SKILL.md`. It prints each path as `wrote`, `unchanged` or (with `--dry-run`) `would write`, is safe to re-run, and never prompts or touches your home directory.

Full command list: `callers`, `code-search`, `collection`, `docs-search`, `index`, `index-status`, `index-update`, `memory`, `memory-search`, `multisearch`, `refs`, `search`, `setup`.

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

## AXI conformance

Where `llm-semantic` stands against the ten principles on [axi.md](https://axi.md). This table is the current record; `docs/axi-audit.md` is the snapshot it replaced.

| # | Principle | Status | Notes |
|---|---|---|---|
| 1 | Token-efficient output (TOON) | Met | `--axi` is a persistent flag on every command; the MCP server runs the CLI with `--axi` too. |
| 2 | Minimal default schemas (3-4 fields per item) | Met | `--axi` search rows carry four fields (`file` as `path:line`, `name`, `score`, `content`); `memory search` carries `id`, `question`, `score`, `answer`. `--full` opts back into every field. |
| 3 | Content truncation with a size hint and `--full` | Met | `--axi` cuts content over 200 characters and says how many were left out. `--full` returns the whole text. |
| 4 | Pre-computed aggregates, always a total | Met | The search family states its total (`N results for "query"` plain, `total` in `--axi`); `multisearch` reports `total_queries` and `total_results`. |
| 5 | Definitive empty states | Met | An empty search says it found nothing in plain and `--axi` (`message`). `callers`/`refs` distinguish `symbol not indexed`, `not indexed for edges` and a genuine empty answer (see "Known limit" above). |
| 6 | Structured errors on stdout, exit 0/1, idempotent, never prompt | **Exception** (two) | a) A malformed invocation exits 2 and plain-mode errors go to stderr — a deliberate go-axi convention, kept so a usage mistake reads differently from a command failure. b) `collection delete` without `--force` and `memory stats --prune` without `--yes` still prompt, and a declined prompt exits 0. That is a known gap, tracked as technical debt. Everything else prints structured errors on stdout (`error: true` in `--json`, TOON in `--axi`) and exits 0/1. |
| 7 | Ambient context (`setup` command, Agent Skill) | Met | `llm-semantic setup` writes a SessionStart hook and an Agent Skill into the project's `.claude/` (see Usage). |
| 8 | Content first (no-argument run shows live data) | Met | A bare `llm-semantic` prints a description, the executable path (home as `~`) and the index status for the current directory. |
| 9 | Contextual disclosure (`help[]` next steps) | Met | `--axi` output of the search family, `memory search`, `multisearch` and `index-status` carries a `help` list of suggested next steps, as does the missing-index error. |
| 10 | Consistent `--help` on every subcommand | Met | `--help` exits 0 on all subcommands. |

Scope note for rows 2-5: they are met in `--axi` (and plain, where the note says so). `--json` keeps its pre-05.1 schema by board decision (2026-10-31) — full fields, untruncated content, and a bare result array without `total` or a zero-result message — so the machine-readable JSON mode does not meet principles 2-5 and is a standing exception with that reason.

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

The commands say which case an empty answer is, in plain and `--axi` output (`--axi` adds a `status` field): `symbol not indexed` when no chunk carries the name, `not indexed for edges` when the symbol is indexed only in languages the index holds no edges for, and `recorded in the call graph` only when an empty answer is genuine. `--json` keeps its old shape and does not carry this.

## For integrators

Tools that read `llm-semantic`'s data directly (such as [atcr](https://github.com/samestrin/atcr)) should build against the published storage contract: [docs/storage-contract.md](docs/storage-contract.md). It is `contract_version: 1` and covers the Qdrant store only — the five point kinds (chunks, `file_hash` bookkeeping, calibration metadata, memory entries, retrieval logs), their payload keys and value shapes, and the filter rule that chunks are exactly the points carrying a `chunk_id` payload key. The default sqlite file (`.index/semantic.db`) is an internal implementation detail and is not part of the contract.

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
