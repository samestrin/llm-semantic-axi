# Languages and call edges

What `llm-semantic index` indexes, and what `callers` and `refs` can answer for it. The README's known-limit section summarises this page.

**Empty means "not indexed", not "none exist".** Only the Go, JavaScript, TypeScript, Python and Rust chunkers record call edges, so an empty `callers` or `refs` answer in any other language is not proof that nothing calls the symbol. The table and the per-language limits are below.

## Language table

Every extension `RegisterAllChunkers` registers, and what indexes it. A test (`TestReadmeLanguageTable_MatchesRegisteredExtensions`) fails when this table and the registered extensions disagree.

| Language | Extensions | Chunker | Call edges |
|---|---|---|---|
| Go | `.go` | Go (`go/ast`) | yes |
| C | `.c` | tree-sitter | no |
| C++ | `.cpp` `.cxx` `.cc` `.hpp` `.hxx` `.hh` `.h` | tree-sitter | no |
| C# | `.cs` | tree-sitter | no |
| Java | `.java` | tree-sitter | no |
| JavaScript | `.js` `.jsx` `.mjs` `.cjs` | tree-sitter | yes |
| Kotlin | `.kt` `.kts` | tree-sitter | no |
| PHP | `.php` `.php5` `.php7` `.phtml` | tree-sitter | no |
| Python | `.py` `.pyi` `.pyw` | tree-sitter | yes |
| Ruby | `.rb` | tree-sitter | no |
| Rust | `.rs` | tree-sitter | yes |
| TypeScript | `.ts` `.tsx` `.mts` `.cts` | tree-sitter | yes |
| Markdown | `.md` `.markdown` | markdown | no |
| HTML | `.html` `.htm` | HTML | no |
| Other text | `.adoc` `.bash` `.cfg` `.conf` `.csv` `.diff` `.fish` `.ini` `.json` `.log` `.patch` `.rst` `.sh` `.text` `.toml` `.txt` `.xml` `.yaml` `.yml` `.zsh` | generic text windows | no |

## Known limit: call edges exist for Go, JavaScript, TypeScript, Python and Rust only

`callers` and `refs` read call edges from the index, and only the Go, JavaScript, TypeScript, Python and Rust chunkers emit them (see the table above). **An empty `callers` result in a Java, C# or any other file whose row says `no` means "not indexed for edges", not "no callers."** Do not read it as proof.

JavaScript and TypeScript record a call only when the callee is a bare name (`foo()`) or has a `this.` receiver (`this.foo()`). `obj.foo()`, `a.b.foo()` and `super.foo()` are not recorded, so an empty `callers` for a method that is only called through another object is still not proof. A `this.foo()` call links only to a `foo` method of the caller's own class, in the same file; when that class has none, or the call is not inside a class, `refs` lists it as unresolved rather than linking it to some other `foo`.

Python records a call when the callee is a bare name (`foo()`) or has a `self.` or `cls.` receiver, and records an `imports` edge for each imported module or name. `obj.foo()` and `a.b.foo()` are not recorded and there are no type-use edges, so an empty `callers` for a method that is only called through another object is not proof there. A call is attributed to the innermost function or method that contains it, never to the enclosing class.

Rust records a call when the callee is a bare name (`helper()`, `helper::<T>()`) or has a `self.` receiver and the caller's own `impl` or trait defines a method of that name, and records an `imports` edge for each `use`, named by the path's last segment with aliases ignored and nothing for a wildcard. `obj.f()`, path calls (`Type::f()`, `Self::f()`) and macros are not recorded, and there are no type-use edges. A call is attributed to the innermost function or method that contains it, as in Python. A call inside an inline `mod` body or in a `const` or `static` initialiser has no function to belong to and records no edge, and neither does `self.f()` when `f` is defined in a second `impl` block rather than the caller's own; nothing tracks that last miss. So an empty `callers` for a method reached only through those forms is not proof.

An `imports` edge, in any language, names a module or package, and a module has no chunk, so `refs` always lists it as unresolved. `import debounce from "lodash/debounce"` is not linked to a local `function debounce`, and Go's `import "net/http"` is not linked to a `func http`.

**An index built before this change has no JavaScript, TypeScript, Python or Rust edges. Run `llm-semantic index --force` to rebuild it;** `index-update` does not re-chunk files whose hash is unchanged.

The commands say which case an empty answer is, in plain and `--axi` output (`--axi` adds a `status` field): `symbol not indexed` when no chunk carries the name, `not indexed for edges` when the symbol is indexed only in languages the index holds no edges for, and `recorded in the call graph` only when an empty answer is genuine. `--json` keeps its old shape and does not carry this.
