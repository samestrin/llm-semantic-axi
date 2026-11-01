# AXI audit: `llm-semantic` against the ten principles on axi.md

- **Audited:** 2026-11-01, on a `make build` of `main` at `d7daeaf`.
- **Method:** by hand. `axi-axi` is not installed on the build host. Each principle was checked against the text on https://axi.md, by running `build/llm-semantic` in an empty temp directory and reading the code the output comes from.
- **Status:** a snapshot. Epics 05 and 05.1 close the gaps below. Once 05.1 T5 adds the README conformance table, that table is the current record and this file is only history.

| # | Principle (axi.md) | Verdict | Evidence | Closed by |
|---|---|---|---|---|
| 1 | Token-efficient output (TOON) | **Met** for CLI success output | `--axi` is a persistent flag, and every success emit site goes through `emitJSON`, which calls `output.EncodeAXI` (`internal/semantic/commands/axi.go:39-50`). The MCP server still asks for `--json --min` (`internal/semantic/mcpserver/handlers.go:197-210`), and `--axi` errors are not TOON (see 6b). | MCP half: 05.1 T4 |
| 2 | Minimal default schemas (3-4 fields per item) | **Not met** | A default search result is `chunk` (12 keys: `id`, `file_path`, `type`, `name`, `signature`, `content`, `start_line`, `end_line`, `language`, `domain`, `file_mtime`, `content_hash`) plus `score`, `relevance` and `preview` (`internal/semantic/types.go:78-143`). Only `--min` trims the keys. | 05.1 T3 |
| 3 | Content truncation with a size hint and `--full` | **Not met** | `content` is emitted whole. No command has a `--full` flag, and no output contains a truncation hint (grep finds neither). | 05.1 T3 |
| 4 | Pre-computed aggregates, always a total | **Not met** | `multisearch` reports `total_queries` and `total_results` (`multisearch.go:151-152`). `search`, `code-search`, `docs-search`, `memory-search`, `callers` and `refs` emit a bare list with no total. | 05.1 T1 |
| 5 | Definitive empty states | **Not met** | Plain mode prints `No results found.` (`search.go:272`). `--json` prints `[]`, and `--axi` prints `[0]:`. That is an empty list, not a zero-result message. On a non-Go file, an empty `callers`/`refs` reads as "none exist" when it means "not indexed". | 05.1 T1 |
| 6 | Structured errors on stdout, exit 0/1, idempotent, never prompt | **Not met** (four gaps) | See 6a-6d below. Partly met: `search` with no index exits 1, and `--json` prints `{"error": true, "message": ...}` on stdout. | 6a: 05 T2. 6b, 6d: **neither** (gap). 6c: 05.1 T5 (exception) |
| 7 | Ambient context (setup command, Agent Skill) | **Not met** | `--help` lists no `setup` command, and none exists in `internal/semantic/commands/`. | 05 T4 |
| 8 | Content first (no-argument run shows live data, path as `~`) | **Not met** | A run with no arguments prints the cobra help text and exits 0. | 05 T3 |
| 9 | Contextual disclosure (`help[]` next steps) | **Not met** | No output in any mode carries a `help` list (grep finds none). The nearest thing is plain-mode prose such as `Run 'llm-semantic index' to create one.` | 05.1 T2 |
| 10 | Consistent `--help` on every subcommand | **Met** | `--help` exits 0 for all 12 subcommands (`callers`, `code-search`, `collection`, `docs-search`, `index`, `index-status`, `index-update`, `memory`, `memory-search`, `multisearch`, `refs`, `search`) and for `collection delete`. | — |

## Principle 6 in detail

- **6a. `index-status` with no index exits 0.** Probed output:
  - `--axi`: `error: index not found` / `indexed: false` on stdout, exit 0.
  - `--json`: `{"error":"index not found","indexed":false}`, exit 0.
  - plain: `Semantic index not found.` on stdout, exit 0.

  Closed by **05 T2**.
- **6b. `--axi` errors are plain text on stderr.** `search foo --axi --index-dir ./nope` prints `Error: semantic index not found...` on stderr with nothing on stdout (exit 1). With `--json` the same error is structured on stdout. The cause: `output.PrintError` has an AXI branch, but it takes the AXI setting from `output.SetDefaultAXI`. Nothing outside `pkg/output/axi_test.go` calls that function, so `cmd/llm-semantic/main.go:23-26` always builds a non-AXI formatter. **Neither 05 nor 05.1 has a task for this.** 05.1 AC2 ("errors follow principle 6, a test asserts an error run") would catch it only if its test runs in `--axi`. Filed as tech debt on the issue.
- **6c. Usage errors exit 2, and plain-mode errors go to stderr.** `--bogus` and an unknown subcommand print `Error: unknown flag: --bogus` on stderr and exit 2. This holds in `--axi` mode too (6b). axi.md asks for exit codes 0/1 and errors on stdout. Exit 2 for a malformed invocation is a deliberate go-axi convention (`goaxi.ExitUsage`, `pkg/output/exit.go:52-70`), and stderr is deliberate for plain text (`pkg/output/formatter.go:365-378`). **05.1 T5** must state both as README exceptions with that reason.
- **6d. Two commands prompt, and their abort exits 0.** `collection delete` without `--force` (`collection.go:103-118`) and `memory stats --prune` without `--yes` (`memory.go:1396-1409`) print `Are you sure? [y/N]` and read stdin. On a TTY that is an interactive prompt. On closed stdin they print `Aborted...` and return nil, so a deletion that did not happen exits 0. **Neither 05 nor 05.1 has a task for this.** Filed as tech debt on the issue.

Idempotency of mutations (`index`, `index-update`, `memory`) was not probed. It needs an embedding endpoint, and none exists on the build host.
