# Technical debt

The record for this repository. Committed on purpose: the validation pipeline runs in a disposable worktree built from the pushed branch, so an untracked file would be invisible to it.

Severity is about consequence. **HIGH** means a caller can be misled about what happened.

## Stats

| | Open | Resolved | Total |
|---|---|---|---|
| HIGH | 0 | 0 | 0 |
| MEDIUM | 3 | 0 | 3 |
| LOW | 1 | 0 | 1 |
| **Total** | **4** | **0** | **4** |

## Open

- [ ] **MEDIUM** — `cmd/llm-semantic-mcp/main.go` — `serverInstructions` tells the client the server "wraps the llm-semantic CLI with 4 tools". It registers **16**. A model reading the instruction string is told the surface is a quarter of its real size. Reproduce: run the binary and compare its startup line (`started with 16 tools`) against the instructions text. Fix is a one-line correction, but it wants a test pinning the two to each other so the number cannot drift again.

- [ ] **MEDIUM** — `internal/semantic/config/config.go:9` and `internal/semantic/mcpserver/handlers.go:12,83-112` — the config is parsed twice by two different YAML libraries. The `config` package uses `goccy/go-yaml` and produces typed errors carrying line and column; the MCP handler re-declares its own `SemanticConfig`/`Config` structs and parses with `gopkg.in/yaml.v3`. The two struct sets are not equivalent: the handler's `SemanticConfig` has eight fields, collection and storage per profile, while the `config` package reads five keys per profile. So `enabled`, `include` and `exclude` are silently ignored on the MCP path, and a scope configured in the file does not apply there. Two parsers over one file drift, and this pair already has. Collapsing them also removes one YAML dependency.

- [ ] **MEDIUM** — `internal/semantic/commands/root.go:40` and `internal/semantic/mcpserver/handlers.go:114` — the default config path `.planning/.config/config.yaml` is hardcoded in two places. They can disagree, and nothing would catch it.

- [ ] **LOW** — `internal/semantic` — call edges are emitted by `GoChunker` only. Eight chunkers ship (generic, go, html, javascript, markdown, php, python, rust) and seven are silent, so `callers` and `refs` return an empty result for non-Go files that is indistinguishable from "no callers exist". Documented in the README and in `AGENTS.md` rather than fixed, because extending ref extraction past Go is a feature, not a defect. Filed so the ambiguity stays visible.

## Resolved

*(none yet)*
