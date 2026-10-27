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
			if h.Installable != (tc.agent == "codex" || tc.agent == "copilot") {
				t.Errorf("installable %v", h.Installable)
			}
		})
	}
	if _, ok := CheckHook(Dirs{}, "emacs"); ok {
		t.Error("hooks of an unknown agent")
	}
}

// TestInstallHooks: the standalone snippets of Codex and Copilot CLI are
// written, replacing older bunk hooks; a file with hooks of the user's
// own and an agent whose hooks share a settings file are left alone.
func TestInstallHooks(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d := Dirs{Home: home}
	for _, agent := range []string{"codex", "copilot"} {
		path, err := InstallHooks(d, agent)
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		if h, _ := CheckHook(d, agent); h.State != HooksInstalled || h.Path != path {
			t.Errorf("%s after install: %+v", agent, h)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, mode %v", path, err, fi.Mode())
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
	if _, err := InstallHooks(d, "codex"); err == nil || !strings.Contains(err.Error(), "other hooks") {
		t.Errorf("over the user's hooks: %v", err)
	}
	if data, _ := os.ReadFile(codex); string(data) != mine {
		t.Errorf("the user's hooks.json changed: %s", data)
	}
	writeFile(t, codex, "{")
	if _, err := InstallHooks(d, "codex"); err == nil {
		t.Error("over a file that is not JSON")
	}
	for _, agent := range []string{"claude", "gemini"} {
		if _, err := InstallHooks(d, agent); err == nil || !strings.Contains(err.Error(), "merge") {
			t.Errorf("%s: %v", agent, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(home, ".codex")); len(entries) != 1 {
		t.Errorf("leftovers in ~/.codex: %v", entries)
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
		`merge the "hooks" key of "bunk notify hooks claude" into it`,
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
	if code, _, errOut := run("hooks", "install", "claude"); code != 1 || !strings.Contains(errOut, "~/.claude/settings.json") {
		t.Errorf("install claude: exit %d, %q", code, errOut)
	}
	if code, _, errOut := run("hooks", "install", "emacs"); code != 1 || !strings.Contains(errOut, "copilot") {
		t.Errorf("install emacs: exit %d, %q", code, errOut)
	}
}
