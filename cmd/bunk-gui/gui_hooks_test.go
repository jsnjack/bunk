package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"bunk/internal/notify"
)

// TestGUIHooksPrefs: the Agent hooks section of the preferences lists
// every agent without hooks in an empty home directory. Install writes
// Copilot CLI's file, Copy puts Claude Code's snippet on the clipboard
// with where it goes, the guide button opens the setup docs, and the
// switch turns the reminder off in config.toml.
func TestGUIHooksPrefs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	claude, _ := notify.HookSnippet("claude")
	copilot, _ := notify.HookSnippet("copilot")
	g := runGUI(t, scenario{
		cost:   3,
		config: "# mine\n",
		args:   []string{"--size", "100x30", "--agent-home", home},
		script: []string{
			"maximize", "wait window.maximized=true window.w>1800",
			"kbd ctrl+comma",
			"wait prefs!=null prefs.hooks.#=4",
			"snap before",
			"pref-scroll hooks-copilot-install",
			"ptr-at pref hooks-copilot-install", "ptr-click",
			"wait prefs.hooks.2.state=installed prefs.hooks_note~Installed",
			"snap installed",
			"pref-scroll hooks-claude-copy",
			"ptr-at pref hooks-claude-copy", "ptr-click",
			"wait prefs.hooks_note~\"Copied the claude snippet\" clipboard " + strconv.Quote(claude),
			"snap copied",
			"pref-scroll hooks-guide",
			"ptr-at pref hooks-guide", "ptr-click",
			"wait run.opened.#=1",
			"pref-scroll hooks-hint",
			"ptr-at pref hooks-hint", "ptr-click",
			"wait prefs.hooks_hint=false run.reloads>=1",
			"dialog-close",
			"wait prefs=null",
			"quit",
		},
	})
	before := g.snap("before").Prefs
	if before == nil || !before.HooksHint {
		t.Fatalf("preferences opened with %+v", before)
	}
	var agents []string
	for _, h := range before.Hooks {
		agents = append(agents, h.Agent)
		if h.State != "missing" || h.Installable != (h.Agent == "codex" || h.Agent == "copilot") || !strings.HasPrefix(h.Path, home) {
			t.Errorf("before: %+v", h)
		}
	}
	if !slices.Equal(agents, []string{"claude", "codex", "copilot", "gemini"}) {
		t.Errorf("agents %q", agents)
	}
	if note := g.snap("installed").Prefs.HooksNote; note != "Installed ~/.copilot/hooks/bunk.json." {
		t.Errorf("install note %q", note)
	}
	if data, err := os.ReadFile(filepath.Join(home, ".copilot", "hooks", "bunk.json")); err != nil || string(data) != copilot {
		t.Errorf("copilot hooks file: %v\n%s", err, data)
	}
	if note := g.snap("copied").Prefs.HooksNote; !strings.Contains(note, `merge its "hooks" key into ~/.claude/settings.json`) {
		t.Errorf("copy note %q", note)
	}
	if !slices.Equal(g.Opened, []string{hooksDocsURL}) {
		t.Errorf("opened %q, want the setup guide", g.Opened)
	}
	if got := readFile(t, g.file("config.toml")); got != "hooks_hint = false\n\n# mine\n" {
		t.Errorf("config.toml:\n%s", got)
	}
}

// TestGUIHooksHint: an agent whose hooks are missing brings one toast, for
// its first tab only; its button opens the preferences. With the hooks
// installed, or with hooks_hint = false, no toast comes. The hint runs
// when the tab first shows the agent, so the snapshot right after that
// wait has every toast.
func TestGUIHooksHint(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeAgent(t, bin, "claude", "go")
	path := agentPath(t, bin)
	for _, tc := range []struct {
		name   string
		config string
		home   string // "" keeps the harness's, with every agent's hooks
		toast  bool
	}{
		{name: "missing", home: t.TempDir(), toast: true},
		{name: "installed"},
		{name: "turned off", config: "hooks_hint = false\n", home: t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{"--size", "100x30", "--tab", ""}
			if tc.home != "" {
				args = append(args, "--agent-home", tc.home)
			}
			script := []string{
				"wait focused.line=$",
				"type claude\\r",
				"wait tab.1.status.agent=claude",
				"snap first",
				"kbd alt+2",
				"wait selected_tab=2 focused.line=$",
				"type claude\\r",
				"wait tab.2.status.agent=claude",
				"snap second",
			}
			if tc.toast {
				script = append(script, "toast-action", "wait prefs!=null", "dialog-close", "wait prefs=null")
			}
			g := runGUI(t, scenario{cost: 2, config: tc.config, args: args, env: []string{path}, script: append(script, "quit")})
			const text = "claude runs without bunk's hooks, so its state is guessed from its output"
			want := []string{}
			if tc.toast {
				want = []string{text}
			}
			for _, snap := range []string{"first", "second"} {
				s := g.snap(snap)
				if tc.toast != (s.LastToast == text && s.ToastButton == "Set up") || (!tc.toast && s.LastToast != "") {
					t.Errorf("%s: toast %q with button %q", snap, s.LastToast, s.ToastButton)
				}
			}
			if !slices.Equal(g.Toasts, want) {
				t.Errorf("toasts %q, want %q", g.Toasts, want)
			}
		})
	}
}
