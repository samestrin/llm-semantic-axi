package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

// setupHookArgs follows the binary in every hook setup writes. It asks for
// index-status and tolerates its exit code: a bare `llm-semantic` exits
// non-zero in a directory with no index (by design, so a missing index is
// never silent), and Claude Code renders that as a hook error in every
// un-indexed project. `|| true` keeps the status text on the session output
// while letting the session start cleanly.
const setupHookArgs = " index-status || true"

// setupHookCommand is the hook when PATH finds llm-semantic.
const setupHookCommand = "llm-semantic" + setupHookArgs

// legacySetupHookCommand is the bare command setup used to write. A settings
// file carrying it is migrated to the current command rather than left to error.
const legacySetupHookCommand = "llm-semantic"

// The agent versions the Codex and OpenCode files were checked against. The
// fixtures under testdata/setup/ are named for them.
const (
	codexPinnedVersion    = "0.166.0"
	opencodePinnedVersion = "1.19.5"
)

// setupTargetNames is every agent setup registers, in the order it writes them.
var setupTargetNames = []string{"claude", "codex", "opencode"}

// setupExecutable locates the running binary; tests replace it.
var setupExecutable = os.Executable

// setupSkillContent is also committed at skills/llm-semantic/SKILL.md, the copy
// `npx skills add` installs. TestCommittedSkillEqualsWhatSetupWrites fails when
// the two differ, so change both together.
const setupSkillContent = `---
name: llm-semantic
description: Semantic code search over a local index. Use to find code by meaning, or to look up callers and references of a symbol, when grep on exact text is not enough.
---

# llm-semantic

Run ` + "`llm-semantic`" + ` with no arguments to see whether this directory has an index.

- ` + "`llm-semantic index .`" + ` builds the index; ` + "`llm-semantic index-update`" + ` refreshes it.
- ` + "`llm-semantic search \"<query>\"`" + ` finds code by meaning.
- ` + "`llm-semantic callers <symbol>`" + ` and ` + "`llm-semantic refs <symbol>`" + ` work for Go, JavaScript, TypeScript, Python and Rust. An empty result for any other language means not indexed, not that none exist.
- Output is TOON by default. ` + "`--json`" + ` gives JSON output, ` + "`--plain`" + ` gives plain text, and ` + "`--axi`" + ` remains an alias for TOON.
`

