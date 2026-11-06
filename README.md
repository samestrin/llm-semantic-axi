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

The download installer fetches the latest release and installs both binaries into `~/.local/bin`. It needs no sudo, no clone and no Go — only `sh`, `curl` or `wget`, `tar` and `sha256sum` or `shasum`:

```bash
curl -fsSL https://github.com/samestrin/llm-semantic-axi/releases/latest/download/install-release.sh | sh
```

The archive is verified against the release's `SHA256SUMS.txt` before anything is written; a mismatch or a cut-off download stops the install with the existing binaries untouched. Options (each also readable from the environment variable named after it): `--version vX.Y.Z` (`LLM_SEMANTIC_VERSION`) to pin a release, `--dir DIR` (`LLM_SEMANTIC_INSTALL_DIR`) to change the install directory, `--base-url URL` (`LLM_SEMANTIC_BASE_URL`) and `--timeout SECS` (`LLM_SEMANTIC_INSTALL_TIMEOUT`).

**The install directory must be on your `PATH`.** `llm-semantic-mcp` finds the CLI through `PATH`, so the MCP server cannot run until it is. If `~/.local/bin` is not already on it, add this to your shell profile and open a new shell (the installer prints the same warning when it detects this):

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Building from source

Requires Go. This is the alternative when you want to hack on the tool rather than just run it:

