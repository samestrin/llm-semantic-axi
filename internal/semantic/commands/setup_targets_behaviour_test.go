package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/samestrin/llm-semantic-axi/pkg/output"
)

// The files each target owns, relative to the project directory.
var (
	claudeSettingsRel = filepath.Join(".claude", "settings.json")
	claudeSkillRel    = filepath.Join(".claude", "skills", "llm-semantic", "SKILL.md")
	codexHooksRel     = filepath.Join(".codex", "hooks.json")
	codexConfigRel    = filepath.Join(".codex", "config.toml")
	opencodePluginRel = filepath.Join(".opencode", "plugins", "llm-semantic.ts")
)

var filesByTarget = map[string][]string{
	"claude":   {claudeSettingsRel, claudeSkillRel},
	"codex":    {codexHooksRel, codexConfigRel},
	"opencode": {opencodePluginRel},
}

// hookCommandsIn returns every SessionStart command a target registered, read
// back from the file the agent itself loads.
func hookCommandsIn(t *testing.T, dir, target string) []string {
	t.Helper()
	switch target {
	case "claude", "codex":
		rel := claudeSettingsRel
		if target == "codex" {
			rel = codexHooksRel
		}
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s is not valid JSON: %v\n%s", rel, err, b)
		}
		return sessionStartCommands(t, m)
	case "opencode":
		b, err := os.ReadFile(filepath.Join(dir, opencodePluginRel))
		if err != nil {
			t.Fatalf("read plugin: %v", err)
		}
		return pluginCommands(string(b))
	}
	t.Fatalf("unknown target %q", target)
	return nil
}

var pluginCommandRE = regexp.MustCompile(`(?m)^const command = (".*")$`)

// pluginCommands decodes the command constants a plugin declares.
func pluginCommands(src string) []string {
	var cmds []string
	for _, m := range pluginCommandRE.FindAllStringSubmatch(src, -1) {
		var s string
		if json.Unmarshal([]byte(m[1]), &s) == nil {
			cmds = append(cmds, s)
		}
	}
	return cmds
}

func assertExists(t *testing.T, dir string, rels []string, want bool) {
	t.Helper()
	for _, rel := range rels {
		_, err := os.Stat(filepath.Join(dir, rel))
		if want && err != nil {
			t.Errorf("%s not written: %v", rel, err)
		}
		if !want && !os.IsNotExist(err) {
			t.Errorf("%s exists but its target was not selected", rel)
		}
	}
}

// TestSetupEachTargetWritesOnlyItsFilesOnce defends --target narrowing and the
// first write: each target's files exist and hold exactly one hook, and no
// other target's files appear. Without this, narrowing to one agent could
// still drop files into another agent's directory.
func TestSetupEachTargetWritesOnlyItsFilesOnce(t *testing.T) {
	withPlain(t)
	for target, files := range filesByTarget {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			if err := runSetup(&out, dir, setupOptions{targets: []string{target}}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
			assertExists(t, dir, files, true)
			for other, otherFiles := range filesByTarget {
				if other != target {
					assertExists(t, dir, otherFiles, false)
				}
			}
			if cmds := hookCommandsIn(t, dir, target); len(cmds) != 1 || cmds[0] != setupHookCommand {
				t.Errorf("commands = %v, want [%q]", cmds, setupHookCommand)
			}
			for _, rel := range files {
				if !strings.Contains(out.String(), "wrote "+rel) {
					t.Errorf("output does not say %s was written:\n%s", rel, out.String())
				}
			}
		})
	}
}

// TestSetupEachTargetUnchangedOnRerun defends idempotency per target: a
// second run changes no byte and must not claim a write.
func TestSetupEachTargetUnchangedOnRerun(t *testing.T) {
	withPlain(t)
	for target, files := range filesByTarget {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			opts := setupOptions{targets: []string{target}}
			if err := runSetup(&bytes.Buffer{}, dir, opts); err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, rel := range files {
				before[rel], _ = os.ReadFile(filepath.Join(dir, rel))
			}
			var out bytes.Buffer
			if err := runSetup(&out, dir, opts); err != nil {
				t.Fatal(err)
			}
			for _, rel := range files {
				after, _ := os.ReadFile(filepath.Join(dir, rel))
				if !bytes.Equal(before[rel], after) {
					t.Errorf("rerun changed %s", rel)
				}
				if !strings.Contains(out.String(), "unchanged "+rel) {
					t.Errorf("rerun does not report %s unchanged:\n%s", rel, out.String())
				}
			}
			if strings.Contains(out.String(), "wrote") {
				t.Errorf("rerun claims a write:\n%s", out.String())
			}
		})
	}
}

