# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Standalone repository, extracted from [samestrin/llm-tools](https://github.com/samestrin/llm-tools). `internal/semantic`, `cmd/llm-semantic`, `cmd/llm-semantic-mcp` and the used slice of `pkg/output` move across unchanged.
- `Makefile` with `build`, `build-all`, `test`, `test-race`, `test-cover`, `lint`, `vet`, `fmt`, `install`, `clean` and `hooks` targets. `llm-tools` had no semantic build targets at all, so this is new rather than copied.
- CI: `lint` → `test -race` → `coverage`, chained by `needs:`, plus a five-platform cross-compile matrix and a tag-triggered release workflow.
- `.golangci.yml` matching the selection used across `samestrin/atcr` and `samestrin/go-axi`.
- Parse-error fallback for the tree-sitter chunkers: when a file's parse tree has an error, the functions and classes still parsed are kept and every other non-blank line is indexed as generic text windows (a chunker failure or panic windows the whole file). `index` and `index-update` print `Chunked N files as text windows (parse failed):` with the paths, and `--json` / `--axi` carry `FilesFallback` and `FallbackFiles` (`files_fallback`, `fallback_files` for `index-update`). (epic 02)
- C chunker (`.c`): functions, structs, unions, enums and typedefs are indexed with names and line ranges, including definitions inside `#ifdef`. `.h` is left to the C++ chunker. (epic 02)
- Java chunker (`.java`): classes, enums and records are `struct` chunks, interfaces and annotation types are `interface` chunks, and methods and constructors (including interface method declarations) are `method` chunks, with annotations kept in the content and dropped from the signature. Java files were previously skipped. Like every non-Go language, Java emits no call edges, so an empty `callers`/`refs` for a Java file means "not indexed". (epic 02)
- TypeScript chunker for `.ts`, `.tsx`, `.mts` and `.cts`. `.tsx` is parsed with the TSX grammar, so React components index as functions and classes instead of falling back to text windows; `.mts` and `.cts` files, previously skipped, are now indexed. Abstract classes, enums and `export default class` are chunked too. The JavaScript chunker keeps `.js`, `.jsx`, `.mjs` and `.cjs`. (epic 02)
- Tree-sitter C++ chunker for `cpp`, `cxx`, `cc`, `hpp`, `hxx`, `hh` and `h`. Functions, classes, structs, unions, enums, namespaces and `extern "C"` blocks become named chunks with line ranges; a template's range includes its `template <...>` header; out-of-line `Foo::bar` definitions are methods. `.h` is registered here once (the C++ grammar parses C headers); a header it rejects falls back to text windows. Like every non-Go chunker it emits no call edges, so `callers` and `refs` return empty for C++ files. (epic 02)
- Ruby chunker (`.rb`): classes are `struct` chunks, modules are `file` chunks (the type the Rust and C++ chunkers give a namespace), and `def` is a `method` inside a class or module and a `function` at top level. `def self.x`, methods in `class << self`, `private def x`, endless defs and scoped `class A::B` names are covered. Ruby files were previously skipped. Like every non-Go chunker it emits no call edges, so `callers` and `refs` return empty for Ruby files. (epic 02.1)

### Changed

- go-axi moves from **v0.1.2** to **v0.3.0**.
- Eleven direct dependencies that only `internal/support` and `internal/filesystem` used are dropped: `BurntSushi/toml`, `html-to-markdown`, `goquery`, `cascadia`, `expr`, `go-gitignore`, `go-diff`, `gjson`, `sjson`, `match`, `pretty`. Thirteen direct requires remain.
- Both binaries now report the version they were built from. `version` and `commit` in `cmd/llm-semantic`, and `serverVersion` in `cmd/llm-semantic-mcp`, are stamped by `-X` at build time instead of being hardcoded to `1.5.0` and `1.6.0`.
- `serverVersion` changed from a `const` to a `var`. `ldflags -X` rewrites a symbol, and a const has no symbol to rewrite, so the hardcoded value would have silently outlived every tag.

### Coverage gate

CI fails below 80% total coverage rather than printing a percentage and moving on. The sibling repo only echoes the number, which is how its coverage drifted unobserved.
