package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsOf reads and parses a project's .claude/settings.json.
func settingsOf(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings is not valid JSON: %v\n%s", err, b)
	}
	return m
}

// sessionStartCommands lists every hook command registered for SessionStart.
func sessionStartCommands(t *testing.T, settings map[string]any) []string {
	t.Helper()
	var cmds []string
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	for _, g := range groups {
		inner, _ := g.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			if c, ok := h.(map[string]any)["command"].(string); ok {
				cmds = append(cmds, c)
			}
		}
	}
	return cmds
}

// homeGuard points HOME at an empty directory and returns a check that it is
// still empty. Defends the promise that setup writes only project-local files.
func homeGuard(t *testing.T) func() {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return func() {
		t.Helper()
		entries, _ := os.ReadDir(home)
		if len(entries) != 0 {
			t.Errorf("setup touched the home directory: %v", entries)
		}
	}
}

// TestSetupWritesHookAndSkill defends the core contract: both files exist with
// the content a Claude Code session will actually load.
func TestSetupWritesHookAndSkill(t *testing.T) {
	dir := t.TempDir()
	homeOK := homeGuard(t)

	var out bytes.Buffer
	if err := runSetup(&out, dir, false); err != nil {
		t.Fatalf("runSetup: %v", err)
	}

	cmds := sessionStartCommands(t, settingsOf(t, dir))
	if len(cmds) != 1 || cmds[0] != setupHookCommand {
		t.Errorf("SessionStart commands = %v, want [%q]", cmds, setupHookCommand)
	}
	skill, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "llm-semantic", "SKILL.md"))
	if err != nil {
		t.Fatalf("skill not written: %v", err)
	}
	if !strings.HasPrefix(string(skill), "---\nname: llm-semantic\ndescription: ") {
		t.Errorf("skill lacks frontmatter with name and description:\n%s", skill)
	}
	for _, want := range []string{filepath.Join(".claude", "settings.json"), filepath.Join(".claude", "skills", "llm-semantic", "SKILL.md")} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not print the path %q:\n%s", want, out.String())
		}
	}
	homeOK()
}

// TestSetupSecondRunChangesNothingAndSaysSo defends idempotency: no duplicate
// hook entry, no byte changed, and the output must not claim a write.
func TestSetupSecondRunChangesNothingAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	if err := runSetup(&bytes.Buffer{}, dir, false); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, ".claude", "settings.json")
	skill := filepath.Join(dir, ".claude", "skills", "llm-semantic", "SKILL.md")
	before1, _ := os.ReadFile(settings)
	before2, _ := os.ReadFile(skill)

	var out bytes.Buffer
	if err := runSetup(&out, dir, false); err != nil {
		t.Fatal(err)
	}
	after1, _ := os.ReadFile(settings)
	after2, _ := os.ReadFile(skill)
	if !bytes.Equal(before1, after1) || !bytes.Equal(before2, after2) {
		t.Error("a second run changed bytes")
	}
	if n := len(sessionStartCommands(t, settingsOf(t, dir))); n != 1 {
		t.Errorf("hook entries after second run = %d, want 1", n)
	}
	if !strings.Contains(out.String(), "unchanged") || strings.Contains(out.String(), "wrote") {
		t.Errorf("second run must say unchanged and not claim a write:\n%s", out.String())
	}
}

// TestSetupPreservesExistingSettings defends "merge, never overwrite": other
// keys and other SessionStart hooks must survive.
func TestSetupPreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"permissions":{"allow":["Bash(ls)"]},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}],"Stop":[{"hooks":[{"type":"command","command":"echo bye"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runSetup(&bytes.Buffer{}, dir, false); err != nil {
		t.Fatal(err)
	}

	s := settingsOf(t, dir)
	if _, ok := s["permissions"]; !ok {
		t.Error("permissions key was dropped")
	}
	if _, ok := s["hooks"].(map[string]any)["Stop"]; !ok {
		t.Error("the Stop hook was dropped")
	}
	cmds := sessionStartCommands(t, s)
	if len(cmds) != 2 || cmds[0] != "echo hi" || cmds[1] != setupHookCommand {
		t.Errorf("SessionStart commands = %v, want existing hook kept and ours appended", cmds)
	}
}

// TestSetupDryRunWritesNothing defends --dry-run: no file, no directory, and
// the output says what would happen.
func TestSetupDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	homeOK := homeGuard(t)

	var out bytes.Buffer
	if err := runSetup(&out, dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Errorf("dry run created .claude (stat err = %v)", err)
	}
	if !strings.Contains(out.String(), "would write") || strings.Contains(out.String(), "wrote") {
		t.Errorf("dry run output wrong:\n%s", out.String())
	}
	homeOK()
}

// TestSetupDryRunWithExistingSettingsLeavesBytes defends dry-run against an
// existing file that a real run would modify.
func TestSetupDryRunWithExistingSettingsLeavesBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	orig := []byte(`{"model":"x"}`)
	os.WriteFile(p, orig, 0o644)

	if err := runSetup(&bytes.Buffer{}, dir, true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, orig) {
		t.Errorf("dry run modified settings: %s", got)
	}
}

// TestSetupRefusesUnparseableSettings defends against clobbering a settings
// file we cannot read: error, file untouched, no skill half-written claim.
func TestSetupRefusesUnparseableSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	orig := []byte(`{ not json`)
	os.WriteFile(p, orig, 0o644)

	if err := runSetup(&bytes.Buffer{}, dir, false); err == nil {
		t.Fatal("expected an error for invalid settings JSON")
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, orig) {
		t.Errorf("invalid settings were overwritten: %s", got)
	}
}

// TestSetupRefusesWrongShapedHooks defends against a settings file whose
// hooks key has an unexpected type: we must not silently replace it.
func TestSetupRefusesWrongShapedHooks(t *testing.T) {
	for _, body := range []string{`{"hooks":"nope"}`, `{"hooks":{"SessionStart":"nope"}}`, `[]`} {
		dir := t.TempDir()
		p := filepath.Join(dir, ".claude", "settings.json")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		if err := runSetup(&bytes.Buffer{}, dir, false); err == nil {
			t.Errorf("%s: expected an error", body)
		}
		got, _ := os.ReadFile(p)
		if string(got) != body {
			t.Errorf("%s: file was modified to %s", body, got)
		}
	}
}

// TestSetupRewritesStaleSkill defends the skill staying current: an edited
// skill file is restored and reported as written, not as unchanged.
func TestSetupRewritesStaleSkill(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude", "skills", "llm-semantic", "SKILL.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("old"), 0o644)

	var out bytes.Buffer
	if err := runSetup(&out, dir, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != setupSkillContent {
		t.Errorf("stale skill not refreshed: %s", got)
	}
}

// TestSetupCmdRegistered defends the wiring: the command exists on the root,
// accepts --dry-run, and runs against the working directory.
func TestSetupCmdRegistered(t *testing.T) {
	dir := newWorkspace(t)
	root := RootCmd()
	root.SetArgs([]string{"setup", "--dry-run"})
	out := captureStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatalf("setup --dry-run: %v", err)
		}
	})
	if !strings.Contains(out, "would write") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Error("dry run wrote .claude")
	}
}
