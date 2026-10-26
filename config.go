// config.go - configuration loading, the theme in tcell colours, and key
// bindings.
//
// Config file location: ~/.config/bunk/config.toml (XDG_CONFIG_HOME respected).
// The themes and their validation live in internal/theme, shared with
// bunk-gui.
package main

import (
	"bunk/internal/alert"
	"bunk/internal/session"
	"bunk/internal/theme"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------------
// TOML file structure
// ---------------------------------------------------------------------------

type fileConfig struct {
	Theme      string               `toml:"theme"`
	LogFile    string               `toml:"log_file"`
	LogLevel   string               `toml:"log_level"`
	CellAspect float64              `toml:"cell_aspect"` // cell pixel H/W ratio; 0 = auto-detect
	Scrollback int                  `toml:"scrollback"`  // max scrollback lines per pane; 0 = default
	UI         theme.UI             `toml:"ui"`
	Themes     map[string]theme.Def `toml:"themes"` // custom themes, [themes.NAME]
	Keys       map[string]string    `toml:"keys"`   // action → key string, e.g. "split" → "f1"
	// Notifications: OSC 777 notifications to the host terminal when an
	// agent in a pane the user is not looking at needs them.
	Notifications alert.Config `toml:"notifications"`
}

// keybindingNames maps lowercase TOML key names to their tcell.Key constant.
// Package-level so it is allocated once.
var keybindingNames = map[string]tcell.Key{
	"f1": tcell.KeyF1, "f2": tcell.KeyF2, "f3": tcell.KeyF3,
	"f4": tcell.KeyF4, "f5": tcell.KeyF5, "f6": tcell.KeyF6,
	"f7": tcell.KeyF7, "f8": tcell.KeyF8, "f9": tcell.KeyF9,
	"f10": tcell.KeyF10, "f11": tcell.KeyF11, "f12": tcell.KeyF12,
	"up": tcell.KeyUp, "down": tcell.KeyDown,
	"left": tcell.KeyLeft, "right": tcell.KeyRight,
	"pgup": tcell.KeyPgUp, "pageup": tcell.KeyPgUp,
	"pgdn": tcell.KeyPgDn, "pagedown": tcell.KeyPgDn,
	"home": tcell.KeyHome, "end": tcell.KeyEnd,
	"enter": tcell.KeyEnter, "return": tcell.KeyEnter,
	"escape": tcell.KeyEsc, "esc": tcell.KeyEsc,
	"backspace": tcell.KeyBackspace2,
	"delete":    tcell.KeyDelete, "del": tcell.KeyDelete,
	"tab":    tcell.KeyTab,
	"insert": tcell.KeyInsert,
}

// ---------------------------------------------------------------------------
// Keybinding type
// ---------------------------------------------------------------------------

// Keybinding represents one resolved key combination (key code + modifiers).
//
// For most special keys (F1, arrows, Esc, …) only key+mod matter.  For
// modifier+letter combinations the encoding depends on the modifier:
//
//   - Ctrl+letter is its own tcell.Key (KeyCtrlA … KeyCtrlZ) with mod=0.
//   - Alt+letter is delivered as KeyRune with the letter rune and ModAlt;
//     in that case `r` carries the letter so Matches can compare it.
type Keybinding struct {
	key  tcell.Key
	mod  tcell.ModMask
	r    rune   // only meaningful when key == tcell.KeyRune (e.g. alt+letter)
	raw  string // original string, for display
	none bool   // "none": the action has no key, so its key reaches the pane
}

// Matches returns true if ev matches this keybinding.
func (kb Keybinding) Matches(ev *tcell.EventKey) bool {
	if kb.none || ev.Key() != kb.key {
		return false
	}
	// For rune-keys (alt+letter), the actual letter must match.  We normalise
	// to lowercase so users typing "alt+r" config still match Alt+r typed at
	// the terminal regardless of caps-lock state.
	if kb.key == tcell.KeyRune {
		evRune := ev.Rune()
		if evRune >= 'A' && evRune <= 'Z' {
			evRune += 'a' - 'A'
		}
		if evRune != kb.r {
			return false
		}
	}
	evMod := ev.Modifiers()
	// ctrl+letter keys (ctrl+a … ctrl+z) have the Ctrl baked into the key
	// code by tcell; the ModCtrl bit may still be set in Modifiers() depending
	// on the platform/terminal.  Strip it before comparing so that the stored
	// mod=0 (set by parseKey) still matches.
	if kb.key >= tcell.KeyCtrlA && kb.key <= tcell.KeyCtrlZ {
		evMod &^= tcell.ModCtrl
	}
	return evMod == kb.mod
}

// String returns the human-readable key description.
func (kb Keybinding) String() string { return kb.raw }

// parseKey converts a key description string such as "f1", "ctrl+q",
// "alt+up", or "shift+pgup" into a Keybinding.
//
// Modifier prefixes (case-insensitive): ctrl, alt, shift.
// Key names (case-insensitive):
//
//	f1-f12, up, down, left, right, pgup/pageup, pgdn/pagedown,
//	home, end, enter/return, escape/esc, backspace, delete/del, tab, insert.
//
// Ctrl+letter combinations (e.g. ctrl+c) are represented as tcell.KeyCtrlC
// with no extra modifier bits, matching what tcell actually produces.
// "none" disables the action: it matches no key.
func parseKey(s string) (Keybinding, error) {
	orig := s
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "none" {
		return Keybinding{raw: orig, none: true}, nil
	}

	parts := strings.Split(s, "+")
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		return Keybinding{}, fmt.Errorf("empty key binding")
	}

	var mod tcell.ModMask
	keyName := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			mod |= tcell.ModCtrl
		case "alt":
			mod |= tcell.ModAlt
		case "shift":
			mod |= tcell.ModShift
		default:
			return Keybinding{}, fmt.Errorf("unknown modifier %q in keybinding %q", p, orig)
		}
	}

	// ctrl+<letter>: tcell represents these as KeyCtrlA-KeyCtrlZ with no
	// separate ModCtrl bit.
	if mod&tcell.ModCtrl != 0 && len(keyName) == 1 && keyName[0] >= 'a' && keyName[0] <= 'z' {
		key := tcell.Key(keyName[0]-'a') + tcell.KeyCtrlA
		return Keybinding{key: key, mod: mod &^ tcell.ModCtrl, raw: orig}, nil
	}

	// alt+<letter> (and bare letter, though we have no defaults using it):
	// tcell delivers these as KeyRune with the letter rune and the Alt bit
	// in Modifiers().
	if len(keyName) == 1 && keyName[0] >= 'a' && keyName[0] <= 'z' {
		return Keybinding{key: tcell.KeyRune, r: rune(keyName[0]), mod: mod, raw: orig}, nil
	}

	key, ok := keybindingNames[keyName]
	if !ok {
		return Keybinding{}, fmt.Errorf("unknown key name %q in keybinding %q", keyName, orig)
	}
	return Keybinding{key: key, mod: mod, raw: orig}, nil
}

