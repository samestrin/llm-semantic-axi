# Working on llm-semantic

House rules for an agent making changes **to this repository**.

## What this is

A semantic code search CLI built to the [AXI](https://axi.md) principles. One rule sits above the rest:

**Nothing may report success when it did not succeed.**

A search that returns nothing because the index is empty, reported the same way as a search that genuinely found nothing. An encode that emits an empty document and exits 0. An incremental update that indexed zero files and said "up to date". Each of those is the same bug wearing different clothes. If you are weighing a design choice, that is the tiebreaker.

The sharpest live instance is documented in the README: `callers` and `refs` return empty for any language other than Go, because only `GoChunker` emits call edges. Empty there means "not indexed", not "none exist". Anything that makes that distinction *less* visible is a regression.

## Commands

```bash
make test        # go test ./...
make test-race   # race detector — CI runs this
make lint        # go vet + gofmt check
make build       # both binaries into ./build/ — required before the MCP e2e tests run
make test-cover  # coverage profile and total
```

CI runs `gofmt -s -l`, `go vet`, and `go test -race`, then **fails the build below 80% coverage**. That gate is a hard fail, not a warning. It builds into `./build/` first, because the MCP end-to-end tests exec the real binary.

## Testing

Write the failing test first. Every test carries a comment naming what it defends — usually the bug it prevents.

Five things worth knowing before you add one:

1. **Assert side effects, not exit codes.** An index test that only checks for exit 0 passes even when zero files were indexed. Check the rows in the database.
2. **Tests are not parallel-safe.** `internal/semantic/commands` swaps package globals (`GlobalAXIOutput`, `GlobalJSONOutput`, `GlobalMinOutput`). Never add `t.Parallel()` there, and restore any global you set with `t.Cleanup`.
3. **No network in tests.** There is no embedding endpoint in CI. Remote embedders and Qdrant are tested through `httptest` fakes, injected dependencies, or their error paths. A test that needs a live endpoint will pass on one machine and fail on every other.
4. **sqlite is testable for real.** The backend is `modernc.org/sqlite`, pure Go with no CGO, so a genuine database in `t.TempDir()` costs almost nothing. Prefer that over mocking storage.
5. **Prove a test can fail.** Break the code deliberately and confirm it goes red. A test that passes against sabotaged code is not defending anything.

## Technical debt

`.planning/technical-debt/README.md` is the record. It is the one thing under `.planning/` that is committed, because the validation pipeline runs in a disposable worktree built from the pushed branch and cannot see an untracked file.

**When you resolve an item, update it in the same change that fixes it.** Mark the row `[x]`, rewrite the entry to say what was done while keeping the original report readable, and correct the counts. A debt file that lags reality is worse than none, because it is trusted.

When you find something you are not fixing, file it: severity, `file:line`, what is wrong, and how to reproduce. Severity is about consequence — **HIGH** means a caller can be misled about what happened.

A partial fix does not earn a `[x]`. An item naming two commands or two call sites stays open until both are done, or it is split so the unfinished half stays visible. Ticking the box for half the work is the same failure this repo exists to prevent.

## Scope

Prefer the smallest change that solves the problem. If a fix starts pulling in adjacent work, say so and ask rather than expanding quietly.

This repository was extracted from `llm-tools` and the extraction is not finished across the wider toolchain. Do not port behaviour back and forth between the two; `llm-tools` is frozen on go-axi v0.1.2 and is being archived.
