# AXI conformance

Where `llm-semantic` stands against the ten principles on [axi.md](https://axi.md). This table is the current record; [axi-audit.md](axi-audit.md) is the snapshot it replaced.

| # | Principle | Status | Notes |
|---|---|---|---|
| 1 | Token-efficient output (TOON) | **Exception** | TOON is opt-in through `--axi`, a persistent flag on every command — the default output is human-readable plain text, not TOON. The MCP server runs the CLI with `--axi` so tools get TOON by default. |
| 2 | Minimal default schemas (3-4 fields per item) | Met | `--axi` search rows carry four fields (`file` as `path:line`, `name`, `score`, `content`); `memory search` carries `id`, `question`, `score`, `answer`. `--full` opts back into every field. |
| 3 | Content truncation with a size hint and `--full` | Met | `--axi` cuts content over 200 characters and says how many were left out. `--full` returns the whole text. |
| 4 | Pre-computed aggregates, always a total | Met | The search family states its total (`N results for "query"` plain, `total` in `--axi`); `multisearch` reports `total_queries` and `total_results`. |
| 5 | Definitive empty states | Met | An empty search says it found nothing in plain and `--axi` (`message`), and a search over an index with no chunks says the index is empty instead (`status: index_empty` vs `no_match` in `--axi`). `callers`/`refs` distinguish `symbol not indexed`, `not indexed for edges` and a genuine empty answer (see [languages.md](languages.md)). |
| 6 | Structured errors on stdout, exit 0/1, idempotent, never prompt | **Exception** | A malformed invocation exits 2 and plain-mode errors go to stderr — a deliberate go-axi convention, kept so a usage mistake reads differently from a command failure. No command prompts: `collection delete` without `--force` and `memory stats --prune` without `--yes` refuse with an error naming the flag and exit 1, deleting nothing. Everything else prints structured errors on stdout (`error: true` in `--json`, TOON in `--axi`) and exits 0/1. |
| 7 | Ambient context (`setup` command, Agent Skill) | **Exception** | `llm-semantic setup` targets Claude Code only: it merges a SessionStart hook and an Agent Skill into the project's `.claude/` and writes nothing for other agents — no session-end hook and nothing under your home directory (see Usage). |
| 8 | Content first (no-argument run shows live data) | Met | A bare `llm-semantic` prints a description, the executable path (home as `~`) and the index status for the current directory. In an unindexed directory it states `status: not_indexed`, suggests `llm-semantic index .` as the next step and exits 0. |
| 9 | Contextual disclosure (`help[]` next steps) | Met | `--axi` output of the search family, `memory search`, `multisearch` and `index-status` carries a `help` list of suggested next steps, as does the missing-index error. |
| 10 | Consistent `--help` on every subcommand | Met | `--help` exits 0 on every subcommand, and an unknown flag or command names it, lists the valid choices and exits 2. The root `-v`/`-V`/`--version` print the bare version and exit 0 on both binaries (a subcommand's `-v` keeps its own meaning, e.g. `index --verbose`). |

Scope note for rows 2-5: they are met in `--axi` (and plain, where the note says so). `--json` keeps its pre-AXI schema so existing scripts keep working — full fields, untruncated content, and a bare result array without `total` or a zero-result message — so the machine-readable JSON mode does not meet principles 2-5 and is a standing exception with that reason.