// mustParseKey parses a key string and panics on error (used only for
// built-in defaults which are always valid).
func mustParseKey(s string) Keybinding {
	kb, err := parseKey(s)
	if err != nil {
		panic("bunk: bad built-in keybinding: " + err.Error())
	}
	return kb
}

// Keybindings holds one resolved Keybinding per named action.
type Keybindings struct {
	Split        Keybinding
	SplitContext Keybinding
	Quit         Keybinding
	Copy         Keybinding
	Paste        Keybinding
	Search       Keybinding
	Passthrough  Keybinding
	Zoom         Keybinding
	NavUp        Keybinding
	NavDown      Keybinding
	NavLeft      Keybinding
	NavRight     Keybinding
	ScrollUp     Keybinding
	ScrollDown   Keybinding
	SearchNext   Keybinding
	SearchPrev   Keybinding
	SearchExit   Keybinding
	ReinitHost   Keybinding
	NextNeedsYou Keybinding
}

// keybindingDefaults is the single source of truth for action names, their
// struct field pointers, default key strings, and help descriptions.
// Used by resolveKeybindings() and keybindingsHelpText() — add new actions here only.
type kbEntry struct {
	action string
	field  func(*Keybindings) *Keybinding
	def    string
	desc   string // shown in --help output
}

var keybindingDefaults = []kbEntry{
	{"passthrough", func(k *Keybindings) *Keybinding { return &k.Passthrough }, "ctrl+f12", "Toggle keyboard passthrough for the active pane (PASS badge)"},
	{"split", func(k *Keybindings) *Keybinding { return &k.Split }, "f1", "Auto-split the active pane (vertical if wide, horizontal if tall)"},
	{"split_context", func(k *Keybindings) *Keybinding { return &k.SplitContext }, "alt+f1", "Split inheriting container / SSH / sudo context"},
	{"zoom", func(k *Keybindings) *Keybinding { return &k.Zoom }, "f12", "Toggle fullscreen zoom on the active pane"},
	{"nav_up", func(k *Keybindings) *Keybinding { return &k.NavUp }, "alt+up", "Move focus to the pane above"},
	{"nav_down", func(k *Keybindings) *Keybinding { return &k.NavDown }, "alt+down", "Move focus to the pane below"},
	{"nav_left", func(k *Keybindings) *Keybinding { return &k.NavLeft }, "alt+left", "Move focus to the pane on the left"},
	{"nav_right", func(k *Keybindings) *Keybinding { return &k.NavRight }, "alt+right", "Move focus to the pane on the right"},
	{"next_needs_you", func(k *Keybindings) *Keybinding { return &k.NextNeedsYou }, "alt+a", "Focus the next pane whose agent needs you, else one that is done"},
	{"scroll_up", func(k *Keybindings) *Keybinding { return &k.ScrollUp }, "shift+pgup", "Scroll back through pane history"},
	{"scroll_down", func(k *Keybindings) *Keybinding { return &k.ScrollDown }, "shift+pgdn", "Scroll forward / return to live output"},
	{"search", func(k *Keybindings) *Keybinding { return &k.Search }, "ctrl+f", "Incremental search in the active pane"},
	{"search_next", func(k *Keybindings) *Keybinding { return &k.SearchNext }, "ctrl+n", "Next search match (also: Enter)"},
	{"search_prev", func(k *Keybindings) *Keybinding { return &k.SearchPrev }, "ctrl+p", "Previous search match"},
	{"search_exit", func(k *Keybindings) *Keybinding { return &k.SearchExit }, "escape", "Close search bar"},
	{"copy", func(k *Keybindings) *Keybinding { return &k.Copy }, "ctrl+c", "Copy selection; forwards Ctrl+C to shell if nothing selected"},
	{"paste", func(k *Keybindings) *Keybinding { return &k.Paste }, "ctrl+v", "Paste from system clipboard"},
	{"quit", func(k *Keybindings) *Keybinding { return &k.Quit }, "ctrl+q", "Quit bunk"},
	{"reinit_host", func(k *Keybindings) *Keybinding { return &k.ReinitHost }, "alt+r", "Re-initialise host terminal (use after binary corruption)"},
}

