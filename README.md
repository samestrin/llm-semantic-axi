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
# (in an unindexed directory it states not_indexed and exits 0; `index-status`
# is the one that errors with exit 1 there, so a missing index is never silent)
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

`llm-semantic setup` registers the tool with Claude Code for the current project: it merges a SessionStart hook into `.claude/settings.json` (other keys kept) and writes an Agent Skill to `.claude/skills/llm-semantic/SKILL.md`. It prints each path as `wrote`, `unchanged` or (with `--dry-run`) `would write` — and the same statuses as `would_write` / `unchanged` / `wrote` under `--json` and `--axi`. It is safe to re-run, and never prompts or touches your home directory. The SessionStart hook it registers runs `llm-semantic index-status || true`, tolerating the non-zero exit a missing index raises, so a session in an un-indexed project starts without a hook error.

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

`llm-semantic` is built to the ten principles on [axi.md](https://axi.md). Seven are met; three are stated exceptions: TOON is opt-in through `--axi` (row 1), a malformed invocation exits 2 and plain-mode errors go to stderr (row 6), and `setup` targets Claude Code only (row 7). [docs/axi-conformance.md](docs/axi-conformance.md) is the full table with the note on each row, including the standing `--json` exception for principles 2-5.

## Known limit: call edges exist for Go, JavaScript, TypeScript, Python and Rust only

`callers` and `refs` read call edges from the index, and only the Go, JavaScript, TypeScript, Python and Rust chunkers emit them. **An empty `callers` result in a Java, C# or any other language with no recorded edges means "not indexed for edges", not "no callers."** Do not read it as proof.

[docs/languages.md](docs/languages.md) lists every indexed extension with its chunker and its call-edge column, and the per-language limits: which JavaScript, TypeScript, Python and Rust calls are recorded and which are not, and why an `imports` edge is never a resolved link. **An index built before those edges existed has none of them. Run `llm-semantic index --force` to rebuild it;** `index-update` does not re-chunk files whose hash is unchanged.

The commands say which case an empty answer is, in plain and `--axi` output (`--axi` adds a `status` field): `symbol not indexed` when no chunk carries the name, `not indexed for edges` when the symbol is indexed only in languages the index holds no edges for, and `recorded in the call graph` only when an empty answer is genuine. `--json` keeps its old shape and does not carry this.

## How stale the index is

The search family (`search`, its profile aliases, `multisearch`, `callers`, `refs`) reports how far the index is behind the working tree, so a search over old code does not read like a search over current code. `--axi` carries a `stale` object (`fresh`, `stale` or `unknown`, with counts only when they were measured); plain output ends with an `Index is stale: ...` or `Index staleness unknown: ...` line, and `--json` / `--min` print that line on stderr so their stdout schema is unchanged. A fresh index prints no line. [docs/staleness.md](docs/staleness.md) is the full contract, including when the record behind it is written.

## For integrators

Tools that read `llm-semantic`'s data directly (such as [atcr](https://github.com/samestrin/atcr)) should build against the published storage contract: [docs/storage-contract.md](docs/storage-contract.md). It is `contract_version: 1` and covers the Qdrant store only — the five point kinds (chunks, `file_hash` bookkeeping, calibration metadata, memory entries, retrieval logs), their payload keys and value shapes, and the filter rule that chunks are exactly the points carrying a `chunk_id` payload key. The default sqlite file (`.index/semantic.db`) is an internal implementation detail and is not part of the contract.

## Documentation

- [docs/languages.md](docs/languages.md) — every indexed extension, its chunker, and the call-edge limits behind `callers` and `refs`.
- [docs/staleness.md](docs/staleness.md) — how the search family reports an index that is behind the working tree, in each output mode.
- [docs/axi-conformance.md](docs/axi-conformance.md) — the ten axi.md principles, each met or stated as an exception with its reason.
- [docs/storage-contract.md](docs/storage-contract.md) — the published Qdrant storage contract (`contract_version: 1`) for tools that read the index directly.
- [docs/axi-audit.md](docs/axi-audit.md) — history: the 2026-11-01 audit snapshot that [docs/axi-conformance.md](docs/axi-conformance.md) replaced.

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
