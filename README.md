# llm-semantic

`llm-semantic` is a semantic code search CLI for coding agents and the people who run them: it indexes a codebase into local embeddings, finds code by meaning, and answers "what calls this" from a recorded call graph.

[![CI](https://github.com/samestrin/llm-semantic-axi/actions/workflows/test.yml/badge.svg)](https://github.com/samestrin/llm-semantic-axi/actions/workflows/test.yml) [![Release](https://img.shields.io/github/v/release/samestrin/llm-semantic-axi)](https://github.com/samestrin/llm-semantic-axi/releases/latest) [![License](https://img.shields.io/github/license/samestrin/llm-semantic-axi)](LICENSE)

## Demo

![A terminal session running --version, a search with --axi, and a callers query](docs/demo/llm-semantic.gif)

The session runs `--version`, a `search` with `--axi` and a `callers` query against a release binary, with nothing mocked. [docs/demo/README.md](docs/demo/README.md) has the tape and the steps to render the GIF again.

## Why

- Grep finds exact text, not intent. `llm-semantic search "where is the retry logic"` finds the code that does it, whatever it is named.
- Following a call chain by hand costs an agent many reads. `callers` and `refs` answer from call edges recorded at index time, with no language server, for Go, JavaScript, TypeScript, Python and Rust.
- Tool output is paid for in tokens. `--axi` prints TOON, a token-dense format built to the [AXI](https://axi.md) principles, with four fields per result, long content cut and a stated total.
- An empty answer is easy to misread as "none exist". Every command says which kind of empty it is: no match, an empty index, a symbol not indexed, a language with no recorded call edges, or an index that is behind the working tree.
- Code, docs and stored decisions are different questions. Profiles (`code`, `docs`, `memory`, `sprints`) keep each in its own index when each points at its own `--index-dir` (sqlite) or collection (Qdrant), so a docs question is not answered from source.

## Install

The download installer is the common path. It installs `llm-semantic` and `llm-semantic-mcp` from the latest release into `~/.local/bin`, and needs no sudo, no clone and no Go: only `sh`, `curl` or `wget`, `tar`, `awk`, and `sha256sum` or `shasum`.

```bash
curl -fsSL https://github.com/samestrin/llm-semantic-axi/releases/latest/download/install-release.sh | sh
```

It checks the archive against the release's `SHA256SUMS.txt` before writing anything; a mismatch, a missing checksum or a cut-off download stops the install and leaves the existing binaries untouched. Options, each also read from the environment variable named after it: `--version vX.Y.Z` (`LLM_SEMANTIC_VERSION`) pins a release, `--dir DIR` (`LLM_SEMANTIC_INSTALL_DIR`) changes the install directory, `--base-url URL` (`LLM_SEMANTIC_BASE_URL`) changes where the assets are fetched from, and `--timeout SECS` (`LLM_SEMANTIC_INSTALL_TIMEOUT`) limits each download. Pass options through the pipe with `sh -s --`, for example `| sh -s -- --dir /opt/bin`.

The install directory must be on your `PATH`: `llm-semantic-mcp` finds the CLI through `PATH`, so the MCP server cannot run until it is. The installer warns when it is not. Add this to your shell profile and open a new shell:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Release archive by hand

Each release publishes `llm-semantic-<os>-<arch>.tar.gz` for `linux` and `darwin` on `amd64` and `arm64`, each holding both binaries, plus `SHA256SUMS.txt`. Download one, check it, and put the binaries on your `PATH`:

```bash
curl -fsSLO https://github.com/samestrin/llm-semantic-axi/releases/latest/download/llm-semantic-linux-amd64.tar.gz
curl -fsSLO https://github.com/samestrin/llm-semantic-axi/releases/latest/download/SHA256SUMS.txt
grep ' llm-semantic-linux-amd64.tar.gz$' SHA256SUMS.txt | sha256sum -c -
tar -xzf llm-semantic-linux-amd64.tar.gz
mkdir -p ~/.local/bin && mv llm-semantic llm-semantic-mcp ~/.local/bin/
```

On macOS, use `shasum -a 256 -c -` in place of `sha256sum -c -`. Windows has raw `llm-semantic-windows-amd64.exe` and `llm-semantic-mcp-windows-amd64.exe` binaries on the release page, also listed in `SHA256SUMS.txt`; the installer does not support Windows.

### With Go

Requires Go 1.24 or newer:

```bash
go install github.com/samestrin/llm-semantic-axi/cmd/llm-semantic@latest
go install github.com/samestrin/llm-semantic-axi/cmd/llm-semantic-mcp@latest
```

The binaries land in `$(go env GOPATH)/bin`. A `go install` build is not stamped with a version, so `llm-semantic --version` prints `dev`.

### From source

```bash
git clone https://github.com/samestrin/llm-semantic-axi.git
cd llm-semantic-axi
make build          # both binaries in ./build/
sudo ./install.sh   # or: build and install both to /usr/local/bin
```

### For coding agents

Two ways to tell an agent the tool exists:

- `llm-semantic setup`, run in a project, registers the tool with Claude Code: it merges a SessionStart hook into `.claude/settings.json` (other keys are kept) and writes the Agent Skill to `.claude/skills/llm-semantic/SKILL.md`. It prints each path as `wrote`, `unchanged` or, with `--dry-run`, `would write`. It is safe to re-run, never prompts, and never touches your home directory. The hook runs `llm-semantic index-status || true`, so a session in a project with no index starts without a hook error.
- `npx skills add samestrin/llm-semantic-axi --skill llm-semantic` installs the same skill, [`skills/llm-semantic/SKILL.md`](skills/llm-semantic/SKILL.md), through the `skills` CLI for any agent it supports. It adds no hook.

### Upgrade

Re-run the installer, or `go install ...@latest`, or `git pull && make build` in a clone. Then re-run `llm-semantic setup` in each project that used it, so the skill text matches the new binary. When the changelog says a release adds call edges for a language, rebuild the index with `llm-semantic index --force`: `index-update` does not re-chunk files whose content is unchanged.

### Uninstall

```bash
rm ~/.local/bin/llm-semantic ~/.local/bin/llm-semantic-mcp   # installer or archive
rm "$(go env GOPATH)/bin/llm-semantic" "$(go env GOPATH)/bin/llm-semantic-mcp"   # go install
sudo rm /usr/local/bin/llm-semantic /usr/local/bin/llm-semantic-mcp   # install.sh
```

In each indexed project, delete `.index/` (the sqlite index). Where `setup` ran, delete `.claude/skills/llm-semantic/` and remove the `llm-semantic index-status || true` hook from `.claude/settings.json` by hand. Where `npx skills add` ran, `npx skills remove llm-semantic` removes the skill.

## Quick start

There is no bundled model, so `index` and `search` need an OpenAI-compatible embedding endpoint (TEI, Ollama, vLLM, OpenAI, Azure). Point the tool at yours, index a repository and query it:

```bash
curl -fsSL https://github.com/samestrin/llm-semantic-axi/releases/latest/download/install-release.sh | sh
export LLM_SEMANTIC_API_URL=http://localhost:8080 LLM_SEMANTIC_MODEL=nomic-embed-text   # your endpoint and its model
git clone https://github.com/samestrin/llm-semantic-axi.git && cd llm-semantic-axi
llm-semantic index .
llm-semantic search "retry embedding requests with backoff" --top 3
llm-semantic callers EnrichForEmbedding
```

Output of `index` and `callers` (trimmed, paths shortened). Which results `search` returns depends on the model your endpoint serves, so its output is not shown here.

```text
$ llm-semantic index .
Indexing .../llm-semantic-axi...
  [100/302 files] 869 chunks...
  [200/302 files] 2211 chunks...
  [300/302 files] 3435 chunks...
  [302/302 files] 3451 chunks...

Running initial calibration...
Indexed 302 files, created 3451 chunks

$ llm-semantic callers EnrichForEmbedding
5 caller(s) of EnrichForEmbedding

  .../internal/semantic/embed_enrichment_test.go:8  TestEnrichForEmbedding
  .../internal/semantic/embed_enrichment_test.go:152  TestEnrichForEmbedding_EmptyContent
  .../internal/semantic/index_manager.go:377  indexWithCrossFileBatching
  .../internal/semantic/index_manager.go:686  processFileWithDiff
  .../internal/semantic/index_manager.go:1228  prepareFile
```

## Usage

| Command | What it does |
|---|---|
| `llm-semantic` | With no arguments, shows the index status for this directory. With no index it prints `Status: not_indexed` and the next step, and exits 0. |
| `index [path]` | Builds the index for a directory. `--force` re-indexes every file. |
| `index-update [path]` | Re-indexes the files changed since a git ref (`--since`, default `HEAD~1`), or every changed file with `--no-git`. |
| `index-status` | Shows files, chunks, calibration and text-window files. Exits 1 when there is no index. |
| `search <query>` | Finds code by meaning. `code-search`, `docs-search` and `memory-search` are the same search on one profile. |
| `multisearch <q1> [q2] ...` | Runs several queries, removes duplicates and boosts results more than one query found. |
| `callers <symbol>` | Lists what calls a symbol, from recorded call edges. |
| `refs <symbol>` | Lists what a symbol calls, imports and uses. |
| `memory <store\|search\|list\|delete\|promote\|stats\|import>` | Stores and searches question and answer pairs (decisions, clarifications). |
| `collection delete` | Deletes every chunk in one profile. Requires `--force`. |
| `setup` | Registers the tool with Claude Code in this project (see Install). |

Every command accepts `--help`. A long run of options is in `llm-semantic <command> --help`.

```bash
# build, then refresh after a commit
llm-semantic index . --exclude-tests
llm-semantic index-update .

# search, narrowed to a path and a chunk type, as TOON
llm-semantic search "where is the retry logic" --path internal/ --type function --top 5 --axi

# hybrid dense and lexical search, then several phrasings at once
llm-semantic search "parse the config file" --hybrid
llm-semantic multisearch "load config" "validate config" "config defaults"

# the call graph
llm-semantic callers EnrichForEmbedding
llm-semantic refs EnrichForEmbedding

# decisions
llm-semantic memory store --question "Which store backs the index?" --answer "sqlite by default; qdrant for large corpora"
llm-semantic memory search "index storage"

# a separate index for docs
llm-semantic index docs/ --index-dir .index/docs
llm-semantic docs-search "how stale is the index" --index-dir .index/docs

# preview what setup would write
llm-semantic setup --dry-run
```

`--profile` names the index being addressed: `code` (the default), `docs`, `memory` or `sprints`. A profile is separate only when it points somewhere separate. With the default sqlite store, give each profile its own `--index-dir`. With `--storage qdrant`, a profile's collection comes from `--collection`, the config file's `<profile>_collection`, or the last part of a non-default `--index-dir`; otherwise every profile shares one collection. The config file also gives each profile its own include and exclude scope.

## Configuration

Flags win over environment variables, and environment variables over defaults. Secrets are named here, never shown.

| Name | Default | Meaning |
|---|---|---|
| `LLM_SEMANTIC_API_URL` / `--api-url` | `http://localhost:8080` | Embedding endpoint (OpenAI-compatible unless `--embedder` says otherwise). |
| `LLM_SEMANTIC_MODEL` / `--model` | `nomic-embed-text` | Embedding model the endpoint serves. |
| `LLM_SEMANTIC_API_KEY` / `--api-key` | unset, then `OPENAI_API_KEY` | Key for the default `openai` embedder. |
| `--embedder` | `openai` | Request shape: `openai`, `cohere`, `huggingface` or `openrouter`. |
| `COHERE_API_KEY`, `OPENROUTER_API_KEY` | unset | Key for `--embedder cohere` or `openrouter` when `--api-key` is not set. |
| `HUGGING_FACE_API_KEY`, `HUGGINGFACE_API_KEY`, `HF_TOKEN` | unset | Key for `--embedder huggingface`, read in that order. |
| `LLM_SEMANTIC_EMBEDDER_TIMEOUT` | `60` | Seconds one embedding request may take. |
| `LLM_SEMANTIC_RERANKER_API_URL` | unset | Reranker endpoint. When set, searches rerank unless `--no-rerank` is given. |
| `LLM_SEMANTIC_RERANKER_MODEL` | `Qwen/Qwen3-Reranker-0.6B` | Reranker model. |
| `--storage` | `sqlite` | `sqlite` keeps the index in `.index/semantic.db` with no server; `qdrant` uses a Qdrant instance. |
| `--index-dir` | `.index` | Directory of the sqlite index. |
| `QDRANT_API_URL` | unset | Qdrant URL; required with `--storage qdrant`. |
| `QDRANT_API_KEY` | unset | Qdrant key; required unless `QDRANT_INSECURE` is `true`. |
| `QDRANT_INSECURE` | unset | `true` allows Qdrant with no key, for a local or trusted network. |
| `QDRANT_COLLECTION` / `--collection` | `llm_semantic` | Qdrant collection name. |
| `--profile` | `code` | Index to address: `code`, `docs`, `memory` or `sprints`. |
| `--config` | `.planning/.config/config.yaml` when it exists | YAML file whose `semantic:` section sets `<profile>_storage`, `<profile>_collection`, `<profile>_include` and `<profile>_exclude` per profile. |
| `LLM_SEMANTIC_TIMEOUT` | `120` | Seconds `llm-semantic-mcp` lets one CLI command run. |
| `LLM_SEMANTIC_VERSION` / `--version` | latest | Installer: release to install. |
| `LLM_SEMANTIC_INSTALL_DIR` / `--dir` | `~/.local/bin` | Installer: install directory. |
| `LLM_SEMANTIC_BASE_URL` / `--base-url` | the release download URL | Installer: where the assets are fetched from. |
| `LLM_SEMANTIC_INSTALL_TIMEOUT` / `--timeout` | `600` | Installer: seconds each download may take. |

## Output and exit codes

| Flag | Output |
|---|---|
| none | Human-readable text. |
| `--json` | JSON. Keeps its pre-AXI schema (full fields, no total) so existing scripts keep working. |
| `--min` | Minimal, token-optimized form. |
| `--axi` | TOON, the token-dense AXI format to give a model: four fields per result, content cut at 200 characters with `--full` for the rest, a `total`, a `status` for empty answers and a `help` list of next steps. |

Search-family output also says how far the index is behind the working tree (`fresh`, `stale` or `unknown`). Under `--json` and `--min` that line goes to stderr so stdout keeps its schema.

| Exit code | Meaning |
|---|---|
| `0` | Success, including a definitive empty answer: no match, a symbol not indexed, or a bare run with no index. |
| `1` | The command failed: no index for `index-status`, an unreachable endpoint, or a destructive command refused without `--force` or `--yes`. In plain mode the error goes to stderr; under `--json` and `--axi` it is an `error: true` object on stdout. |
| `2` | Usage error: an unknown flag or command, or a missing argument. The message names what was wrong and lists the valid choices. |

`llm-semantic -v`, `-V` and `--version` print the bare version and exit 0, and so do the same flags on `llm-semantic-mcp`.

## Performance

- **Version:** `v1.1.0-11-gc1ec2a4` (`llm-semantic --version`)
- **Date:** 2026-11-11
- **Machine:** Intel(R) Xeon(R) W-2295 CPU @ 3.00GHz, 36 threads, 141.2 GiB RAM, Debian GNU/Linux 13 (trixie) (linux/amd64), kernel 7.0.0-38-generic
- **Method:** each case runs its warmup runs untimed, then its timed runs: warmup 3 (cold start), 3 (typical command), 1 (large input); runs 20 (cold start), 20 (typical command), 5 (large input). Each timing is the wall clock of one `llm-semantic` process, from start to exit, on the release build. Every `index` and `search` run also gets `--api-url`, `--model`, `--storage sqlite` and `--index-dir` pointing at the fake embedder and a temporary index; each `index` run starts from an empty index.
- **Embeddings:** a fixed-latency local fake: a loopback OpenAI-compatible server that waits 1.0 ms per request and answers with deterministic 256-dimension hash vectors, so the numbers measure the tool and not a model.
- **Large input:** 3,000 files, a corpus the program generates (Go, Python and Markdown files).

| Case | Command | Warmup | Runs | Min (ms) | Median (ms) | Mean (ms) | Max (ms) |
|---|---|---:|---:|---:|---:|---:|---:|
| cold start | `llm-semantic --version` | 3 | 20 | 3.8 | 4.0 | 4.1 | 4.9 |
| typical command | `llm-semantic search "load the config file and validate it" --json` | 3 | 20 | 85.8 | 88.6 | 88.8 | 93.2 |
| large input | `llm-semantic index <corpus> --json` | 1 | 5 | 32430.7 | 33200.5 | 33496.6 | 35090.8 |

Every timing is in [benchmarks/results-performance.json](benchmarks/results-performance.json). Measured by [`benchmarks/cmd/perf`](benchmarks/cmd/perf/main.go).

## How it works

`index` splits each file into chunks (functions, methods and types through tree-sitter, or `go/ast` for Go; sections for Markdown; text windows for other text), embeds each chunk through the endpoint and stores the vectors with the call edges it found. `search` embeds the query and ranks chunks by similarity, optionally fused with a lexical index (`--hybrid`) and reranked. `callers` and `refs` read the stored edges. With the default SQLite storage they never call the endpoint; with Qdrant they call it once, to learn the embedding dimension before opening the collection.

`llm-semantic-mcp` is an MCP server that runs the CLI with `--axi` for each tool call. It is a fallback for environments that cannot run a CLI; the CLI is the product. TOON encoding goes through [go-axi](https://github.com/samestrin/go-axi), which guards against the raw codec's failure mode: a type it cannot encode does not error, it emits empty output, and a command then silently prints nothing.

The tool was extracted from [samestrin/llm-tools](https://github.com/samestrin/llm-tools), which carried three unrelated tools in one Go module. This binary links sqlite, Qdrant and tree-sitter and is around 38 MB, a cost anyone wanting the small file tool used to pay as well, so it lives here on its own.

## Limits

- **`callers` and `refs` record call edges for Go, JavaScript, TypeScript, Python and Rust only.** In any other language, an empty answer means "not indexed for edges", not "no callers"; the output says so. Even in those five languages some call shapes are not recorded, so an empty answer is not proof. [docs/languages.md](docs/languages.md) lists what each language records.
- An index built before a language gained call edges has none for it until `llm-semantic index --force`.
- There is no bundled embedding model. `index` and `search` fail without a reachable endpoint, and the quality of `search` is the quality of the model you point it at.
- TOON is opt-in through `--axi`; the default output is plain text.
- A usage error exits 2, and plain-mode errors go to stderr, not stdout.
- `--json` keeps its pre-AXI schema: no truncation, no `total`, no zero-result message. Those are in `--axi` and plain output only.
- `setup` targets Claude Code only and registers no session-end hook. Other agents get the skill through `npx skills add`, without a hook.
- The installer supports Linux and macOS on `amd64` and `arm64`. Windows has raw binaries only.
- The sqlite file (`.index/semantic.db`) is an internal detail. Only the Qdrant store has a published contract.

## Documentation

- [docs/languages.md](docs/languages.md): every indexed extension, its chunker, and the call-edge limits behind `callers` and `refs`.
- [docs/staleness.md](docs/staleness.md): how the search family reports an index that is behind the working tree, in each output mode.
- [docs/axi-conformance.md](docs/axi-conformance.md): the ten axi.md principles, each met or stated as an exception with its reason.
- [docs/storage-contract.md](docs/storage-contract.md): the published Qdrant storage contract (`contract_version: 1`) for tools that read the index directly, such as [atcr](https://github.com/samestrin/atcr).
- [docs/axi-audit.md](docs/axi-audit.md): history, the 2026-11-01 audit snapshot that [docs/axi-conformance.md](docs/axi-conformance.md) replaced.
- [skills/llm-semantic/SKILL.md](skills/llm-semantic/SKILL.md): the Agent Skill that `setup` and `npx skills add` install.
- [benchmarks/](benchmarks/): the performance program, its raw JSON and the committed table it generates.
- [docs/demo/](docs/demo/): the VHS tape, the rendered GIF and the steps to render it again.
- [docs/release-checklist.md](docs/release-checklist.md): what to re-run and re-render at each release.

## Development

```bash
make build       # both binaries into ./build/; the MCP end-to-end tests need them
make test        # go test ./...
make test-race   # race detector, as CI runs it
make lint        # go vet and the gofmt check
make test-cover  # coverage profile and total
make hooks       # opt in to the pre-commit hook
```

CI fails below 80% coverage. [AGENTS.md](AGENTS.md) has the house rules for changes.

Support is best effort. Issues and pull requests are read and answered as time allows, with no response-time or fix-time guarantee. To get an answer sooner, include a reproduction against a tagged release.

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## License

MIT, see [LICENSE](LICENSE).
