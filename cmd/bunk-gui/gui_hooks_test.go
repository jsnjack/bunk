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
		if h.State != "missing" || !h.Installable || !strings.HasPrefix(h.Path, home) {
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

// TestGUIHooksHint: an agent whose hooks are missing brings the hooks
// banner once, for its first tab only, and Set up installs them. With the
// hooks installed, or with hooks_hint = false, no banner comes. The hint
// runs when the tab first shows the agent, so the snapshot right after
// that wait has the banner.
func TestGUIHooksHint(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeAgent(t, bin, "claude", "go")
	path := agentPath(t, bin)
	for _, tc := range []struct {
		name   string
		config string
		home   string // "" keeps the harness's, with every agent's hooks
		banner bool
	}{
		{name: "missing", home: t.TempDir(), banner: true},
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
				"wait focused.line=$ hooks.banner=false",
				"type claude\\r",
				"wait tab.1.status.agent=claude",
				"snap first",
				"kbd alt+2",
				"wait selected_tab=2 focused.line=$",
				"type claude\\r",
				"wait tab.2.status.agent=claude",
				"snap second",
			}
			if tc.banner {
				script = append(script, "hooks-banner accept", "wait hooks.banner=false hooks.result~Installed", "snap installed")
			}
			g := runGUI(t, scenario{cost: 2, config: tc.config, args: args, env: []string{path}, script: append(script, "quit")})
			for _, snap := range []string{"first", "second"} {
				h := g.snap(snap).Hooks
				if h.Banner != tc.banner || tc.banner && (!slices.Equal(h.Agents, []string{"claude"}) ||
					!strings.HasPrefix(h.Text, "Set up bunk's hooks for claude (not set up)?") ||
					!strings.Contains(h.Text, "~/.claude/settings.json, keeping a copy")) {
					t.Errorf("%s: hooks banner %+v", snap, h)
				}
			}
			if len(g.Toasts) != 0 && !tc.banner {
				t.Errorf("toasts %q", g.Toasts)
			}
			if tc.banner {
				if h, _ := notify.CheckHook(notify.Dirs{Home: tc.home}, "claude"); h.State != notify.HooksInstalled {
					t.Errorf("after Set up: %+v", h)
				}
				if !slices.Equal(g.Toasts, []string{"Hooks installed"}) {
					t.Errorf("toasts %q", g.Toasts)
				}
			}
		})
	}
}

// TestGUIHooksOutdatedAtStart: bunk hooks older than the current snippet
// bring the banner at start, before any agent runs; Update merges the new
// hooks into Claude Code's settings.json, keeping its other settings and a
// copy of the old file. Not now leaves the file alone.
func TestGUIHooksOutdatedAtStart(t *testing.T) {
	t.Parallel()
	old := `{
  "model": "opus",
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "bunk-gui notify working"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "afplay done.wav"}]}]
  }
}
`
	for _, accept := range []bool{true, false} {
		t.Run(strconv.FormatBool(accept), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			settings := filepath.Join(home, ".claude", "settings.json")
			must(t, os.MkdirAll(filepath.Dir(settings), 0o755))
			must(t, os.WriteFile(settings, []byte(old), 0o600))
			step := "hooks-banner dismiss"
			if accept {
				step = "hooks-banner accept"
			}
			g := runGUI(t, scenario{
				args:   []string{"--size", "100x30", "--agent-home", home},
				script: []string{"wait focused.line=$ hooks.banner=true", "snap offered", step, "wait hooks.banner=false", "quit"},
			})
			if h := g.snap("offered").Hooks; !slices.Equal(h.Agents, []string{"claude"}) || !strings.HasPrefix(h.Text, "Update bunk's hooks for claude (outdated)?") {
				t.Errorf("banner %+v", h)
			}
			got := readFile(t, settings)
			if !accept {
				if got != old {
					t.Errorf("Not now changed settings.json:\n%s", got)
				}
				return
			}
			if h, _ := notify.CheckHook(notify.Dirs{Home: home}, "claude"); h.State != notify.HooksInstalled {
				t.Errorf("after Update: %+v", h)
			}
			for _, want := range []string{`"model": "opus"`, `"afplay done.wav"`} {
				if !strings.Contains(got, want) {
					t.Errorf("settings.json lost %s:\n%s", want, got)
				}
			}
			if backup := readFile(t, settings+notify.BackupSuffix); backup != old {
				t.Errorf("backup:\n%s", backup)
			}
			if fi, err := os.Stat(settings); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("mode %v, %v; want the file's own 0600", fi.Mode(), err)
			}
		})
	}
}