```bash
git clone https://github.com/samestrin/llm-semantic-axi.git
cd llm-semantic-axi
sudo ./install.sh   # builds and installs to /usr/local/bin
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

Two things to set up before the first `index`: a storage backend and an embedding endpoint.

**Storage** defaults to sqlite and needs no setup: `--storage sqlite` keeps everything in `.index/semantic.db` inside the indexed directory, pure Go, no server. For larger corpora, `--storage qdrant` points at a Qdrant instance instead; configure it with the `QDRANT_API_URL` and `QDRANT_API_KEY` environment variables, and name the collection with `--collection` (default: `QDRANT_COLLECTION` env, or `llm_semantic`).

**Embeddings**: any OpenAI-compatible embedding endpoint works — TEI, Ollama, vLLM, OpenAI, Azure. There is no bundled model, so an endpoint must be reachable before indexing. Set `--api-url` and `--model` (and `--api-key` if the endpoint needs one), or the `LLM_SEMANTIC_API_URL`, `LLM_SEMANTIC_MODEL` and `LLM_SEMANTIC_API_KEY` environment variables:

```bash
export LLM_SEMANTIC_API_URL=http://localhost:8080   # your TEI / Ollama / vLLM endpoint
export LLM_SEMANTIC_MODEL=bge-small-en-v1.5         # the model that endpoint serves
llm-semantic index .
```

`--embedder` additionally selects the Cohere, HuggingFace or OpenRouter request shapes when the endpoint is not OpenAI-shaped.

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
| 5 | Definitive empty states | Met | An empty search says it found nothing in plain and `--axi` (`message`), and a search over an index with no chunks says the index is empty instead (`status: index_empty` vs `no_match` in `--axi`). `callers`/`refs` distinguish `symbol not indexed`, `not indexed for edges` and a genuine empty answer (see "Known limit" above). |
| 6 | Structured errors on stdout, exit 0/1, idempotent, never prompt | **Exception** | A malformed invocation exits 2 and plain-mode errors go to stderr — a deliberate go-axi convention, kept so a usage mistake reads differently from a command failure. No command prompts: `collection delete` without `--force` and `memory stats --prune` without `--yes` refuse with an error naming the flag and exit 1, deleting nothing. Everything else prints structured errors on stdout (`error: true` in `--json`, TOON in `--axi`) and exits 0/1. |
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
| JavaScript | `.js` `.jsx` `.mjs` `.cjs` | tree-sitter | yes |
| Kotlin | `.kt` `.kts` | tree-sitter | no |
| PHP | `.php` `.php5` `.php7` `.phtml` | tree-sitter | no |
| Python | `.py` `.pyi` `.pyw` | tree-sitter | yes |
| Ruby | `.rb` | tree-sitter | no |
| Rust | `.rs` | tree-sitter | yes |
| TypeScript | `.ts` `.tsx` `.mts` `.cts` | tree-sitter | yes |
| Markdown | `.md` `.markdown` | markdown | no |
| HTML | `.html` `.htm` | HTML | no |
| Other text | `.adoc` `.bash` `.cfg` `.conf` `.csv` `.diff` `.fish` `.ini` `.json` `.log` `.patch` `.rst` `.sh` `.text` `.toml` `.txt` `.xml` `.yaml` `.yml` `.zsh` | generic text windows | no |

## Known limit: call edges exist for Go, JavaScript, TypeScript, Python and Rust only

`callers` and `refs` read call edges from the index, and only the Go, JavaScript, TypeScript, Python and Rust chunkers emit them (see the table above). **An empty `callers` result in a Java, C# or any other file whose row says `no` means "not indexed for edges", not "no callers."** Do not read it as proof.

JavaScript and TypeScript record a call only when the callee is a bare name (`foo()`) or has a `this.` receiver (`this.foo()`). `obj.foo()`, `a.b.foo()` and `super.foo()` are not recorded, so an empty `callers` for a method that is only called through another object is still not proof. A `this.foo()` call links only to a `foo` method of the caller's own class, in the same file; when that class has none, or the call is not inside a class, `refs` lists it as unresolved rather than linking it to some other `foo`.

Python records a call when the callee is a bare name (`foo()`) or has a `self.` or `cls.` receiver, and records an `imports` edge for each imported module or name. `obj.foo()` and `a.b.foo()` are not recorded and there are no type-use edges, so an empty `callers` for a method that is only called through another object is not proof there. A call is attributed to the innermost function or method that contains it, never to the enclosing class.

Rust records a call when the callee is a bare name (`helper()`, `helper::<T>()`) or has a `self.` receiver and the caller's own `impl` or trait defines a method of that name, and records an `imports` edge for each `use`, named by the path's last segment with aliases ignored and nothing for a wildcard. `obj.f()`, path calls (`Type::f()`, `Self::f()`) and macros are not recorded, and there are no type-use edges. A call is attributed to the innermost function or method that contains it, as in Python. A call inside an inline `mod` body or in a `const` or `static` initialiser has no function to belong to and records no edge, and neither does `self.f()` when `f` is defined in a second `impl` block rather than the caller's own; nothing tracks that last miss. So an empty `callers` for a method reached only through those forms is not proof.

An `imports` edge, in any language, names a module or package, and a module has no chunk, so `refs` always lists it as unresolved. `import debounce from "lodash/debounce"` is not linked to a local `function debounce`, and Go's `import "net/http"` is not linked to a `func http`.

**An index built before this change has no JavaScript, TypeScript, Python or Rust edges. Run `llm-semantic index --force` to rebuild it;** `index-update` does not re-chunk files whose hash is unchanged.

The commands say which case an empty answer is, in plain and `--axi` output (`--axi` adds a `status` field): `symbol not indexed` when no chunk carries the name, `not indexed for edges` when the symbol is indexed only in languages the index holds no edges for, and `recorded in the call graph` only when an empty answer is genuine. `--json` keeps its old shape and does not carry this.

## How stale the index is

The search family (`search`, its profile aliases, `multisearch`, `callers`, `refs`) reports how far the index is behind the working tree, so a search over old code does not read like a search over current code.

- `--axi` carries a `stale` object. `state` is `fresh`, `stale` or `unknown`; a stale or fresh answer that was measured adds `commits_behind`, `files_changed`, `indexed_commit` and `indexed_at`, and an `unknown` one adds a `reason` (for example `no_record` for an index built before the record existed). Counts appear only when measured, so nothing reads as "0 behind" that was never checked. `memory search` reports `state: not_applicable` — memories are stored, not indexed from the working tree.
- Plain output ends with a line such as `Index is stale: 3 commits behind HEAD, 1 uncommitted file changed (indexed at 1a2b3c4d5e6f on 2026-11-02T19:37:48Z). Run llm-semantic index <path> to bring it up to date.` — or `Index staleness unknown: <reason>...` when it cannot be measured.
- `--json` and `--min` keep their stdout schema unchanged (board decision, 2026-10-31); the same line goes to **stderr** instead, so the JSON stays parseable.
- A fresh index prints no line. Only `--axi` says `fresh`, because that is the output an agent branches on.
- The record is written only by an `index` or `index-update` in which every file indexed. A run where any file failed leaves the previous record in place, so the index keeps reporting how far behind it was.

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

## Support policy

Support is best effort. This is a personal open-source project: issues and pull requests are read and answered as time allows, with no response-time or fix-time guarantee. If you need an answer sooner, include a reproduction against a tagged release.

## License

MIT