// keybindingsHelpText returns a formatted key-bindings table for --help.
// If kb is non-nil the effective (possibly user-overridden) key strings are
// shown; otherwise the built-in defaults are used (e.g. for --help before
// the config is loaded).
func keybindingsHelpText(kb *Keybindings) string {
	var b strings.Builder
	b.WriteString("Key bindings (configurable via ~/.config/bunk/config.toml):\n")
	for _, e := range keybindingDefaults {
		key := e.def
		if kb != nil {
			key = e.field(kb).raw
		}
		fmt.Fprintf(&b, "  %-16s  %s\n", key, e.desc)
	}
	return b.String()
}

// resolveKeybindings builds a Keybindings from built-in defaults, then applies
// user overrides from the [keys] TOML map. Unknown or invalid entries are
// logged and ignored.
func resolveKeybindings(kf map[string]string) Keybindings {
	var kb Keybindings
	known := make(map[string]struct{}, len(keybindingDefaults))
	for _, e := range keybindingDefaults {
		known[e.action] = struct{}{}
		s := e.def
		if override, ok := kf[e.action]; ok && override != "" {
			s = override
		}
		parsed, err := parseKey(s)
		if err != nil {
			L.Warn("keybinding: parse error, using default", "action", e.action, "value", s, "err", err)
			parsed = mustParseKey(e.def)
		}
		*e.field(&kb) = parsed
	}
	for action := range kf {
		if _, ok := known[action]; !ok {
			L.Warn("keybinding: unknown action, ignoring", "action", action)
		}
	}
	return kb
}

