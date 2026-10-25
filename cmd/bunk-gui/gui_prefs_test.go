package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"bunk/internal/session"
)

// TestGUIPreferences drives the preferences window with real clicks and
// keys: layout, rail, font family and size, default text zoom, and adding,
// editing and removing [[agents]] entries. Every change applies live, and
// config.toml keeps the user's comments and order byte for byte.
func TestGUIPreferences(t *testing.T) {
	t.Parallel()
	const cfg = `# bunk-gui test settings
tabs = "rail"   # side
font_size = 11

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
		config: cfg,
		args:   []string{"-size", "80x24"},
		script: []string{
			"300ms maximize",
			"1200ms kbd ctrl+comma",
			"1600ms snap open",
			"1700ms ptr-at pref tabs-bar", "1800ms ptr-click",
			"2200ms snap bar",
			"2300ms ptr-at pref rail-open", "2400ms ptr-click",
			"2800ms ptr-at pref font-size -30 0", "2900ms ptr-click", "3000ms kbd ctrl+a", "3100ms kbd-type 12.5\\r",
			"3500ms ptr-at pref font", "3600ms ptr-click", "3700ms kbd ctrl+a", "3800ms kbd-type DejaVu Sans Mono\\r",
			"4300ms ptr-at pref text-zoom -30 0", "4400ms ptr-click", "4500ms kbd ctrl+a", "4600ms kbd-type 120\\r",
			"5000ms snap text",
			"5100ms ptr-at pref agent-0-processes", "5200ms ptr-click", "5300ms kbd ctrl+a", "5400ms kbd-type aider, aider-chat\\r",
			"5800ms ptr-at pref agent-1-remove", "5900ms ptr-click",
			"6300ms ptr-at pref agent-add", "6400ms ptr-click",
			"6700ms ptr-at pref agent-1-name", "6800ms ptr-click", "6900ms kbd-type crush\\r",
			"7100ms snap incomplete",
			"7200ms ptr-at pref agent-1-processes", "7300ms ptr-click", "7400ms kbd-type crush\\r",
			"7800ms snap agents",
			"7900ms dialog-close",
			"8200ms snap closed",
			"8300ms quit",
		},
	})
	open := g.snap("open")
	if p := open.Prefs; p == nil || p.Tabs != "rail" || p.RailOpen || p.Font != "Noto Sans Mono" || p.FontSize != 11 || p.TextZoom != 100 ||
		!slices.Equal(p.Agents, [][3]string{{"aider", "aider", ""}, {"goose", "goose", "goose/cli.py"}}) {
		t.Fatalf("Ctrl+, opened %+v", open.Prefs)
	}
	if s := g.snap("bar"); s.Layout != "bar" {
		t.Errorf("Tabs on top: layout %q", s.Layout)
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
	if s := g.snap("closed"); s.Prefs != nil || s.ConfigError != "" {
		t.Errorf("closed: prefs %+v, config error %q", s.Prefs, s.ConfigError)
	}
	got, err := os.ReadFile(filepath.Join(g.dir, "config.toml"))
	must(t, err)
	wantFile := `# bunk-gui test settings
tabs = "bar"   # side
font_size = 12.5
rail_open = true
font = "DejaVu Sans Mono"
text_zoom = 120

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
