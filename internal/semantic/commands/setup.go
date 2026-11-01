package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// setupHookCommand is what Claude Code runs at session start. A bare run
// shows the index status for the directory.
const setupHookCommand = "llm-semantic"

const setupSkillContent = `---
name: llm-semantic
description: Semantic code search over a local index. Use to find code by meaning, or to look up callers and references of a Go symbol, when grep on exact text is not enough.
---

# llm-semantic

Run ` + "`llm-semantic`" + ` with no arguments to see whether this directory has an index.

- ` + "`llm-semantic index .`" + ` builds the index; ` + "`llm-semantic index-update`" + ` refreshes it.
- ` + "`llm-semantic search \"<query>\"`" + ` finds code by meaning.
- ` + "`llm-semantic callers <symbol>`" + ` and ` + "`llm-semantic refs <symbol>`" + ` work for Go only. An empty result for any other language means not indexed, not that none exist.
- Add ` + "`--json`" + ` or ` + "`--axi`" + ` for machine-readable output.
`

func setupCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register llm-semantic with Claude Code in this project",
		Long: `Write a SessionStart hook into .claude/settings.json (merged, other keys
kept) and an Agent Skill under .claude/skills/llm-semantic/. Only files under
the current directory are written. Safe to run again; never prompts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			return runSetup(os.Stdout, dir, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be written without writing")
	return cmd
}

// setupAction is one planned file change.
type setupAction struct {
	path    string // relative to the project directory
	content []byte
	changed bool
}

// runSetup plans both changes before performing either, so a settings file
// that cannot be merged fails the command without a half-finished setup.
func runSetup(w io.Writer, dir string, dryRun bool) error {
	settingsRel := filepath.Join(".claude", "settings.json")
	skillRel := filepath.Join(".claude", "skills", "llm-semantic", "SKILL.md")

	settings, err := planSettings(filepath.Join(dir, settingsRel))
	if err != nil {
		return err
	}
	settings.path = settingsRel

	skillPath := filepath.Join(dir, skillRel)
	existing, err := os.ReadFile(skillPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", skillRel, err)
	}
	skill := setupAction{path: skillRel, content: []byte(setupSkillContent)}
	skill.changed = !bytes.Equal(existing, skill.content)

	jsonMode := GlobalJSONOutput || GlobalAXIOutput
	var results []map[string]string
	for _, a := range []setupAction{settings, skill} {
		status := "unchanged"
		switch {
		case !a.changed:
		case dryRun:
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

// planSettings computes the merged settings without writing. When our hook is
// already registered, changed is false and the file's bytes are left alone.
func planSettings(path string) (setupAction, error) {
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
	if hasSessionStartCommand(groups, setupHookCommand) {
		return setupAction{content: raw}, nil
	}

	groups = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": setupHookCommand}},
	})
	hooks["SessionStart"] = groups
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return setupAction{}, err
	}
	return setupAction{content: append(out, '\n'), changed: true}, nil
}

func hasSessionStartCommand(groups []any, command string) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == command {
				return true
			}
		}
	}
	return false
}
