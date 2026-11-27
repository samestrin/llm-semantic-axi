# llm-semantic-axi

`llm-semantic` is a single Go binary that gives an AI agent, or you, commands to search code by meaning and answer "what calls this" from a recorded call graph, with [AXI](https://axi.md)-compliant output built for agents to parse.

[![CI](https://github.com/samestrin/llm-semantic-axi/actions/workflows/test.yml/badge.svg)](https://github.com/samestrin/llm-semantic-axi/actions/workflows/test.yml) [![Release](https://img.shields.io/github/v/release/samestrin/llm-semantic-axi)](https://github.com/samestrin/llm-semantic-axi/releases/latest) [![License](https://img.shields.io/github/license/samestrin/llm-semantic-axi)](LICENSE)

## Demo

![A terminal session running --version, a search and a callers query](docs/demo/llm-semantic.gif)

The session runs `--version`, a `search` and a `callers` query against a release binary, with nothing mocked. The GIF is still the v1.2.0 render: it types `--axi`, which v2 does not need because TOON is the default, and its `callers` rows predate v2.0.0's default row shape. [docs/demo/README.md](docs/demo/README.md) has the tape and the steps to render the GIF again.

## Why

- Grep finds exact text, not intent. `llm-semantic search "where is the retry logic"` finds the code that does it, whatever it is named.
- Following a call chain by hand costs an agent many reads. `callers` and `refs` answer from call edges recorded at index time, with no language server, for Go, JavaScript, TypeScript, Python and Rust.
- Tool output is paid for in tokens. The default output is TOON, a token-dense format built to the [AXI](https://axi.md) principles, with at most four fields per result, long content cut and a stated total. `--plain` prints text for a person instead.
- An empty answer is easy to misread as "none exist". Every command says which kind of empty it is: no match, an empty index, a symbol not indexed, a language with no recorded call edges, or an index that is behind the working tree.
- Code, docs and stored decisions are different questions. Profiles (`code`, `docs`, `memory`, `sprints`) keep each in its own index — `.index` for `code`, `.index/<profile>` for the rest with the default sqlite store, and a matching collection with Qdrant — so a docs question is not answered from source.

## Install

The download installer is the common path. It installs `llm-semantic` and `llm-semantic-mcp` from the latest release into `~/.local/bin`, and needs no sudo, no clone and no Go: only `sh`, `curl` or `wget`, `tar`, `awk`, and `sha256sum` or `shasum`.

```bash
curl -fsSL https://github.com/samestrin/llm-semantic-axi/releases/latest/download/install-release.sh | sh
```

It checks the archive against the release's `SHA256SUMS.txt` before writing anything; a mismatch, a missing checksum or a cut-off download stops the install and leaves the existing binaries untouched. Options, each also read from the environment variable named after it: `--version vX.Y.Z` (`LLM_SEMANTIC_VERSION`) pins a release, `--dir DIR` (`LLM_SEMANTIC_INSTALL_DIR`) changes the install directory, `--base-url URL` (`LLM_SEMANTIC_BASE_URL`) changes where the assets are fetched from, and `--timeout SECS` (`LLM_SEMANTIC_INSTALL_TIMEOUT`) limits each download. Pass options through the pipe with `sh -s --`, for example `| sh -s -- --dir /opt/bin`.

The install directory must be on your `PATH`: `llm-semantic-mcp` finds the CLI through `PATH`, falling back only to `/usr/local/bin/llm-semantic`, so the MCP server cannot run from any other install directory until it is on `PATH`. The installer warns when it is not. Add this to your shell profile and open a new shell:

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
go install github.com/samestrin/llm-semantic-axi/v2/cmd/llm-semantic@latest
go install github.com/samestrin/llm-semantic-axi/v2/cmd/llm-semantic-mcp@latest
```

The binaries land in `$(go env GOPATH)/bin`. A `go install` build is not stamped with a version, so `llm-semantic --version` prints `dev`.

### From source

```bash
git clone https://github.com/samestrin/llm-semantic-axi.git
cd llm-semantic-axi
make build          # both binaries in ./build/
sudo ./install.sh   # or: build and install both to /usr/local/bin (INSTALL_DIR=<dir> ./install.sh for a directory you can write)
```

### For coding agents

Two ways to tell an agent the tool exists:

- `llm-semantic setup`, run in a project, registers the tool with three agents, each at session start. Claude Code: a SessionStart hook merged into `.claude/settings.json` (other keys are kept) and the Agent Skill at `.claude/skills/llm-semantic/SKILL.md`. Codex: a SessionStart hook merged into `.codex/hooks.json`, and `[features] hooks = true` merged into `.codex/config.toml` (other keys are kept). OpenCode: a plugin at `.opencode/plugins/llm-semantic.ts` that runs on `session.created`. That plugin file is written as fixed content, so local edits to it are overwritten on the next `setup` run (the Claude Code and Codex settings files are merged, keeping their other keys), and it runs the hook through `sh -c`, so the OpenCode plugin needs `sh` on `PATH`. `--target claude`, `codex` or `opencode` (repeatable) registers only those. Each hook runs `llm-semantic index-status || true`, so a session starts with the index status and a project with no index starts without a hook error; when `llm-semantic` is not on `PATH` the hook names this binary's absolute path, and re-running `setup` after moving the binary repairs it. `setup` prints each path as `wrote`, `unchanged` or, with `--dry-run`, `would_write`. It is safe to re-run, never prompts, and writes only under the current directory: it refuses a path that is a symlink out of the directory, a settings file it cannot fully parse (it writes nothing then), and to run in your home directory, where the files would be your global agent configuration.
- `npx skills add samestrin/llm-semantic-axi --skill llm-semantic` installs the same skill, [`skills/llm-semantic/SKILL.md`](skills/llm-semantic/SKILL.md), through the `skills` CLI for any agent it supports. It adds no hook.

### Upgrade

Re-run the installer, or the two `go install .../v2/cmd/...@latest` lines above (the old path without `/v2` stops at v1.2.0), or `git pull && make build` in a clone. Then re-run `llm-semantic setup` in each project that used it, so the skill text matches the new binary. When the changelog says a release adds call edges for a language, rebuild the index with `llm-semantic index --force`: `index-update` does not re-chunk files whose content is unchanged.

### Uninstall

```bash
rm ~/.local/bin/llm-semantic ~/.local/bin/llm-semantic-mcp   # installer or archive
rm "$(go env GOPATH)/bin/llm-semantic" "$(go env GOPATH)/bin/llm-semantic-mcp"   # go install
sudo rm /usr/local/bin/llm-semantic /usr/local/bin/llm-semantic-mcp   # install.sh
```

In each indexed project, delete `.index/` (the sqlite index). Where `setup` ran, delete `.claude/skills/llm-semantic/` and `.opencode/plugins/llm-semantic.ts`, and remove the `llm-semantic index-status || true` hook from `.claude/settings.json` and `.codex/hooks.json` by hand. `setup` also set `[features] hooks = true` in `.codex/config.toml`; leave it if other Codex hooks need it. Where `npx skills add` ran, `npx skills remove llm-semantic` removes the skill.

## Quick start

There is no bundled model, so `index` and `search` need an OpenAI-compatible embedding endpoint (TEI, Ollama, vLLM, OpenAI, Azure). Point the tool at yours, index a repository and query it:

```bash
curl -fsSL https://github.com/samestrin/llm-semantic-axi/releases/latest/download/install-release.sh | sh
export LLM_SEMANTIC_API_URL=http://localhost:8080 LLM_SEMANTIC_MODEL=nomic-embed-text   # your endpoint and its model
git clone https://github.com/samestrin/llm-semantic-axi.git && cd llm-semantic-axi
llm-semantic index . --plain
llm-semantic search "retry embedding requests with backoff" --top 3
llm-semantic callers EnrichForEmbedding --plain
```

Output of `index` and `callers` as text (trimmed, paths shortened); without `--plain` both print TOON. Which results `search` returns depends on the model your endpoint serves, so its output is not shown here.

```text
$ llm-semantic index . --plain
Indexing .../llm-semantic-axi...
  [100/336 files] 972 chunks...
  [200/336 files] 2103 chunks...
  [300/336 files] 3426 chunks...
  [336/336 files] 3822 chunks...

Running initial calibration...
Indexed 336 files, created 3822 chunks

$ llm-semantic callers EnrichForEmbedding --plain
5 caller(s) of EnrichForEmbedding

  .../internal/semantic/embed_enrichment_test.go:8  TestEnrichForEmbedding
  .../internal/semantic/embed_enrichment_test.go:152  TestEnrichForEmbedding_EmptyContent
  .../internal/semantic/index_manager.go:393  indexWithCrossFileBatching
  .../internal/semantic/index_manager.go:712  processFileWithDiff
  .../internal/semantic/index_manager.go:1318  prepareFile
```

## Usage

| Command | What it does |
|---|---|
| `llm-semantic` | With no arguments, shows the index status for this directory. With no index it prints `status: not_indexed` and the next step, and exits 0. |
| `index [path]` | Builds the index for a directory. `--force` re-indexes every file. Exits 1 if any file failed, listing what was indexed and what failed. See [What `index` reads](#what-index-reads). |
| `index-update [path]` | Re-indexes the files changed since a git ref (`--since`, default `HEAD~1`), or every changed file with `--no-git`. Exits 1 if any file failed, as `index` does. |
| `index-status` | Shows files, chunks, calibration and text-window files. Exits 1 when there is no index. |
| `search <query>` | Finds code by meaning. `code-search`, `docs-search` and `memory-search` are the same search on one profile. |
| `multisearch <q1> [q2] ...` | Runs several queries, removes duplicates and boosts results more than one query found. |
| `callers <symbol>` | Lists what calls a symbol, from recorded call edges. `--full` restores every field of each row. |
| `refs <symbol>` | Lists what a symbol calls, imports and uses. `--full` restores every field of each row. |
| `memory <store\|search\|list\|delete\|promote\|stats\|import>` | Stores and searches question and answer pairs (decisions, clarifications). |
| `collection delete --profile <name>` | Deletes every chunk in one profile, from that profile's own index. Requires `--force`. |
| `setup` | Registers the tool with Claude Code, Codex and OpenCode in this project (see Install). |

Every command accepts `--help`. A long run of options is in `llm-semantic <command> --help`.

### What `index` reads

Every file `index` and `index-update` read is sent to the embedding API, so they read only what belongs to the project:

- Files and directories ignored by `.gitignore` (at any level, including the repository's above the indexed directory) and `.git/info/exclude` (each nested repository's too) are skipped, as are hidden directories (`.config/`, `.docker/`, ...) below the indexed directory.
- Files whose names usually hold credentials are withheld: `credentials*`, `secret*` and `service-account*` data files (`.json`, `.yaml`, `.toml`, `.env`, ...), `.env*`, keys and certificates (`*.pem`, `*.key`, `id_rsa*`, ...), `*.tfstate` and `*.log`.
- Only regular files are read. A symlink is followed only to a file inside the indexed directory; FIFOs and devices are skipped. Files over 1 MiB, and files with a NUL byte in their first 8000 bytes (binary), are withheld.
- Withheld files are counted by reason in `files_withheld` (`secret_name`, `outside_root`, `not_regular_file`, `too_large`, `binary`), and `--plain` prints a `Withheld N files` line.
- `index` refuses your home directory and `/` (exit 2) unless you pass `--allow-home`.
- The index is not written through a symlink: a `.index` directory, `semantic.db` or its `-wal`, `-shm` or `.lock` file that is a symlink is refused.

```bash
# build, then refresh after a commit
llm-semantic index . --exclude-tests
llm-semantic index-update .

# search, narrowed to a path and a chunk type (TOON, the default)
llm-semantic search "where is the retry logic" --path internal/ --type function --top 5

# hybrid dense and lexical search, then several phrasings at once
llm-semantic search "parse the config file" --hybrid
llm-semantic multisearch "load config" "validate config" "config defaults"

# the call graph
llm-semantic callers EnrichForEmbedding
llm-semantic refs EnrichForEmbedding

# decisions
llm-semantic memory store --question "Which store backs the index?" --answer "sqlite by default; qdrant for large corpora"
llm-semantic memory search "index storage"

# a separate index for docs; --profile docs defaults to .index/docs
llm-semantic index docs/ --profile docs
llm-semantic docs-search "how stale is the index" --profile docs

# preview what setup would write
llm-semantic setup --dry-run
```

`--profile` names the index being addressed: `code` (the default), `docs`, `memory` or `sprints`. With the default sqlite store, `code` uses `.index` and every other profile uses `.index/<profile>` (`.index/docs`, `.index/memory`, ...), so `index docs/ --profile docs` after `index .` fills the docs index instead of skipping every file as unchanged. Two things turn that default off: an explicit `--index-dir` (even `--index-dir .index`) and a config file, auto-discovered or given with `--config`. An explicit path is used as given, and with a config but no `--index-dir` every profile stays at `.index`; either way, pointing two profiles at one directory makes them share an index. With `--storage qdrant`, a profile's collection comes from `--collection`, the config file's `<profile>_collection`, or the last part of a non-default index path (under the default, the profile name); otherwise every profile shares the `llm_semantic` collection. The config file also gives each profile its own include and exclude scope.

`collection delete --profile <name>` deletes from that profile's own index — `.index/<profile>` by default, or the explicit `--index-dir`/config path — not from `.index` unless `--index-dir .index` says so. With no index for the profile it fails, naming the missing directory, instead of printing `No chunks found` for another profile's index. A file whose chunks are all deleted also loses its incremental-index record, so `index-status` stops listing it and the next `index` or `index-update` indexes it again; a file that still has chunks under another profile keeps its record and stays skipped there.

## Configuration

Flags win over environment variables, and environment variables over defaults. Secrets are named here, never shown.

| Name | Default | Meaning |
|---|---|---|
| `LLM_SEMANTIC_API_URL` / `--api-url` | `http://localhost:8080` | Embedding endpoint for `--embedder openai` (any OpenAI-compatible server). `cohere`, `huggingface` and `openrouter` call their hosted APIs and ignore it. Credentials in the URL (`user:pass@` or a query such as `?api-key=`) are sent but never printed: messages show only the scheme, host and path. |
| `LLM_SEMANTIC_MODEL` / `--model` | `nomic-embed-text` for `openai` | Embedding model the endpoint serves. Unset, the hosted embedders use their own: `embed-english-v3.0` (cohere), `sentence-transformers/all-MiniLM-L6-v2` (huggingface), `mistralai/codestral-embed-2505` (openrouter). |
| `LLM_SEMANTIC_API_KEY` / `--api-key` | unset, then `OPENAI_API_KEY` | Key for the default `openai` embedder. |
| `--embedder` | `openai` | Request shape: `openai`, `cohere`, `huggingface` or `openrouter`. |
| `COHERE_API_KEY`, `OPENROUTER_API_KEY` | unset | Key for `--embedder cohere` or `openrouter` when `--api-key` is not set. |
| `HUGGING_FACE_API_KEY`, `HUGGINGFACE_API_KEY`, `HF_TOKEN` | unset | Key for `--embedder huggingface`, read in that order. |
| `LLM_SEMANTIC_EMBEDDER_TIMEOUT` | `60` | Seconds one embedding request may take. |
| `LLM_SEMANTIC_RERANKER_API_URL` | unset | Reranker endpoint. When set, searches rerank unless `--no-rerank` is given. |
| `LLM_SEMANTIC_RERANKER_MODEL` | `Qwen/Qwen3-Reranker-0.6B` | Reranker model. |
| `--storage` | `sqlite` | `sqlite` keeps the index in `.index/semantic.db` with no server; `qdrant` uses a Qdrant instance. |
| `--index-dir` | `.index` for `code`, `.index/<profile>` for any other profile | Directory of the sqlite index. Set here or in a config file, it wins for every profile; a leading `~/` is expanded even when quoted. |
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
| none | TOON, the token-dense AXI format to give a model: at most four fields per result, content cut at 200 characters with `--full` for the rest, a `total`, a `status` for empty answers and a `help` list of next steps. |
| `--plain` | Human-readable text. |
| `--json` | JSON. Keeps its pre-AXI schema (full fields, no total) so existing scripts keep working. A search with no results prints `null` when the store returns nothing, or `[]` when reranking or `--hybrid` fusion removes every result (`--min` always prints `[]` when empty), so treat both as empty. A failed reranker prints `reranker failed: ...` on a separate stderr line, so stdout stays a bare JSON array. |
| `--min` | A minimal TOON or JSON payload. Not valid with `--plain`. |
| `--axi` | Accepted so v1 invocations keep working; TOON is already the default, so it changes nothing. |

`--plain`, `--json` and `--axi` each pick a format, so any two together are a usage error (exit 2), as is `--min` with `--plain`.

Search-family output also says how far the index is behind the working tree (`fresh`, `stale` or `unknown`). Under `--json` that line goes to stderr so stdout keeps its schema.

| Exit code | Meaning |
|---|---|
| `0` | Success, including a definitive empty answer: no match, a symbol not indexed, or a bare run with no index. |
| `1` | The command failed: no index for `index-status`, an unreachable endpoint, an `index` or `index-update` run in which any file failed (even if others were indexed), or a destructive command refused without `--force` or `--yes`. Every error goes to stdout in the active format: a TOON or JSON object with `error: true`, or `Error: ...` under `--plain`. A failed `index` or `index-update` prints its usual counts and `errors` list with `error: true` and a `message` in the same TOON or JSON document; `--plain` prints the summary followed by the `Error: ...` line. stderr carries only diagnostics. |
| `2` | Usage error: an unknown flag or command, a missing argument, a bad flag value (such as `--top -1`, `--threshold 5` or an unknown `--profile` or `--embedder`), or contradictory format flags. The message names what was wrong; for an unknown flag or command it lists the valid choices. Nothing is created or sent first. |

`llm-semantic -v`, `-V` and `--version` print the bare version and exit 0, and so do the same flags on `llm-semantic-mcp`.

## Performance

- **Version:** `v2.0.0` (`llm-semantic --version`)
- **Date:** 2026-11-23
- **Machine:** Intel(R) Xeon(R) W-2295 CPU @ 3.00GHz, 36 threads, 141.2 GiB RAM, Debian GNU/Linux 13 (trixie) (linux/amd64), kernel 7.0.0-38-generic
- **Method:** each case runs its warmup runs untimed, then its timed runs: warmup 3 (cold start), 3 (typical command), 1 (large input); runs 20 (cold start), 20 (typical command), 5 (large input). Each timing is the wall clock of one `llm-semantic` process, from start to exit, on the release build. Every `index` and `search` run also gets `--api-url`, `--model`, `--storage sqlite` and `--index-dir` pointing at the fake embedder and a temporary index; each `index` run starts from an empty index.
- **Embeddings:** a fixed-latency local fake: a loopback OpenAI-compatible server that waits 1.0 ms per request and answers with deterministic 256-dimension hash vectors, so the numbers measure the tool and not a model.
- **Large input:** 3,000 files, a corpus the program generates (Go, Python and Markdown files).

| Case | Command | Warmup | Runs | Min (ms) | Median (ms) | Mean (ms) | Max (ms) |
|---|---|---:|---:|---:|---:|---:|---:|
| cold start | `llm-semantic --version` | 3 | 20 | 3.8 | 4.0 | 4.0 | 4.5 |
| typical command | `llm-semantic search "load the config file and validate it" --json` | 3 | 20 | 84.4 | 89.4 | 90.1 | 100.3 |
| large input | `llm-semantic index <corpus> --json` | 1 | 5 | 33973.2 | 35688.4 | 35674.8 | 37751.7 |

Every timing is in [benchmarks/results-performance.json](benchmarks/results-performance.json). Measured by [`benchmarks/cmd/perf`](benchmarks/cmd/perf/main.go).

## How it works

`index` splits each file into chunks (functions, methods and types through tree-sitter, or `go/ast` for Go; sections for Markdown; text windows for other text), embeds each chunk through the endpoint and stores the vectors with the call edges it found. `search` embeds the query and ranks chunks by similarity, optionally fused with a lexical index (`--hybrid`) and reranked. `callers` and `refs` read the stored edges. With the default SQLite storage they never call the endpoint; with Qdrant they call it once, to learn the embedding dimension before opening the collection.

`llm-semantic-mcp` is an MCP server that runs the CLI with `--axi` for each tool call. A tool's path, query, symbol or id is passed after `--`, and a value starting with `-` is refused, so tool input cannot become a CLI option. It is a fallback for environments that cannot run a CLI; the CLI is the product. TOON encoding goes through [go-axi](https://github.com/samestrin/go-axi), which guards against the raw codec's failure mode: a type it cannot encode does not error, it emits empty output, and a command then silently prints nothing.

The tool was extracted from [samestrin/llm-tools](https://github.com/samestrin/llm-tools), which carried three unrelated tools in one Go module. This binary links sqlite, Qdrant and tree-sitter and is about 39 MiB (a `-s -w` build for linux/amd64), a cost anyone wanting the small file tool used to pay as well, so it lives here on its own.

## Limits

- **`callers` and `refs` record call edges for Go, JavaScript, TypeScript, Python and Rust only.** In any other language, an empty answer means "not indexed for edges", not "no callers"; the output says so. Even in those five languages some call shapes are not recorded, so an empty answer is not proof. [docs/languages.md](docs/languages.md) lists what each language records.
- An index built before a language gained call edges has none for it until `llm-semantic index --force`.
- There is no bundled embedding model. `index` and `search` fail without a reachable endpoint, and the quality of `search` is the quality of the model you point it at.
- `--json` keeps its pre-AXI schema: no truncation, no `total`, no zero-result message. Those are in the default TOON output and `--plain` only.
- `setup` registers Claude Code, Codex and OpenCode. Other agents get the skill through `npx skills add`, without a hook.
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
