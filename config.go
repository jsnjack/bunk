// config.go – configuration loading and theme registry.
//
// Config file location: ~/.config/bunk/config.toml (XDG_CONFIG_HOME respected).
//
// Built-in themes:
//
//	default       – yauhen.cc Tilix palette (dark, cyan accent)
//	solarized-dark – Solarized Dark by Ethan Schoonover
//	dracula       – Dracula by Zeno Rocha
//	nord          – Nord by Arctic Ice Studio
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------------
// TOML file structure
// ---------------------------------------------------------------------------

type fileConfig struct {
	Theme      string     `toml:"theme"`
	LogFile    string     `toml:"log_file"`
	LogLevel   string     `toml:"log_level"`
	CellAspect float64    `toml:"cell_aspect"` // cell pixel H/W ratio; 0 = auto-detect
	UI         uiOverride `toml:"ui"`
}

type uiOverride struct {
	ActiveBorder   string `toml:"active_border"`
	InactiveBorder string `toml:"inactive_border"`
	ScrollThumb    string `toml:"scrollbar_thumb"`
	ScrollTrack    string `toml:"scrollbar_track"`
}

// ---------------------------------------------------------------------------
// Theme definition (hex strings)
// ---------------------------------------------------------------------------

// ThemeDef holds a complete named theme as hex color strings.
type ThemeDef struct {
	Background     string
	Foreground     string
	Palette        [16]string // ANSI colors 0–15
	ActiveBorder   string     // accent for the focused pane border
	InactiveBorder string
	ScrollThumb    string
	ScrollTrack    string
}

// resolvedTheme holds the same colors pre-parsed as tcell.Color values.
// Created once at startup; passed through render functions by value.
type resolvedTheme struct {
	bg, fg         tcell.Color
	palette        [16]tcell.Color
	activeBorder   tcell.Color
	inactiveBorder tcell.Color
	scrollThumb    tcell.Color
	scrollTrack    tcell.Color
}

// Config is the fully resolved application configuration.
type Config struct {
	Theme      resolvedTheme
	LogFile    string
	LogLevel   string
	CellAspect float64 // 0 = auto-detect via TIOCGWINSZ
}

// ---------------------------------------------------------------------------
// Built-in theme registry
// ---------------------------------------------------------------------------

// BuiltinThemes is the registry of shipped themes.
// Keys are the names accepted by --theme and the config "theme" field.
var BuiltinThemes = map[string]ThemeDef{
	// Default: the author's personal Tilix palette from yauhen.cc/posts/my-tillix
	"default": {
		Background: "#1C1C1F",
		Foreground: "#FFFFFF",
		Palette: [16]string{
			"#241F31", "#C01C28", "#2EC27E", "#F5C211",
			"#1E78E4", "#9841BB", "#0AB9DC", "#C0BFBC",
			"#5E5C64", "#ED333B", "#57E389", "#F8E45C",
			"#51A1FF", "#C061CB", "#4FD2FD", "#F6F5F4",
		},
		ActiveBorder:   "#4FD2FD", // bright cyan
		InactiveBorder: "#5E5C64",
		ScrollThumb:    "#C0BFBC",
		ScrollTrack:    "#3D3D40",
	},

	// Solarized Dark – Ethan Schoonover
	"solarized-dark": {
		Background: "#002B36",
		Foreground: "#839496",
		Palette: [16]string{
			"#073642", "#DC322F", "#859900", "#B58900",
			"#268BD2", "#D33682", "#2AA198", "#EEE8D5",
			"#002B36", "#CB4B16", "#586E75", "#657B83",
			"#839496", "#6C71C4", "#93A1A1", "#FDF6E3",
		},
		ActiveBorder:   "#268BD2", // blue
		InactiveBorder: "#073642",
		ScrollThumb:    "#657B83",
		ScrollTrack:    "#073642",
	},

	// Dracula – Zeno Rocha
	"dracula": {
		Background: "#282A36",
		Foreground: "#F8F8F2",
		Palette: [16]string{
			"#21222C", "#FF5555", "#50FA7B", "#F1FA8C",
			"#BD93F9", "#FF79C6", "#8BE9FD", "#F8F8F2",
			"#6272A4", "#FF6E6E", "#69FF94", "#FFFFA5",
			"#D6ACFF", "#FF92DF", "#A4FFFF", "#FFFFFF",
		},
		ActiveBorder:   "#BD93F9", // purple
		InactiveBorder: "#44475A",
		ScrollThumb:    "#6272A4",
		ScrollTrack:    "#21222C",
	},

	// Nord – Arctic Ice Studio
	"nord": {
		Background: "#2E3440",
		Foreground: "#D8DEE9",
		Palette: [16]string{
			"#3B4252", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#88C0D0", "#E5E9F0",
			"#4C566A", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#8FBCBB", "#ECEFF4",
		},
		ActiveBorder:   "#88C0D0", // frost blue
		InactiveBorder: "#4C566A",
		ScrollThumb:    "#D8DEE9",
		ScrollTrack:    "#3B4252",
	},
}

