package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeFile writes data to path, creating its directory.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withSettings merges a snippet's "hooks" key into settings that already
// hold another key and a hook of the user's own under Stop.
func withSettings(t *testing.T, snippet string) string {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(snippet), &s); err != nil {
		t.Fatal(err)
	}
	hooks := s["hooks"].(map[string]any)
	own := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "make lint", "timeout": 600}}}
	hooks["Stop"] = append(hooks["Stop"].([]any), own)
	out, err := json.Marshal(map[string]any{"model": "opus", "hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// codexTOML writes the Codex snippet as inline [hooks] tables of
// config.toml, as bunk's snippet did before hooks.json.
func codexTOML() string {
	r, _ := recipe("codex")
	var b strings.Builder
	b.WriteString("model = \"o3\"\n")
	for _, h := range r.hooks {
		fmt.Fprintf(&b, "\n[[hooks.%s]]\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = '%s'\ntimeout = 5\n", h.event, h.event, shellCommand(h.args))
	}
	return b.String()
}

// codexState is the table of trust hashes Codex writes into config.toml's
// [hooks] for the hooks it has run.
const codexState = `
[hooks.state]

[hooks.state."/home/u/.codex/hooks.json:stop:0:0"]
trusted_hash = "sha256:f57907b637acdfbc34ae08636cc471f5745fcef7bac198c9bdba25d5ef885fd6"
`

// TestCheckHooks: each agent's hooks are missing without a file, installed
// when every hook of the snippet is there beside the user's own settings
// and hooks, and outdated when only older bunk hooks are. The variables
// that move an agent's directory are followed, Codex's config.toml counts
// as much as its hooks.json, and an unreadable file is a problem, not an
// installation.
func TestCheckHooks(t *testing.T) {
	t.Parallel()
	snippet := func(agent string) string {
		s, ok := HookSnippet(agent)
		if !ok {
			t.Fatalf("no %s snippet", agent)
		}
		return s
	}
	const oldDone = `bunk notify done`
	for _, tc := range []struct {
		name   string
		agent  string
		env    map[string]string
		files  map[string]string // home-relative path → contents
		state  HookState
		path   string // home-relative
		issue  bool   // a Problem is reported
		absent []string
	}{
		{name: "claude without settings", agent: "claude", state: HooksMissing, path: ".claude/settings.json"},
		{name: "claude merged into settings", agent: "claude", files: map[string]string{".claude/settings.json": withSettings(t, snippet("claude"))},
			state: HooksInstalled, path: ".claude/settings.json"},
		{name: "claude with an older hook", agent: "claude",
			files: map[string]string{".claude/settings.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + oldDone + `"}]}]}}`},
			state: HooksOutdated, path: ".claude/settings.json",
			absent: []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "StopFailure", "SessionEnd"}},
		{name: "claude with other hooks only", agent: "claude",
			files: map[string]string{".claude/settings.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"make lint"}]}]}}`},
			state: HooksMissing, path: ".claude/settings.json"},
		{name: "claude settings not JSON", agent: "claude", files: map[string]string{".claude/settings.json": "{"},
			state: HooksMissing, path: ".claude/settings.json", issue: true},
		{name: "claude in CLAUDE_CONFIG_DIR", agent: "claude", env: map[string]string{"CLAUDE_CONFIG_DIR": "conf/claude"},
			files: map[string]string{"conf/claude/settings.json": snippet("claude")}, state: HooksInstalled, path: "conf/claude/settings.json"},
		{name: "codex hooks.json", agent: "codex", files: map[string]string{".codex/hooks.json": snippet("codex")},
			state: HooksInstalled, path: ".codex/hooks.json"},
		{name: "codex config.toml", agent: "codex", files: map[string]string{".codex/config.toml": codexTOML()},
			state: HooksInstalled, path: ".codex/hooks.json"},
		{name: "codex trust state", agent: "codex",
			files: map[string]string{".codex/hooks.json": snippet("codex"), ".codex/config.toml": "model = \"o3\"\n" + codexState},
			state: HooksInstalled, path: ".codex/hooks.json"},
		{name: "codex config.toml with trust state", agent: "codex", files: map[string]string{".codex/config.toml": codexTOML() + codexState},
			state: HooksInstalled, path: ".codex/hooks.json"},
		{name: "codex config.toml not TOML", agent: "codex",
			files: map[string]string{".codex/hooks.json": snippet("codex"), ".codex/config.toml": "[hooks"},
			state: HooksInstalled, path: ".codex/hooks.json", issue: true},
		{name: "codex in CODEX_HOME", agent: "codex", env: map[string]string{"CODEX_HOME": "conf/codex"},
			files: map[string]string{".codex/hooks.json": snippet("codex")}, state: HooksMissing, path: "conf/codex/hooks.json"},
		{name: "copilot", agent: "copilot", files: map[string]string{".copilot/hooks/bunk.json": snippet("copilot")},
			state: HooksInstalled, path: ".copilot/hooks/bunk.json"},
		{name: "copilot hooks in another file", agent: "copilot", files: map[string]string{".copilot/hooks/mine.json": snippet("copilot")},
			state: HooksInstalled, path: ".copilot/hooks/bunk.json"},
		{name: "copilot in COPILOT_HOME", agent: "copilot", env: map[string]string{"COPILOT_HOME": "conf/copilot"},
			files: map[string]string{"conf/copilot/hooks/bunk.json": snippet("copilot")}, state: HooksInstalled, path: "conf/copilot/hooks/bunk.json"},
		{name: "gemini", agent: "gemini", files: map[string]string{".gemini/settings.json": withSettings(t, strings.Replace(snippet("gemini"), `"AfterAgent"`, `"Stop"`, 1))},
			state: HooksOutdated, path: ".gemini/settings.json", absent: []string{"AfterAgent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for rel, data := range tc.files {
				writeFile(t, filepath.Join(home, rel), data)
			}
			d := Dirs{Home: home, Getenv: func(k string) string {
				if v, ok := tc.env[k]; ok {
					return filepath.Join(home, v)
				}
				return ""
			}}
			h, ok := CheckHook(d, tc.agent)
			if !ok {
				t.Fatal("no recipe")
			}
			if h.State != tc.state || h.Path != filepath.Join(home, tc.path) || (h.Problem != "") != tc.issue {
				t.Errorf("%+v; want state %v path %s problem %v", h, tc.state, tc.path, tc.issue)
			}
			if tc.absent != nil && !slices.Equal(h.Missing, tc.absent) {
				t.Errorf("missing %q, want %q", h.Missing, tc.absent)
			}
			if !h.Installable || h.Merges != (tc.agent == "claude" || tc.agent == "gemini") {
				t.Errorf("installable %v, merges %v", h.Installable, h.Merges)
			}
		})
	}
	if _, ok := CheckHook(Dirs{}, "emacs"); ok {
		t.Error("hooks of an unknown agent")
	}
}

