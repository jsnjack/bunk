package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bunk/internal/session"
)

// TestGUIPreferences drives the preferences window with real clicks and
// keys: the rail and the peek's delay, layout, which leaves the rail's
// rows unusable, font size, the font family from the font dialog, default
// text zoom, adding, editing and removing [[agents]] entries, session
// restore and where new tabs start. Every change applies live: turning
// restore on gives the window, started without it, a slot. A relative
// folder is refused, and a folder that does not exist is saved with a
// warning. config.toml keeps the user's comments and order byte for byte.
// Open in editor closes the window and opens config.toml; Keyboard
// shortcuts swaps it for the shortcuts window.
func TestGUIPreferences(t *testing.T) {
	t.Parallel()
	const cfg = `# bunk-gui test settings
tabs = "rail"   # side
font_size = 11
restore = false

[keys]
new_tab = "ctrl+alt+n"   # mine

# agents I use
[[agents]]
name = "aider"   # the pair programmer
processes = ["aider"]

[[agents]]
name = "goose"
processes = ["goose"]
scripts = ["goose/cli.py"]
`
	g := runGUI(t, scenario{
		cost:   2,
		config: cfg,
		args:   []string{"--size", "80x24"},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800",
			"kbd ctrl+comma",
			"wait prefs!=null",
			"snap open",
			"ptr-at pref rail-open", "ptr-click",
			"wait rail_open=true",
			"ptr-at pref peek-delay", "ptr-click", "kbd ctrl+a", "kbd-type 150\\r",
			"wait settings.rail_peek_delay=150 prefs.peek_delay=150",
			"ptr-at pref tabs-bar", "ptr-click",
			"wait layout=bar prefs.rail_sensitive=false",
			"snap bar",
			"pref-scroll font-size",
			"ptr-at pref font-size", "ptr-click", "kbd ctrl+a", "kbd-type 12.5\\r",
			"wait prefs.font_size=12.5 font_size=12.5",
			// The font dialog lists the monospace families: type one, step
			// into the list, select it and confirm.
			"pref-scroll font",
			"ptr-at pref font", "ptr-click",
			"wait prefs.font_dialog=true idle 300ms",
			"kbd-type DejaVu Sans Mono",
			"wait idle 300ms",
			"kbd Down", "kbd space", "kbd Return",
			"wait prefs.font_dialog=false font=\"DejaVu Sans Mono 12.5\" prefs.font=\"DejaVu Sans Mono\"",
			"pref-scroll text-zoom",
			"ptr-at pref text-zoom", "ptr-click", "kbd ctrl+a", "kbd-type 120\\r",
			"wait text_zoom=120 text_percent=120",
			"snap text",
			"pref-scroll agent-0",
			"ptr-at pref agent-0", "ptr-click",
			"wait prefs.controls.agent-0-processes!=null prefs.animating=false",
			"pref-scroll agent-0-processes",
			"ptr-at pref agent-0-processes", "ptr-click", "kbd ctrl+a", "kbd-type aider, aider-chat\\r",
			"wait agents.0.processes.#=2",
			"ptr-at pref agent-1-remove", "ptr-click",
			"wait prefs.agents.#=1 agents.#=1",
			"pref-scroll agent-add",
			"ptr-at pref agent-add", "ptr-click",
			"wait prefs.agents.#=2 prefs.controls.agent-1-name!=null prefs.animating=false",
			"pref-scroll agent-1-name",
			"ptr-at pref agent-1-name", "ptr-click", "kbd-type crush\\r",
			"wait prefs.message=\"An agent needs a name and processes or scripts.\"",
			"snap incomplete",
			"ptr-at pref agent-1-processes", "ptr-click", "kbd-type crush\\r",
			"wait agents.#=2 agents.1.name=crush prefs.message=\"\"",
			"snap agents",
			"pref-scroll restore",
			"ptr-at pref restore", "ptr-click",
			"wait settings.restore=true restore.slot!=\"\" prefs.restore_scrollback=true",
			"ptr-at pref restore-scrollback", "ptr-click",
			"wait settings.restore_scrollback=false",
			"pref-scroll dir-current",
			"ptr-at pref dir-current", "ptr-click",
			"wait settings.new_dir=current prefs.new_dir=current",
			"ptr-at pref dir", "ptr-click", "kbd-type work\\r",
			"wait prefs.message=\"A folder is an absolute path, or starts with ~/.\"",
			"snap relative",
			"kbd ctrl+a", "kbd-type ~/no-such-bunk-folder\\r",
			"wait settings.new_dir=\"~/no-such-bunk-folder\" prefs.new_dir=\"~/no-such-bunk-folder\"",
			"snap missing",
			"kbd ctrl+a", "kbd-type /\\r",
			"wait settings.new_dir=/ prefs.message=\"\"",
			"snap more",
			"pref-scroll open-editor",
			"ptr-at pref open-editor", "ptr-click",
			"wait prefs=null run.opened.#=1",
			"kbd ctrl+comma",
			"wait prefs!=null",
			"pref-scroll shortcuts",
			"ptr-at pref shortcuts", "ptr-click",
			"wait prefs=null shortcuts!=null",
			"snap shortcuts",
			"dialog-close",
			"wait shortcuts=null",
			"snap closed",
			"quit",
		},
	})
	open := g.snap("open")
	if p := open.Prefs; p == nil || p.Tabs != "rail" || p.RailOpen || p.Font != "Noto Sans Mono" || p.FontSize != 11 || p.TextZoom != 100 ||
		!slices.Equal(p.Agents, [][3]string{{"aider", "aider", ""}, {"goose", "goose", "goose/cli.py"}}) {
		t.Fatalf("Ctrl+, opened %+v", open.Prefs)
	}
	if s := g.snap("bar"); s.Layout != "bar" || s.Prefs.RailSensitive || !g.snap("open").Prefs.RailSensitive {
		t.Errorf("Tabs on top: layout %q, rail rows usable %v, before %v", s.Layout, s.Prefs.RailSensitive, g.snap("open").Prefs.RailSensitive)
	}
	if s := g.snap("text"); s.Font != "DejaVu Sans Mono 15" || s.FontSize != 15 || s.TextPercent != 120 || s.TextZoom != 120 || !s.RailOpen {
		t.Errorf("font, size 12.5 and zoom 120 %%: font %q, %v pt, %d %%, default %v %%, rail open %v",
			s.Font, s.FontSize, s.TextPercent, s.TextZoom, s.RailOpen)
	}
	if s := g.snap("incomplete"); s.Prefs.Message != "An agent needs a name and processes or scripts." {
		t.Errorf("a name alone: message %q", s.Prefs.Message)
	}
	want := []session.AgentSpec{{Name: "aider", Processes: []string{"aider", "aider-chat"}}, {Name: "crush", Processes: []string{"crush"}}}
	if s := g.snap("agents"); !agentsEqual(s.Agents, want) {
		t.Errorf("agents in force: %+v, want %+v", s.Agents, want)
	}
	if s := g.snap("open"); s.Restore.Slot != "" || s.Settings["restore"] != false || s.Settings["rail_peek_delay"] != 200.0 ||
		s.Prefs.Restore || !s.Prefs.RestoreScrollback || s.Prefs.NewDir != "~" || s.Prefs.PeekDelay != 200 {
		t.Errorf("open with restore = false: slot %q, settings %v, prefs %+v", s.Restore.Slot, s.Settings, s.Prefs)
	}
	if s := g.snap("relative"); s.Settings["new_dir"] != "current" || s.Prefs.NewDir != "current" {
		t.Errorf("a relative folder: new_dir %v, chosen %q; want current kept", s.Settings["new_dir"], s.Prefs.NewDir)
	}
	if s := g.snap("missing"); !strings.Contains(s.Prefs.Message, "is not a folder") {
		t.Errorf("a folder that does not exist: message %q", s.Prefs.Message)
	}
	if s := g.snap("more"); s.Restore.Slot == "" || s.Settings["restore_scrollback"] != false || s.Settings["new_dir"] != "/" {
		t.Errorf("restore, scrollback and new_dir: slot %q, settings %v", s.Restore.Slot, s.Settings)
	}
	if want := "file://" + filepath.Join(g.dir, "config.toml"); !slices.Equal(g.Opened, []string{want}) {
		t.Errorf("Open in editor opened %q, want %q", g.Opened, want)
	}
	if s := g.snap("shortcuts"); s.Prefs != nil || s.Shortcuts == nil {
		t.Errorf("Keyboard shortcuts: prefs %v, shortcuts window %v", s.Prefs != nil, s.Shortcuts != nil)
	}
	if s := g.snap("closed"); s.Prefs != nil || s.Shortcuts != nil || s.ConfigError != "" {
		t.Errorf("closed: prefs %+v, config error %q", s.Prefs, s.ConfigError)
	}
	got, err := os.ReadFile(filepath.Join(g.dir, "config.toml"))
	must(t, err)
	wantFile := `# bunk-gui test settings
tabs = "bar"   # side
font_size = 12.5
restore = true
rail_open = true
rail_peek_delay = 150
font = "DejaVu Sans Mono"
text_zoom = 120
restore_scrollback = false
new_dir = "/"

[keys]
new_tab = "ctrl+alt+n"   # mine

# agents I use
[[agents]]
name = "aider"   # the pair programmer
processes = ["aider", "aider-chat"]

[[agents]]
name = "crush"
processes = ["crush"]
`
	if string(got) != wantFile {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, wantFile)
	}
}

func agentsEqual(a, b []session.AgentSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !slices.Equal(a[i].Processes, b[i].Processes) || len(a[i].Scripts)+len(b[i].Scripts) > 0 &&
			!slices.Equal(a[i].Scripts, b[i].Scripts) {
			return false
		}
	}
	return true
}
