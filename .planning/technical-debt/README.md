# Technical debt

The record for this repository. Committed on purpose: the validation pipeline runs in a disposable worktree built from the pushed branch, so an untracked file would be invisible to it.

Severity is about consequence. **HIGH** means a caller can be misled about what happened.

## Stats

| | Open | Resolved | Total |
|---|---|---|---|
| HIGH | 3 | 0 | 3 |
| MEDIUM | 4 | 0 | 4 |
| LOW | 2 | 0 | 2 |
| **Total** | **9** | **0** | **9** |

Every item below was found by writing a test, sabotaging the code it covers, and reading what the failure actually said. None came from reading the source.

## Open

- [ ] **HIGH** — `internal/semantic/storage_sqlite.go:90` and `:129` — the schema block creates `idx_chunks_file_mtime ON chunks(file_mtime)` **before** `migrateMtimeColumn()` is called. On a database written before `file_mtime` existed, `CREATE INDEX` hits a column that is not there yet and `initSchema` fails with `no such column: file_mtime`. The migration that exists precisely to add that column never gets to run, so the one case it was written for is the one case it cannot serve, and the old index is unopenable.

  `LexicalIndex.initSchema` (`internal/semantic/lexical_index.go:85-91`) orders the same two steps correctly and carries the comment "This must run BEFORE creating indexes that reference file_mtime". So the correct ordering was already known in this codebase and one of the two call sites did not get it. Fix: move the migration above the index creation in `storage_sqlite.go`, matching its sibling. Pinned by `TestSQLiteStorage_OpenLegacyDatabase`.

- [ ] **HIGH** — `internal/semantic/search.go:86`, `:103`, `:156` — `Searcher.Search` silently ignores the caller's `TopK` whenever reranking is on. Line 86 overwrites `searchOpts.TopK` with the over-fetch candidate count, line 103 hands those same options to `applyReranking`, and the trim at line 156 therefore cuts to the candidate count rather than to what the caller asked for. `Search(ctx, q, {TopK: 2, Rerank: true})` returns up to 50 results. `PrefilterSearch` repeats the bug at `:333` and `:348`.

  This is the "do not report something other than what was asked" rule: nothing errors, and a caller that asked for 2 gets 50 with no signal that its limit was discarded. Fix: keep the caller's `TopK` in a separate variable from the candidate count and trim with the former. Pinned as-is by `TestSearch_RerankingIgnoresCallerTopK`, whose comment names the bug so the test is not mistaken for approval of the behaviour.

- [ ] **MEDIUM** — `internal/semantic/lexical_index.go:708` — `LexicalIndex.GetAllMemoryStats` runs `LEFT JOIN memory m ON ms.memory_id = m.id`, but `LexicalIndex.initSchema` never creates a `memory` table; it creates `chunks`, `chunks_fts`, `memory_stats` and `retrieval_log`. A standalone lexical index therefore always errors from this method. It works today only when the index happens to share a database file with a `SQLiteStorage`, which makes it an accident of deployment rather than a contract.

- [ ] **HIGH** — `internal/semantic/index_manager.go:115-121` — `IndexResult` carries no struct tags at all, while `UpdateResult` six lines below it tags every field. So `llm-semantic index --json` emits `FilesProcessed` and `ChunksCreated`, and `llm-semantic index-update --json` emits `files_updated` and `chunks_created`. One binary, two naming conventions, and a consumer parsing both has to know which command it called.

  It is filed HIGH rather than MEDIUM because of what it does to `--axi`. `output.EncodeAXI` runs `projectJSON` first — marshal to JSON, then decode — precisely so the TOON encoder honours the `json` tag instead of the Go identifier. With no tag to honour, the Go field names become the published TOON column contract. That is the exact defect class go-axi's `CheckTags` exists to catch: nothing errors, nothing is empty, and the column names are silently wrong. An instruction like "the third column is ChunksCreated" is then pinned to a Go identifier that any refactor may rename.

  Fix: tag `IndexResult` to match `UpdateResult`, and add a `CheckTags` test over every type these commands print so the next untagged struct fails at test time rather than in a consumer.

- [ ] **MEDIUM** — `cmd/llm-semantic-mcp/main.go` — `serverInstructions` tells the client the server "wraps the llm-semantic CLI with 4 tools". It registers **16**. A model reading the instruction string is told the surface is a quarter of its real size. Reproduce: run the binary and compare its startup line (`started with 16 tools`) against the instructions text. Fix is a one-line correction, but it wants a test pinning the two to each other so the number cannot drift again.

- [ ] **MEDIUM** — `internal/semantic/config/config.go:9` and `internal/semantic/mcpserver/handlers.go:12,83-112` — the config is parsed twice by two different YAML libraries. The `config` package uses `goccy/go-yaml` and produces typed errors carrying line and column; the MCP handler re-declares its own `SemanticConfig`/`Config` structs and parses with `gopkg.in/yaml.v3`. The two struct sets are not equivalent: the handler's `SemanticConfig` has eight fields, collection and storage per profile, while the `config` package reads five keys per profile. So `enabled`, `include` and `exclude` are silently ignored on the MCP path, and a scope configured in the file does not apply there. Two parsers over one file drift, and this pair already has. Collapsing them also removes one YAML dependency.

- [ ] **MEDIUM** — `internal/semantic/commands/root.go:40` and `internal/semantic/mcpserver/handlers.go:114` — the default config path `.planning/.config/config.yaml` is hardcoded in two places. They can disagree, and nothing would catch it.

- [ ] **LOW** — `internal/semantic` — call edges are emitted by `GoChunker` only. Eight chunkers ship (generic, go, html, javascript, markdown, php, python, rust) and seven are silent, so `callers` and `refs` return an empty result for non-Go files that is indistinguishable from "no callers exist". Documented in the README and in `AGENTS.md` rather than fixed, because extending ref extraction past Go is a feature, not a defect. Filed so the ambiguity stays visible.

- [ ] **LOW** — `internal/semantic/commands/index.go:652-674` — `filterExcludedPaths` applies two matchers where one can never decide alone. Disabling the path-component check leaves the tests green, and so does disabling the `strings.Contains` check, because any exclude matching as a path segment necessarily also matches as a substring. Disabling both does go red, so the behaviour is pinned correctly and this is not a correctness bug — it is dead logic that reads as if it were load-bearing. Found by sabotage while writing `TestFilterExcludedPathsMatchesGlobs`.

## Resolved

*(none yet)*