// TestInstallHooks: the standalone snippets of Codex and Copilot CLI are
// written, replacing older bunk hooks; a Codex hooks.json with hooks of
// the user's own and the settings files of Claude Code and Gemini CLI get
// bunk's hooks merged in, with a backup; a Copilot file with the user's
// hooks and a file that is not JSON are left alone.
func TestInstallHooks(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d := Dirs{Home: home}
	for _, agent := range []string{"codex", "copilot", "claude", "gemini"} {
		done, err := InstallHooks(d, agent)
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		if h, _ := CheckHook(d, agent); h.State != HooksInstalled || h.Path != done.Path || done.Backup != "" {
			t.Errorf("%s after install: %+v, %+v", agent, h, done)
		}
		if fi, err := os.Stat(done.Path); err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, mode %v", done.Path, err, fi.Mode())
		}
	}

	codex := filepath.Join(home, ".codex", "hooks.json")
	writeFile(t, codex, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"bunk notify done"}]}]}}`)
	if _, err := InstallHooks(d, "codex"); err != nil {
		t.Fatalf("over older bunk hooks: %v", err)
	}
	if h, _ := CheckHook(d, "codex"); h.State != HooksInstalled {
		t.Errorf("over older bunk hooks: %+v", h)
	}

	mine := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"make lint"}]}]}}`
	writeFile(t, codex, mine)
	done, err := InstallHooks(d, "codex")
	if err != nil || done.Backup != codex+BackupSuffix {
		t.Fatalf("over the user's hooks: %+v, %v", done, err)
	}
	if data, _ := os.ReadFile(codex); !strings.Contains(string(data), "make lint") {
		t.Errorf("the user's hook went: %s", data)
	}
	if h, _ := CheckHook(d, "codex"); h.State != HooksInstalled {
		t.Errorf("merged codex: %+v", h)
	}

	copilot := filepath.Join(home, ".copilot", "hooks", "bunk.json")
	theirs := `{"version":1,"hooks":{"agentStop":[{"type":"command","bash":"make lint"}]}}`
	writeFile(t, copilot, theirs)
	if _, err := InstallHooks(d, "copilot"); err == nil || !strings.Contains(err.Error(), "other hooks") {
		t.Errorf("over the user's Copilot hooks: %v", err)
	}
	if data, _ := os.ReadFile(copilot); string(data) != theirs {
		t.Errorf("the user's Copilot hooks changed: %s", data)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, settings, "{")
	if _, err := InstallHooks(d, "claude"); err == nil {
		t.Error("over a file that is not JSON")
	}
	if data, _ := os.ReadFile(settings); string(data) != "{" {
		t.Errorf("a broken settings.json changed: %q", data)
	}
}

