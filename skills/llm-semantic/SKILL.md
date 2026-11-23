---
name: llm-semantic
description: Semantic code search over a local index. Use to find code by meaning, or to look up callers and references of a symbol, when grep on exact text is not enough.
---

# llm-semantic

Run `llm-semantic` with no arguments to see whether this directory has an index.

- `llm-semantic index .` builds the index; `llm-semantic index-update` refreshes it.
- `llm-semantic search "<query>"` finds code by meaning.
- `llm-semantic callers <symbol>` and `llm-semantic refs <symbol>` work for Go, JavaScript, TypeScript, Python and Rust. An empty result for any other language means not indexed, not that none exist.
- Output is TOON by default; stay on it. `--json` keeps the old JSON schema, which has no `status`, `total`, `help` or `stale` field (its staleness line goes to stderr), `--plain` gives plain text, and `--axi` remains an alias for TOON.

## The embedding API

`index`, `index-update`, `search`, `multisearch`, `memory store`, `memory search` and `memory import` need an OpenAI-compatible embedding API, set with `--api-url` or `LLM_SEMANTIC_API_URL` (default `http://localhost:8080`). `cannot reach the embedding API at <url>` means nothing answered there: start the embedder, or point `--api-url` at one that is running. `callers`, `refs` and `index-status` do not need it on the default SQLite storage.

## Empty answers are not always proof

- Read the `status` field of an empty `callers` or `refs` answer. `symbol_not_indexed` means no chunk carries the name; `not_indexed_for_edges` means the symbol is indexed only in languages with no call edges (`--plain` says `symbol not indexed` and `not indexed for edges`). Neither means no caller exists.
- Even in those five languages some calls are not recorded: calls through another object (`obj.foo()`, `a.b.foo()`), `super.foo()`, and Rust path calls (`Type::f()`, `Self::f()`) and macros. An empty answer for a function reached only that way is not proof. https://github.com/samestrin/llm-semantic-axi/blob/main/docs/languages.md lists what each language records.
- An index built before a language's edges were recorded has none for it. Rebuild with `llm-semantic index --force`; `index-update` does not re-chunk files that have not changed.
- `stale.state: stale` on a `search`, `callers` or `refs` answer means the index is behind the working tree: `commits_behind` commits since it was built, or `files_changed` uncommitted and untracked files in the repository. Run `llm-semantic index-update --no-git`, which rescans every file; plain `index-update` applies only the last commit's changes. Uncommitted files keep the state `stale` until they are committed, even once indexed. `fresh` means it is current; `unknown` and `not_applicable` mean it could not be checked.

## Output and exit codes

- TOON rows are cut to a few fields and content to 200 characters; `--full` restores every field.
- A failure prints `error: true` with a `message`, and a `help` list of next steps where there is one, on stdout in the active format (`Error: ...` under `--plain`).
- Exit 0 is success, including a definitive empty answer. Exit 1 means the command failed: no index, an unreachable embedding API, a file that could not be indexed, or a destructive command (`collection delete`, `memory stats --prune`) refused without `--force` / `--yes`. Exit 2 is a usage error: an unknown flag or command, a missing or extra argument, a missing required flag, a bad flag value, or contradictory format flags; the message names what was wrong and shows the right form.