// TestSetupEachTargetRepairsAMovedBinary defends path repair: a hook written
// with an absolute path breaks when the binary moves, and a rerun must point
// it at the new location in place, not append a second hook that leaves the
// broken one firing in every session.
func TestSetupEachTargetRepairsAMovedBinary(t *testing.T) {
	withPlain(t)
	for target := range filesByTarget {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			oldBin := filepath.Join(t.TempDir(), "llm-semantic")
			newBin := filepath.Join(t.TempDir(), "llm-semantic")
			opts := setupOptions{targets: []string{target}, bin: oldBin}
			if err := runSetup(&bytes.Buffer{}, dir, opts); err != nil {
				t.Fatal(err)
			}
			if cmds := hookCommandsIn(t, dir, target); len(cmds) != 1 || cmds[0] != oldBin+" index-status || true" {
				t.Fatalf("first run commands = %v", cmds)
			}

			opts.bin = newBin
			var out bytes.Buffer
			if err := runSetup(&out, dir, opts); err != nil {
				t.Fatal(err)
			}
			want := newBin + " index-status || true"
			if cmds := hookCommandsIn(t, dir, target); len(cmds) != 1 || cmds[0] != want {
				t.Errorf("after move commands = %v, want [%q]", cmds, want)
			}
			if !strings.Contains(out.String(), "wrote") {
				t.Errorf("repair not reported as a write:\n%s", out.String())
			}
		})
	}
}

// TestSetupRepairCollapsesDuplicatesAndLeavesForeignHooks defends the repair
// matcher: every stale llm-semantic hook collapses to one current hook, and a
// hook that is not ours, even one that mentions llm-semantic, is kept as is.
func TestSetupRepairCollapsesDuplicatesAndLeavesForeignHooks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, codexHooksRel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	existing := `{"hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/gone/llm-semantic index-status || true"}]},
		{"hooks":[{"type":"command","command":"llm-semantic"},{"type":"command","command":"echo keep"}]},
		{"hooks":[{"type":"command","command":"'/old dir/llm-semantic' index-status || true"}]},
		{"hooks":[{"type":"command","command":"llm-semantic search foo"}]},
		{"hooks":[{"type":"command","command":"/x/other-tool index-status || true"}]}
	]}}`
	os.WriteFile(p, []byte(existing), 0o644)

	if err := runSetup(&bytes.Buffer{}, dir, setupOptions{targets: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	got := hookCommandsIn(t, dir, "codex")
	want := []string{setupHookCommand, "echo keep", "llm-semantic search foo", "/x/other-tool index-status || true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q\nwant %q", got, want)
	}
}

// TestSetupDefaultRegistersEveryTarget defends Principle 7: running setup with
// no --target registers all three agents, not Claude Code alone.
func TestSetupDefaultRegistersEveryTarget(t *testing.T) {
	withPlain(t)
	dir := t.TempDir()
	if err := runSetup(&bytes.Buffer{}, dir, setupOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, files := range filesByTarget {
		assertExists(t, dir, files, true)
	}
}

// TestSetupTargetFlagNarrowsThroughTheCommand defends the wiring of the
// repeatable --target flag: two targets named means exactly those two.
func TestSetupTargetFlagNarrowsThroughTheCommand(t *testing.T) {
	dir := newWorkspace(t)
	stubSetupBinary(t, "", nil)
	if err := executeRoot(t, "setup", "--plain", "--target", "codex", "--target", "opencode"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	assertExists(t, dir, filesByTarget["codex"], true)
	assertExists(t, dir, filesByTarget["opencode"], true)
	assertExists(t, dir, filesByTarget["claude"], false)
}

// TestSetupRejectsAnUnknownTarget defends exit 2 for a usage error, naming
// the valid choices, before anything is written.
func TestSetupRejectsAnUnknownTarget(t *testing.T) {
	dir := newWorkspace(t)
	stubSetupBinary(t, "", nil)
	err := executeRoot(t, "setup", "--plain", "--target", "cursor")
	if err == nil {
		t.Fatal("expected an error for --target cursor")
	}
	if code := output.ExitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2 (%v)", code, err)
	}
	for _, want := range []string{"cursor", "claude", "codex", "opencode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	for _, files := range filesByTarget {
		assertExists(t, dir, files, false)
	}
}

// stubSetupBinary empties PATH, so llm-semantic is not on it, and makes the
// running executable resolve to exe (or fail with exeErr).
func stubSetupBinary(t *testing.T, exe string, exeErr error) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	old := setupExecutable
	setupExecutable = func() (string, error) { return exe, exeErr }
	t.Cleanup(func() { setupExecutable = old })
}

func putOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "llm-semantic"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// TestSetupBinaryIsPathVerified defends the command choice: the bare name only
// when PATH really finds llm-semantic, otherwise the absolute path of the
// running binary, because a bare name that PATH cannot find makes every
// session start with a "command not found" hook.
func TestSetupBinaryIsPathVerified(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "llm-semantic")
	stubSetupBinary(t, exe, nil)

	if got, err := resolveSetupBinary(); err != nil || got != exe {
		t.Errorf("not on PATH: got %q, %v; want %q", got, err, exe)
	}
	putOnPath(t)
	if got, err := resolveSetupBinary(); err != nil || got != "llm-semantic" {
		t.Errorf("on PATH: got %q, %v; want the bare name", got, err)
	}
}

// TestSetupBinaryUnresolvableIsAnError defends against writing a hook that
// cannot run: when PATH lacks the binary and the executable path is unknown,
// setup fails rather than writing a bare name that will never be found.
func TestSetupBinaryUnresolvableIsAnError(t *testing.T) {
	dir := newWorkspace(t)
	stubSetupBinary(t, "", os.ErrNotExist)
	if err := executeRoot(t, "setup", "--plain"); err == nil {
		t.Fatal("expected an error when llm-semantic cannot be located")
	}
	for _, files := range filesByTarget {
		assertExists(t, dir, files, false)
	}
}

// TestSetupCommandQuotesAnAbsolutePathWithSpaces defends the fallback through
// the real command: a path with a space must reach every hook shell-quoted,
// or the shell runs "/opt/my" and the hook fails in every session.
func TestSetupCommandQuotesAnAbsolutePathWithSpaces(t *testing.T) {
	dir := newWorkspace(t)
	stubSetupBinary(t, "/opt/my tools/llm-semantic", nil)
	if err := executeRoot(t, "setup", "--plain"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	want := "'/opt/my tools/llm-semantic' index-status || true"
	for target := range filesByTarget {
		if cmds := hookCommandsIn(t, dir, target); len(cmds) != 1 || cmds[0] != want {
			t.Errorf("%s commands = %q, want [%q]", target, cmds, want)
		}
	}
}

// TestSetupCodexConfigMergeKeepsEverythingElse defends "TOML merged, other
// keys kept": comments, other tables and other [features] keys survive, and a
// hooks = false the user left is turned on, since running setup is the opt-in.
func TestSetupCodexConfigMergeKeepsEverythingElse(t *testing.T) {
	cases := map[string]string{
		"features table with hooks off": "# my codex config\nmodel = \"o3\"\n\n[features]\nweb_search = true # keep\nhooks = false\n\n[profiles.fast]\nmodel = \"mini\"\n",
		"features table without hooks":  "model = \"o3\"\n[features]\nweb_search = true\n[profiles.fast]\nmodel = \"mini\"\n",
		"no features table":             "# my codex config\nmodel = \"o3\"\n[profiles.fast]\nmodel = \"mini\"",
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, codexConfigRel)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(existing), 0o644)

			if err := runSetup(&bytes.Buffer{}, dir, setupOptions{targets: []string{"codex"}}); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(p)
			var before, after map[string]any
			if _, err := toml.Decode(existing, &before); err != nil {
				t.Fatal(err)
			}
			if _, err := toml.Decode(string(got), &after); err != nil {
				t.Fatalf("result is not valid TOML: %v\n%s", err, got)
			}
			features, _ := after["features"].(map[string]any)
			if features["hooks"] != true {
				t.Errorf("features.hooks = %v, want true\n%s", features["hooks"], got)
			}
			delete(features, "hooks")
			if len(features) == 0 {
				delete(after, "features")
			}
			if bf, ok := before["features"].(map[string]any); ok {
				delete(bf, "hooks")
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("other keys changed:\nbefore %v\nafter  %v", before, after)
			}
			if strings.HasPrefix(existing, "#") && !strings.HasPrefix(string(got), "# my codex config\n") {
				t.Errorf("comment was dropped:\n%s", got)
			}
		})
	}
}