// TestMergeSettingsHooks: merging keeps every other key, event, group and
// handler in order and the file's indentation, replaces bunk's hooks of
// any version, and changes nothing a second time.
func TestMergeSettingsHooks(t *testing.T) {
	t.Parallel()
	snippet, _ := HookSnippet("claude")
	old := `{
    "model": "opus",
    "hooks": {
        "PreToolUse": [
            {"matcher": "Bash", "hooks": [
                {"type": "command", "command": "guard.sh"},
                {"type": "command", "command": "bunk notify needs-you -"}
            ]}
        ],
        "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "bunk notify working"}]}],
        "Custom": [{"hooks": [{"type": "command", "command": "echo hi"}]}]
    },
    "permissions": {"allow": ["Bash(ls:*)"], "n": 1.50}
}
`
	got, err := mergeSettingsHooks([]byte(old), snippet)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{`"guard.sh"`, `"echo hi"`, `"Bash(ls:*)"`, `1.50`, "\n    \"model\": \"opus\""} {
		if !strings.Contains(text, want) {
			t.Errorf("merged file lacks %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"bunk notify working"`) || strings.Contains(text, `"bunk notify needs-you -"`) {
		t.Errorf("old bunk hooks stayed:\n%s", text)
	}
	if m, h, p := strings.Index(text, `"model"`), strings.Index(text, `"hooks"`), strings.Index(text, `"permissions"`); m > h || h > p {
		t.Errorf("keys out of order:\n%s", text)
	}
	if g, c := strings.Index(text, `"guard.sh"`), strings.Index(text, `"Custom"`); g > c || c > strings.Index(text, `"Stop"`) {
		t.Errorf("the user's hooks moved behind bunk's:\n%s", text)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"), text)
	if h, _ := CheckHook(Dirs{Home: dir}, "claude"); h.State != HooksInstalled {
		t.Errorf("merged settings: %+v", h)
	}
	again, err := mergeSettingsHooks(got, snippet)
	if err != nil || string(again) != text {
		t.Errorf("a second merge changed the file: %v\n%s", err, again)
	}
	for _, bad := range []string{"[]", `{"hooks": []}`, `{"hooks": {"Stop": {}}}`, `{} x`} {
		if _, err := mergeSettingsHooks([]byte(bad), snippet); err == nil {
			t.Errorf("merged into %s", bad)
		}
	}
	if got, err := mergeSettingsHooks(nil, snippet); err != nil || !strings.Contains(string(got), "notify done -") {
		t.Errorf("into a missing file: %v\n%s", err, got)
	}
}

// TestInstallHooksFollowsLink: a settings.json that is a symbolic link,
// as dotfile managers keep it, stays a link; its target gets the hooks.
func TestInstallHooksFollowsLink(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, target, `{"theme": "dark"}`)
	link := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	done, err := InstallHooks(Dirs{Home: home}, "claude")
	if err != nil || done.Backup != target+BackupSuffix {
		t.Fatalf("%+v, %v", done, err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), `"theme": "dark"`) || !strings.Contains(string(data), "notify") {
		t.Errorf("target:\n%s", data)
	}
	if data, _ := os.ReadFile(done.Backup); string(data) != `{"theme": "dark"}` {
		t.Errorf("backup %q", data)
	}
}

// TestHooksCLI runs "notify hooks" and "notify hooks install" against a
// home directory of their own.
func TestHooksCLI(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + t.TempDir()}
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runCLI("bunk", args, env, nil, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	code, out, _ := run("hooks")
	for _, want := range []string{
		"codex    missing    ~/.codex/hooks.json (codex is not on PATH)\n",
		"bunk notify hooks install codex, then trust the hooks with /hooks in Codex\n",
		`run "bunk notify hooks install claude", which merges the hooks into it`,
		"docs/agent-hooks.md",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("hooks: exit %d, %q lacks %q", code, out, want)
		}
	}
	if code, out, _ := run("hooks", "install", "codex"); code != 0 || out != "Installed ~/.codex/hooks.json. Run /hooks in Codex once to trust the hooks.\n" {
		t.Errorf("install codex: exit %d, %q", code, out)
	}
	if _, out, _ := run("hooks"); !strings.Contains(out, "codex    installed  ~/.codex/hooks.json") {
		t.Errorf("after install: %q", out)
	}
	if code, out, errOut := run("hooks", "install", "claude"); code != 0 || out != "Installed ~/.claude/settings.json.\n" {
		t.Errorf("install claude: exit %d, %q %q", code, out, errOut)
	}
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model": "opus"}`)
	if code, out, _ := run("hooks", "install", "claude"); code != 0 || !strings.Contains(out, "The old file is in ~/.claude/settings.json"+BackupSuffix) {
		t.Errorf("install claude beside other settings: exit %d, %q", code, out)
	}
	if code, _, errOut := run("hooks", "install", "emacs"); code != 1 || !strings.Contains(errOut, "copilot") {
		t.Errorf("install emacs: exit %d, %q", code, errOut)
	}
}