func setupCmd() *cobra.Command {
	var dryRun bool
	var targets []string

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register llm-semantic with Claude Code, Codex and OpenCode in this project",
		Long: `Register llm-semantic with coding agents in this project:

  claude    a SessionStart hook in .claude/settings.json (merged, other keys
            kept) and an Agent Skill under .claude/skills/llm-semantic/
  codex     a SessionStart hook in .codex/hooks.json (merged) and
            [features] hooks = true in .codex/config.toml (merged, other keys kept)
  opencode  a plugin at .opencode/plugins/llm-semantic.ts that adds the
            index-status output to each new session

All three by default; --target narrows (repeatable). The hooks run
"llm-semantic index-status || true" when PATH finds llm-semantic, otherwise
this binary's absolute path; rerun setup after moving the binary to repair
them. Only files under the current directory are written. Safe to run again;
never prompts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, t := range targets {
				if !slices.Contains(setupTargetNames, t) {
					// Worded as cobra words a bad flag value, so it exits 2.
					return withHelp(fmt.Errorf("invalid argument %q for \"--target\" flag: use 'claude', 'codex' or 'opencode'", t), helpPointer(cmd))
				}
			}
			bin, err := resolveSetupBinary()
			if err != nil {
				return err
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			return runSetup(os.Stdout, dir, setupOptions{dryRun: dryRun, targets: targets, bin: bin})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be written without writing")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Register only this agent: claude, codex or opencode (repeatable; default all)")
	return cmd
}

// resolveSetupBinary decides how hooks invoke llm-semantic: the bare name when
// PATH finds it, otherwise the absolute path of the running binary. A bare
// name PATH cannot find would fail at the start of every session.
func resolveSetupBinary() (string, error) {
	if _, err := exec.LookPath("llm-semantic"); err == nil {
		return "llm-semantic", nil
	}
	exe, err := setupExecutable()
	if err == nil && exe == "" {
		err = errors.New("empty path")
	}
	if err != nil {
		return "", fmt.Errorf("llm-semantic is not on PATH and the path of this binary is unknown (%v); add llm-semantic to PATH and rerun setup", err)
	}
	return exe, nil
}

// setupHookCommandFor is the hook command for bin, shell-quoted when needed.
func setupHookCommandFor(bin string) string {
	return shellQuote(bin) + setupHookArgs
}

var shellSafeRE = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if shellSafeRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isSetupHook reports whether command is one setup wrote, for any binary
// location and including the legacy bare command, so a rerun repairs it in
// place. A compound or foreign command never matches.
func isSetupHook(command string) bool {
	if command == legacySetupHookCommand {
		return true
	}
	bin, ok := strings.CutSuffix(command, setupHookArgs)
	if !ok {
		return false
	}
	if len(bin) >= 2 && strings.HasPrefix(bin, "'") && strings.HasSuffix(bin, "'") {
		inner := bin[1 : len(bin)-1]
		if strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'") {
			return false // more than one quoted word
		}
		bin = strings.ReplaceAll(inner, `'\''`, "'")
	} else if !shellSafeRE.MatchString(bin) {
		return false
	}
	return filepath.Base(bin) == "llm-semantic"
}

// setupOptions selects what runSetup writes.
type setupOptions struct {
	dryRun  bool
	targets []string // empty means every target
	bin     string   // how hooks invoke llm-semantic; empty means the bare name
}

// setupAction is one planned file change.
type setupAction struct {
	path    string // relative to the project directory
	content []byte
	changed bool
}

// runSetup plans every change before performing any, so a file that cannot be
// merged fails the command without a half-finished setup.
func runSetup(w io.Writer, dir string, opts setupOptions) error {
	bin := opts.bin
	if bin == "" {
		bin = "llm-semantic"
	}
	command := setupHookCommandFor(bin)
	targets := opts.targets
	if len(targets) == 0 {
		targets = setupTargetNames
	}
	for _, t := range targets {
		if !slices.Contains(setupTargetNames, t) {
			return fmt.Errorf("unknown setup target %q: use claude, codex or opencode", t)
		}
	}

	var actions []setupAction
	plan := func(rel string, planner func(path string) (setupAction, error)) error {
		a, err := planner(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		a.path = rel
		actions = append(actions, a)
		return nil
	}
	hooks := func(path string) (setupAction, error) { return planHooksJSON(path, command) }
	fixed := func(content []byte) func(string) (setupAction, error) {
		return func(path string) (setupAction, error) { return planFixedFile(path, content) }
	}
	for _, t := range setupTargetNames {
		if !slices.Contains(targets, t) {
			continue
		}
		var err error
		switch t {
		case "claude":
			if err = plan(filepath.Join(".claude", "settings.json"), hooks); err == nil {
				err = plan(filepath.Join(".claude", "skills", "llm-semantic", "SKILL.md"), fixed([]byte(setupSkillContent)))
			}
		case "codex":
			if err = plan(filepath.Join(".codex", "hooks.json"), hooks); err == nil {
				err = plan(filepath.Join(".codex", "config.toml"), planCodexConfig)
			}
		case "opencode":
			err = plan(filepath.Join(".opencode", "plugins", "llm-semantic.ts"), fixed(opencodePluginContent(command)))
		}
		if err != nil {
			return err
		}
	}

	jsonMode := GlobalJSONOutput || GlobalAXIOutput
	var results []map[string]string
	for _, a := range actions {
		status := "unchanged"
		switch {
		case !a.changed:
		case opts.dryRun:
			status = "would_write"
		default:
			status = "wrote"
			full := filepath.Join(dir, a.path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, a.content, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", a.path, err)
			}
		}
		if jsonMode {
			results = append(results, map[string]string{"path": a.path, "status": status})
		} else {
			fmt.Fprintf(w, "%s %s\n", strings.ReplaceAll(status, "_", " "), a.path)
		}
	}
	if jsonMode {
		return emitJSON(w, results, true)
	}
	return nil
}

// planFixedFile plans a file setup owns outright: rewritten whenever its bytes
// differ from content.
func planFixedFile(path string, content []byte) (setupAction, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return setupAction{}, fmt.Errorf("read %s: %w", path, err)
	}
	return setupAction{content: content, changed: !bytes.Equal(existing, content)}, nil
}

// planHooksJSON computes the merged hooks file without writing: Claude Code's
// .claude/settings.json and Codex's .codex/hooks.json share the shape
// {"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":...}]}]}}.
// When command is already the one registered hook of ours, changed is false
// and the file's bytes are left alone.
func planHooksJSON(path, command string) (setupAction, error) {
	root := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		// UseNumber keeps numbers verbatim; float64 would round large ones.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return setupAction{}, fmt.Errorf("%s is not a JSON object, refusing to overwrite: %w", path, err)
		}
		if root == nil { // the JSON value null
			return setupAction{}, fmt.Errorf("%s is null, not a JSON object, refusing to overwrite", path)
		}
	case !os.IsNotExist(err):
		return setupAction{}, fmt.Errorf("read %s: %w", path, err)
	}

	hooks := map[string]any{}
	if v, ok := root["hooks"]; ok {
		if hooks, ok = v.(map[string]any); !ok {
			return setupAction{}, fmt.Errorf("%s: \"hooks\" is not an object, refusing to overwrite", path)
		}
	}
	groups := []any{}
	if v, ok := hooks["SessionStart"]; ok {
		if groups, ok = v.([]any); !ok {
			return setupAction{}, fmt.Errorf("%s: \"hooks.SessionStart\" is not a list, refusing to overwrite", path)
		}
	}
	groups, changed := reconcileSessionStart(groups, command)
	if !changed {
		return setupAction{content: raw}, nil
	}
	hooks["SessionStart"] = groups
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return setupAction{}, err
	}
	return setupAction{content: append(out, '\n'), changed: true}, nil
}

// reconcileSessionStart leaves exactly one hook of ours, running command. The
// first hook setup wrote earlier is rewritten in place, so a hook pointing at
// a moved binary or the legacy bare command is repaired rather than joined by
// a second one; any further hooks of ours are dropped. Other hooks are kept.
func reconcileSessionStart(groups []any, command string) ([]any, bool) {
	found, changed := false, false
	out := make([]any, 0, len(groups)+1)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, ok := gm["hooks"].([]any)
		if !ok {
			out = append(out, g)
			continue
		}
		kept := make([]any, 0, len(inner))
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			c, _ := hm["command"].(string)
			switch {
			case !isSetupHook(c):
				kept = append(kept, h)
			case !found:
				found = true
				if c != command {
					hm["command"] = command
					changed = true
				}
				kept = append(kept, h)
			default:
				changed = true // a duplicate of ours
			}
		}
		if len(kept) == len(inner) {
			out = append(out, g)
		} else if len(kept) > 0 {
			gm["hooks"] = kept
			out = append(out, g)
		}
	}
	if !found {
		out = append(out, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command}},
		})
		changed = true
	}
	return out, changed
}

var (
	tomlTableHeaderRE = regexp.MustCompile(`^\s*\[`)
	tomlFeaturesRE    = regexp.MustCompile(`^\s*\[\s*features\s*\]\s*(#.*)?$`)
	tomlHooksKeyRE    = regexp.MustCompile(`^\s*hooks\s*=`)
)

// planCodexConfig turns on [features] hooks in .codex/config.toml, without
// which Codex ignores .codex/hooks.json. It edits lines rather than
// re-encoding, so comments and key order survive, then decodes the result and
// refuses unless the only change is features.hooks = true.
func planCodexConfig(path string) (setupAction, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return setupAction{}, fmt.Errorf("read %s: %w", path, err)
	}
	before := map[string]any{}
	if _, err := toml.Decode(string(raw), &before); err != nil {
		return setupAction{}, fmt.Errorf("%s is not valid TOML, refusing to overwrite: %w", path, err)
	}
	features, hasFeatures := before["features"]
	if fm, ok := features.(map[string]any); ok && fm["hooks"] == true {
		return setupAction{content: raw}, nil
	}

	lines := strings.SplitAfter(string(raw), "\n")
	header := slices.IndexFunc(lines, func(l string) bool {
		return tomlFeaturesRE.MatchString(strings.TrimRight(l, "\r\n"))
	})
	var out string
	switch {
	case header >= 0:
		end := len(lines)
		for j := header + 1; j < len(lines); j++ {
			if tomlTableHeaderRE.MatchString(lines[j]) {
				end = j
				break
			}
		}
		if j := slices.IndexFunc(lines[header+1:end], tomlHooksKeyRE.MatchString); j >= 0 {
			line := lines[header+1+j]
			lines[header+1+j] = "hooks = true" + line[len(strings.TrimRight(line, "\r\n")):]
		} else {
			if !strings.HasSuffix(lines[header], "\n") {
				lines[header] += "\n"
			}
			lines = slices.Insert(lines, header+1, "hooks = true\n")
		}
		out = strings.Join(lines, "")
	case hasFeatures:
		return setupAction{}, fmt.Errorf("%s sets features outside a [features] table, refusing to overwrite; add hooks = true to it by hand", path)
	default:
		out = string(raw)
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if out != "" {
			out += "\n"
		}
		out += "[features]\nhooks = true\n"
	}

	after := map[string]any{}
	if _, err := toml.Decode(out, &after); err != nil {
		return setupAction{}, fmt.Errorf("%s: cannot add [features] hooks = true safely, refusing to overwrite: %w", path, err)
	}
	fm, _ := after["features"].(map[string]any)
	ok := fm["hooks"] == true
	delete(fm, "hooks")
	if !hasFeatures && len(fm) == 0 {
		delete(after, "features")
	}
	if bf, isMap := before["features"].(map[string]any); isMap {
		delete(bf, "hooks")
	}
	if !ok || !reflect.DeepEqual(before, after) {
		return setupAction{}, fmt.Errorf("%s: cannot add [features] hooks = true without changing other keys, refusing to overwrite; add it by hand", path)
	}
	return setupAction{content: []byte(out), changed: true}, nil
}

// opencodePluginContent is the plugin OpenCode loads from
// .opencode/plugins/llm-semantic.ts. OpenCode auto-loads only direct children
// of that directory. On each new top-level session it runs command and adds
// the output to the session without asking the model to reply, as the
// SessionStart hooks do for Claude Code and Codex.
func opencodePluginContent(command string) []byte {
	// A JSON string is a valid JS string literal; a string always marshals.
	lit, _ := json.Marshal(command)
	return []byte(strings.Join([]string{
		"// Managed by `llm-semantic setup` for OpenCode " + opencodePinnedVersion + " (@opencode-ai/plugin " + opencodePinnedVersion + ").",
		"// Rerun `llm-semantic setup` to update this file; edits here are overwritten.",
		`import type { Plugin } from "@opencode-ai/plugin"`,
		"",
		"const command = " + string(lit),
		"",
		"export const LlmSemantic: Plugin = async ({ $, client, directory }) => ({",
		"  event: async ({ event }) => {",
		`    if (event.type !== "session.created" || event.properties.info.parentID) return`,
		"    const status = await $`sh -c ${command}`.cwd(directory).nothrow().quiet().text()",
		"    if (!status.trim()) return",
		"    await client.session.prompt({",
		"      path: { id: event.properties.info.id },",
		`      body: { noReply: true, parts: [{ type: "text", text: status }] },`,
		"    })",
		"  },",
		"})",
		"",
	}, "\n"))
}
