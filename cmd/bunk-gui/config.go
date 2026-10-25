package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"bunk/internal/session"
)

// Settings are bunk-gui's user settings, read from an optional TOML file
// (~/.config/bunk-gui/config.toml). Every key is optional:
//
//	tabs = "rail"            # "rail" (side) or "bar" (top)
//	rail_open = false        # start with the rail expanded
//	font = "Noto Sans Mono"  # terminal font family
//	font_size = 11           # points
//
//	[[agents]]               # extra agent CLIs to recognise (repeatable)
//	name = "myagent"
//	processes = ["myagent"]
//	scripts = ["@me/myagent"]
type Settings struct {
	Tabs     string              `toml:"tabs"`
	RailOpen bool                `toml:"rail_open"`
	Font     string              `toml:"font"`
	FontSize float64             `toml:"font_size"`
	Agents   []session.AgentSpec `toml:"agents"`
}

func defaultSettings() Settings {
	return Settings{Tabs: "rail", Font: "Noto Sans Mono", FontSize: 11}
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
		return st, err
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
	if md.IsDefined("rail_open") {
		st.RailOpen = f.RailOpen
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