// ---------------------------------------------------------------------------
// Resolved theme
// ---------------------------------------------------------------------------

// resolvedTheme holds a theme's colours as tcell.Colors. Colours the theme
// leaves to the host terminal are tcell.ColorDefault. Created once at
// startup; passed through render functions by value.
type resolvedTheme struct {
	bg, fg, cursor tcell.Color
	palette        [16]tcell.Color
	activeBorder   tcell.Color
	inactiveBorder tcell.Color
	scrollThumb    tcell.Color
	scrollTrack    tcell.Color
}

// Config is the fully resolved application configuration.
type Config struct {
	Theme       resolvedTheme
	ThemeName   string // the theme in force
	ThemeErr    error  // what was wrong with the theme settings; logged at startup
	LogFile     string
	LogLevel    string
	CellAspect  float64 // 0 = auto-detect via TIOCGWINSZ
	Scrollback  int     // max scrollback lines per pane
	Keybindings Keybindings
	Alerts      alert.Settings // [notifications]
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
// A missing default config uses built-in defaults; explicit paths and invalid
// files return an error before the terminal is initialized.
func LoadConfig(path, themeOverride string) (Config, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultConfigPath()
	}

	fc := fileConfig{
		Theme:    theme.Default,
		LogFile:  "/tmp/bunk.log",
		LogLevel: "info",
	}
	if _, err := toml.DecodeFile(path, &fc); err != nil {
		if explicit || !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("load config %q: %w", path, err)
		}
	}

	if themeOverride != "" {
		fc.Theme = themeOverride
	}
	reg, regErr := theme.NewRegistry(fc.Themes)
	t, selErr := reg.Select(fc.Theme, fc.UI)

	scrollback := fc.Scrollback
	if scrollback <= 0 {
		scrollback = session.DefaultScrollbackLines
	}
	alerts, err := fc.Notifications.Settings()
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}

	return Config{
		Theme:       resolveTheme(t),
		ThemeName:   t.Name,
		ThemeErr:    errors.Join(regErr, selErr),
		LogFile:     fc.LogFile,
		LogLevel:    fc.LogLevel,
		CellAspect:  fc.CellAspect,
		Scrollback:  scrollback,
		Keybindings: resolveKeybindings(fc.Keys),
		Alerts:      alerts,
	}, nil
}

// resolveTheme converts a theme's colours to tcell.Colors.
func resolveTheme(t theme.Theme) resolvedTheme {
	rt := resolvedTheme{
		bg:             tcellColor(t.Background),
		fg:             tcellColor(t.Foreground),
		cursor:         tcellColor(t.Cursor),
		activeBorder:   tcellColor(t.ActiveBorder),
		inactiveBorder: tcellColor(t.InactiveBorder),
		scrollThumb:    tcellColor(t.ScrollThumb),
		scrollTrack:    tcellColor(t.ScrollTrack),
	}
	for i, c := range t.ANSI {
		rt.palette[i] = tcellColor(c)
	}
	return rt
}

// tcellColor converts a theme colour; Unset becomes tcell.ColorDefault,
// the host terminal's own colour.
func tcellColor(c theme.Color) tcell.Color {
	if !c.IsSet() {
		return tcell.ColorDefault
	}
	return tcell.NewHexColor(int32(c.Hex()))
}