// ---------------------------------------------------------------------------
// Loader
// ---------------------------------------------------------------------------

// DefaultConfigPath returns the XDG-compliant config file path.
func DefaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "bunk", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bunk", "config.toml")
}

// LoadConfig reads the TOML config at path (empty = default), applies the
// optional themeOverride, and returns a resolved Config ready for use.
// Missing or unreadable config files are silently ignored.
func LoadConfig(path, themeOverride string) Config {
	if path == "" {
		path = DefaultConfigPath()
	}

	fc := fileConfig{
		Theme:    "default",
		LogFile:  "/tmp/bunk.log",
		LogLevel: "info",
	}
	if _, err := os.Stat(path); err == nil {
		toml.DecodeFile(path, &fc) //nolint:errcheck
	}

	if themeOverride != "" {
		fc.Theme = themeOverride
	}

	def, ok := BuiltinThemes[fc.Theme]
	if !ok {
		def = BuiltinThemes["default"]
	}

	// Apply per-field [ui] overrides.
	if fc.UI.ActiveBorder != "" {
		def.ActiveBorder = fc.UI.ActiveBorder
	}
	if fc.UI.InactiveBorder != "" {
		def.InactiveBorder = fc.UI.InactiveBorder
	}
	if fc.UI.ScrollThumb != "" {
		def.ScrollThumb = fc.UI.ScrollThumb
	}
	if fc.UI.ScrollTrack != "" {
		def.ScrollTrack = fc.UI.ScrollTrack
	}

	return Config{
		Theme:      resolveTheme(def),
		LogFile:    fc.LogFile,
		LogLevel:   fc.LogLevel,
		CellAspect: fc.CellAspect,
	}
}

// resolveTheme parses all hex strings in a ThemeDef into tcell.Colors.
func resolveTheme(d ThemeDef) resolvedTheme {
	rt := resolvedTheme{
		bg:             hexColor(d.Background),
		fg:             hexColor(d.Foreground),
		activeBorder:   hexColor(d.ActiveBorder),
		inactiveBorder: hexColor(d.InactiveBorder),
		scrollThumb:    hexColor(d.ScrollThumb),
		scrollTrack:    hexColor(d.ScrollTrack),
	}
	for i, s := range d.Palette {
		rt.palette[i] = hexColor(s)
	}
	return rt
}

// hexColor parses a "#RRGGBB" hex string into a tcell.Color.
// Returns tcell.ColorDefault on any parse error.
func hexColor(s string) tcell.Color {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return tcell.ColorDefault
	}
	r, e1 := strconv.ParseInt(s[0:2], 16, 32)
	g, e2 := strconv.ParseInt(s[2:4], 16, 32)
	b, e3 := strconv.ParseInt(s[4:6], 16, 32)
	if e1 != nil || e2 != nil || e3 != nil {
		return tcell.ColorDefault
	}
	return tcell.NewRGBColor(int32(r), int32(g), int32(b))
}

// ---------------------------------------------------------------------------
// Default config template
// ---------------------------------------------------------------------------

// DefaultConfigTOML returns a well-commented default config.toml as a string.
func DefaultConfigTOML() string {
	return fmt.Sprintf(`# bunk configuration
# %s

# Built-in themes: default, solarized-dark, dracula, nord
theme = "default"

# Logging.  Set log_file = "" to disable logging entirely.
log_file  = "/tmp/bunk.log"
log_level = "info"  # debug | info | warn | error

# Cell pixel aspect ratio (cell height / cell width).
# Used to decide split direction (vertical vs horizontal) based on real pixels.
# Default 2.25 suits Noto Sans Mono 11pt on Ptyxis; adjust for your font.
# Examples: JetBrains Mono 12pt ≈ 2.2  |  Fira Code 11pt ≈ 2.15
#
# cell_aspect = 2.25   # uncomment and adjust if splits feel wrong

# Optional UI color overrides – leave blank to use the theme's defaults.
# Values must be "#RRGGBB" hex strings.
[ui]
active_border   = ""  # border colour for the focused pane
inactive_border = ""  # border colour for unfocused panes
scrollbar_thumb = ""  # scrollbar handle
scrollbar_track = ""  # scrollbar background
`, DefaultConfigPath())
}
