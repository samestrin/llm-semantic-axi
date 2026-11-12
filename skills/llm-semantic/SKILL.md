---
name: llm-semantic
description: Semantic code search over a local index. Use to find code by meaning, or to look up callers and references of a symbol, when grep on exact text is not enough.
---

# llm-semantic

Run `llm-semantic` with no arguments to see whether this directory has an index.

- `llm-semantic index .` builds the index; `llm-semantic index-update` refreshes it.
- `llm-semantic search "<query>"` finds code by meaning.
- `llm-semantic callers <symbol>` and `llm-semantic refs <symbol>` work for Go, JavaScript, TypeScript, Python and Rust. An empty result for any other language means not indexed, not that none exist.
- Add `--json` or `--axi` for machine-readable output.