// ---------------------------------------------------------------------------
// Default config template
// ---------------------------------------------------------------------------

// DefaultConfigTOML returns a well-commented default config.toml as a string.
func DefaultConfigTOML() string {
	return fmt.Sprintf(`# bunk configuration
# %s

# Built-in themes: %s
# Use "terminal" to inherit colours from your terminal emulator, or the
# name of a custom theme defined below.
theme = "default"

# Logging.  Set log_file = "" to disable logging entirely.
log_file  = "/tmp/bunk.log"
log_level = "info"  # trace | debug | info | warn | error

# Cell pixel aspect ratio (cell height / cell width).
# Used to decide split direction (vertical vs horizontal) based on real pixels.
# Default 2.25 suits Noto Sans Mono 11pt on Ptyxis; adjust for your font.
# Examples: JetBrains Mono 12pt ≈ 2.2  |  Fira Code 11pt ≈ 2.15
#
# cell_aspect = 2.25   # uncomment and adjust if splits feel wrong

# Maximum scrollback lines per pane.  Higher values use more memory.
# Note: scrollback stores screen rows, not logical lines — narrower panes
# consume more rows per line of output due to wrapping.
# scrollback = 10000

# Desktop notifications through the host terminal (OSC 777 notify) when an
# agent in a pane you are not looking at needs you, is done or failed.
# foot, WezTerm, Ghostty and urxvt show them.
[notifications]
enabled = true
states  = ["needs-you", "done", "failed"]

# Optional UI color overrides - leave blank to use the theme's defaults.
# Values must be "#RRGGBB" hex strings.
[ui]
active_border   = ""  # border colour for the focused pane
inactive_border = ""  # border colour for unfocused panes
scrollbar_thumb = ""  # scrollbar handle
scrollbar_track = ""  # scrollbar background

# Custom themes: one [themes.NAME] table each, selected with theme = "NAME".
# background, foreground and 16 palette colours (0-7, then bright 8-15) are
# required; cursor, active_border, inactive_border, scrollbar_thumb and
# scrollbar_track are optional. bunk-gui reads the same tables.
#
# [themes.paper]
# background = "#FDF6E3"
# foreground = "#586E75"
# palette = [
#   "#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
#   "#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3",
# ]

# ---------------------------------------------------------------------------
# Key bindings
# ---------------------------------------------------------------------------
# Format: "f1", "ctrl+c", "alt+up", "shift+pgup", "escape", etc.
# Modifiers: ctrl, alt, shift.  Key names are case-insensitive.
# Leave a value empty (or remove the line) to keep the built-in default.
# Set a value to "none" to disable the action; its key then reaches the pane.
[keys]
split         = "f1"          # auto-split the active pane (host shell)
split_context = "alt+f1"     # auto-split inheriting container/ssh/sudo context
quit         = "ctrl+q"      # quit bunk
copy         = "ctrl+c"      # copy selection (if active); otherwise forwards to shell
paste        = "ctrl+v"      # paste from clipboard
search       = "ctrl+f"      # enter incremental search
passthrough  = "ctrl+f12"    # forward all keys except this toggle to the active pane
zoom         = "f12"         # toggle fullscreen zoom on the active pane
nav_up       = "alt+up"      # move focus to the pane above
nav_down     = "alt+down"    # move focus to the pane below
nav_left     = "alt+left"    # move focus to the pane on the left
nav_right    = "alt+right"   # move focus to the pane on the right
next_needs_you = "alt+a"     # focus the next pane whose agent needs you, then one that is done
scroll_up    = "shift+pgup"  # scroll up in scrollback buffer
scroll_down  = "shift+pgdn"  # scroll down / return toward live output
reinit_host  = "alt+r"       # re-initialise host terminal after binary corruption

# Search mode keys (active only while Ctrl+F search bar is open).
search_next  = "ctrl+n"   # jump to next match  (Enter always works too)
search_prev  = "ctrl+p"   # jump to previous match
search_exit  = "escape"   # close search bar
`, DefaultConfigPath(), strings.Join(theme.BuiltinNames(), ", "))
}
