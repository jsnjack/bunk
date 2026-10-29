package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"bunk/internal/alert"
	"bunk/internal/session"
	"bunk/internal/theme"
)

// Settings are bunk-gui's user settings, read from an optional TOML file
// (~/.config/bunk-gui/config.toml). Every key is optional:
//
//	tabs = "rail"            # "rail" (side) or "bar" (top)
//	rail_open = false        # start with the rail expanded
//	rail_style = "ring"      # tabs in the collapsed rail: "ring" or "map" (railring.go)
//	font = "Noto Sans Mono"  # terminal font family
//	font_size = 11           # points
//	text_zoom = 100          # text size at start and after Ctrl+0, % of font_size
//	theme = "default"        # a built-in theme or a [themes.NAME] table (internal/theme)
//	hooks_hint = true        # a toast when an agent runs without bunk's hooks
//
//	[themes.paper]           # a custom theme, as in bunk's config (theme.Def)
//	background = "#FDF6E3"
//	foreground = "#586E75"
//	palette = ["#073642", …] # 16 colours
//
//	[keys]                   # key bindings (see bindings.go for the actions)
//	next_tab = ["ctrl+right", "ctrl+pgdn"]
//	copy = "ctrl+shift+c"    # Ctrl+C always goes to the terminal
//	zoom = "none"            # "none" or "": the key goes to the terminal
//
//	[[agents]]               # extra agent CLIs to recognise (repeatable)
//	name = "myagent"
//	processes = ["myagent"]
//	scripts = ["@me/myagent"]
//
//	[notifications]          # desktop notifications for tabs you are not looking at
//	enabled = true
//	states = ["needs-you", "done", "failed"]
//	command_after = 10       # a command runs this many seconds: marked, notifies at its end; 0 = never
//
// The file is watched: edits apply to the running window.
type Settings struct {
	Tabs     string `toml:"tabs"`
	RailOpen bool   `toml:"rail_open"`
	// RailStyle draws the tabs in the collapsed rail: "ring", a ring of
	// their panes' states (railring.go), or "map", the state glyph, and
	// for 2 to 4 panes their dots placed like the panes (railMap).
	RailStyle string               `toml:"rail_style"`
	Font      string               `toml:"font"`
	FontSize  float64              `toml:"font_size"`
	TextZoom  float64              `toml:"text_zoom"`
	Theme     string               `toml:"theme"`
	Themes    map[string]theme.Def `toml:"themes"`
	Agents    []session.AgentSpec  `toml:"agents"`
	Keys      map[string]any       `toml:"keys"`
	// Notifications is the [notifications] table as written.
	Notifications alert.Config `toml:"notifications"`
	// HooksHint is hooks_hint as written: remind the user of an agent that
	// runs without bunk's hooks (prefs_hooks.go).
	HooksHint *bool `toml:"hooks_hint"`

	keymap    keymap              // Keys applied to the defaults
	was       map[string][]string // bindings of disabled actions, from "# was" comments
	themes    *theme.Registry     // the built-in themes and the valid ones of Themes
	alerts    alert.Settings      // Notifications applied to the defaults
	hooksHint bool                // HooksHint applied to the default, true
}

// startSize is the text size at start and after Ctrl+0: font_size at
// text_zoom percent.
func (st Settings) startSize() float64 {
	return math.Round(st.FontSize*st.TextZoom/10) / 10
}

func defaultSettings() Settings {
	km, _ := resolveKeys(nil)
	return Settings{Tabs: "rail", RailStyle: "ring", Font: "Noto Sans Mono", FontSize: 11, TextZoom: 100, Theme: theme.Default,
		keymap: km, was: map[string][]string{}, alerts: alert.Default(), hooksHint: true}
}

// configPath is $XDG_CONFIG_HOME/bunk-gui/config.toml.
func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "bunk-gui", "config.toml")
}

// loadSettings reads path. A missing file gives the defaults and no error;
// a malformed one gives the defaults and an error to show, never a crash.
func loadSettings(path string) (Settings, error) {
	st := defaultSettings()
	if path == "" {
		return st, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read settings: %w", err)
	}
	var f Settings
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return st, fmt.Errorf("%s: %w", path, err)
	}
	var problems []string
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		problems = append(problems, "unknown keys: "+strings.Join(keys, ", "))
	}
	if md.IsDefined("tabs") {
		if f.Tabs == "rail" || f.Tabs == "bar" {
			st.Tabs = f.Tabs
		} else {
			problems = append(problems, fmt.Sprintf("tabs = %q (want \"rail\" or \"bar\")", f.Tabs))
		}
	}
	if md.IsDefined("rail_style") {
		if f.RailStyle == "ring" || f.RailStyle == "map" {
			st.RailStyle = f.RailStyle
		} else {
			problems = append(problems, fmt.Sprintf("rail_style = %q (want \"ring\" or \"map\")", f.RailStyle))
		}
	}
	if md.IsDefined("rail_open") {
		st.RailOpen = f.RailOpen
	}
	if f.HooksHint != nil {
		st.HooksHint, st.hooksHint = f.HooksHint, *f.HooksHint
	}
	if f.Font != "" {
		st.Font = f.Font
	}
	if md.IsDefined("font_size") {
		if f.FontSize >= 4 && f.FontSize <= 96 {
			st.FontSize = f.FontSize
		} else {
			problems = append(problems, fmt.Sprintf("font_size = %v (want 4-96)", f.FontSize))
		}
	}
	if md.IsDefined("text_zoom") {
		if f.TextZoom >= 50 && f.TextZoom <= 200 {
			st.TextZoom = f.TextZoom
		} else {
			problems = append(problems, fmt.Sprintf("text_zoom = %v (want 50-200)", f.TextZoom))
		}
	}
	reg, err := theme.NewRegistry(f.Themes)
	if err != nil {
		problems = append(problems, err.Error())
	}
	st.Themes, st.themes = f.Themes, reg
	if md.IsDefined("theme") {
		if _, ok := reg.Lookup(f.Theme); ok {
			st.Theme = f.Theme
		} else {
			problems = append(problems, fmt.Sprintf("theme = %q (want one of %s)", f.Theme, strings.Join(reg.Names(), ", ")))
		}
	}
	km, keyProblems := resolveKeys(f.Keys)
	st.Keys, st.keymap, st.was = f.Keys, km, keysWas(string(data))
	problems = append(problems, keyProblems...)
	alerts, err := f.Notifications.Settings()
	if err != nil {
		problems = append(problems, err.Error())
	}
	st.Notifications, st.alerts = f.Notifications, alerts
	for i, a := range f.Agents {
		if a.Name == "" || len(a.Processes)+len(a.Scripts) == 0 {
			problems = append(problems, fmt.Sprintf("agents[%d] needs a name and processes or scripts", i))
			continue
		}
		st.Agents = append(st.Agents, a)
	}
	if len(problems) > 0 {
		return st, fmt.Errorf("%s: %s", path, strings.Join(problems, "; "))
	}
	return st, nil
}