// TestSetupRefusesCodexConfigItCannotMergeSafely defends "never overwrite what
// we cannot merge": an unparseable config, or features defined in a form a
// line edit cannot reach, fails the whole run and leaves every file as it was.
func TestSetupRefusesCodexConfigItCannotMergeSafely(t *testing.T) {
	for name, existing := range map[string]string{
		"unparseable":         "model = \n",
		"inline features":     "features = { web_search = true }\n",
		"dotted features key": "features.web_search = true\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, codexConfigRel)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(existing), 0o644)

			if err := runSetup(&bytes.Buffer{}, dir, setupOptions{}); err == nil {
				t.Fatal("expected an error")
			}
			if got, _ := os.ReadFile(p); string(got) != existing {
				t.Errorf("config modified:\n%s", got)
			}
			assertExists(t, dir, filesByTarget["claude"], false)
			assertExists(t, dir, []string{codexHooksRel, opencodePluginRel}, false)
		})
	}
}

// TestSetupCodexConfigAlreadyOnIsUnchanged defends idempotency for a config
// that already enables hooks in a form other than ours: no byte is rewritten.
func TestSetupCodexConfigAlreadyOnIsUnchanged(t *testing.T) {
	withPlain(t)
	dir := t.TempDir()
	p := filepath.Join(dir, codexConfigRel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	existing := "features = { hooks = true }\n"
	os.WriteFile(p, []byte(existing), 0o644)
	var out bytes.Buffer
	if err := runSetup(&out, dir, setupOptions{targets: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != existing {
		t.Errorf("config rewritten:\n%s", got)
	}
	if !strings.Contains(out.String(), "unchanged "+codexConfigRel) {
		t.Errorf("not reported unchanged:\n%s", out.String())
	}
}

// fixture reads a checked-in file for a pinned agent version.
func fixture(t *testing.T, agentVersion, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "setup", agentVersion, name))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return b
}

// TestSetupGeneratedFilesMatchPinnedFixtures defends the file formats against
// the agent versions they were checked with: Codex rejects a root-level
// SessionStart and ignores hooks.json without [features].hooks, and OpenCode
// loads only a named Plugin export that reacts to session.created. A change to
// what setup writes must change the fixture too, in the same review.
func TestSetupGeneratedFilesMatchPinnedFixtures(t *testing.T) {
	dir := t.TempDir()
	if err := runSetup(&bytes.Buffer{}, dir, setupOptions{}); err != nil {
		t.Fatal(err)
	}
	codexDir := "codex-" + codexPinnedVersion
	opencodeDir := "opencode-" + opencodePinnedVersion

	gotHooks, _ := os.ReadFile(filepath.Join(dir, codexHooksRel))
	var g, w any
	if err := json.Unmarshal(gotHooks, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixture(t, codexDir, "hooks.json"), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("hooks.json differs from the %s fixture:\n%s", codexDir, gotHooks)
	}
	if _, ok := g.(map[string]any)["SessionStart"]; ok {
		t.Error("hooks.json has a root-level SessionStart, which Codex rejects")
	}

	gotConfig, _ := os.ReadFile(filepath.Join(dir, codexConfigRel))
	var gc, wc map[string]any
	if _, err := toml.Decode(string(gotConfig), &gc); err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(string(fixture(t, codexDir, "config.toml")), &wc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gc, wc) {
		t.Errorf("config.toml differs from the %s fixture:\n%s", codexDir, gotConfig)
	}

	gotPlugin, _ := os.ReadFile(filepath.Join(dir, opencodePluginRel))
	wantPlugin := fixture(t, opencodeDir, "llm-semantic.ts")
	if !strings.Contains(string(wantPlugin), "OpenCode "+opencodePinnedVersion) ||
		!strings.Contains(string(wantPlugin), "@opencode-ai/plugin "+opencodePinnedVersion) {
		t.Errorf("plugin fixture does not name the pinned versions")
	}
	for what, re := range map[string]*regexp.Regexp{
		"import":  regexp.MustCompile(`(?m)^import type \{ Plugin \} from "@opencode-ai/plugin"$`),
		"export":  regexp.MustCompile(`(?m)^export const \w+: Plugin = `),
		"event":   regexp.MustCompile(`event\.type !== "session\.created"`),
		"command": pluginCommandRE,
	} {
		w := re.FindAllString(string(wantPlugin), -1)
		got := re.FindAllString(string(gotPlugin), -1)
		if len(w) != 1 || !reflect.DeepEqual(got, w) {
			t.Errorf("plugin %s = %q, fixture has %q", what, got, w)
		}
	}
	if cmds := pluginCommands(string(gotPlugin)); len(cmds) != 1 || cmds[0] != setupHookCommand {
		t.Errorf("plugin command = %q", cmds)
	}
}
