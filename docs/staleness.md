# How stale the index is

The search family (`search`, its profile aliases, `multisearch`, `callers`, `refs`) reports how far the index is behind the working tree, so a search over old code does not read like a search over current code.

- `--axi` carries a `stale` object. `state` is `fresh`, `stale` or `unknown`; a stale or fresh answer that was measured adds `commits_behind`, `files_changed`, `indexed_commit` and `indexed_at`, and an `unknown` one adds a `reason` (for example `no_record` for an index built before the record existed). Counts appear only when measured, so nothing reads as "0 behind" that was never checked. `memory search` reports `state: not_applicable` — memories are stored, not indexed from the working tree.
- Plain output ends with a line such as `Index is stale: 3 commits behind HEAD, 1 uncommitted file changed (indexed at 1a2b3c4d5e6f on 2026-11-02T19:37:48Z). Run llm-semantic index <path> to bring it up to date.` — or `Index staleness unknown: <reason>...` when it cannot be measured.
- `--json` and `--min` keep their stdout schema unchanged so existing scripts keep working; the same line goes to **stderr** instead, so the JSON stays parseable.
- A fresh index prints no line. Only `--axi` says `fresh`, because that is the output an agent branches on.
- The record is written only by an `index` or `index-update` in which every file indexed. A run where any file failed leaves the previous record in place, so the index keeps reporting how far behind it was.
