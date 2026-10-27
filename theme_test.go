package main

import (
	"bunk/internal/badges"
	"bunk/internal/theme"
	"bunk/internal/vt10x"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// builtinTheme returns a built-in theme in the TUI's colours.
func builtinTheme(name string) resolvedTheme {
	t, ok := theme.Builtin(name)
	if !ok {
		panic("no built-in theme " + name)
	}
	return resolveTheme(t)
}

// loadTheme writes a config file with text and loads it with --theme
// override.
func loadTheme(t *testing.T, text, override string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, override)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// sameTUIColors reports the first colour of the TUI theme that differs.
func sameTUIColors(got, want resolvedTheme) (string, bool) {
	names := []string{"bg", "fg", "active border", "inactive border", "scroll thumb", "scroll track"}
	g := []tcell.Color{got.bg, got.fg, got.activeBorder, got.inactiveBorder, got.scrollThumb, got.scrollTrack}
	w := []tcell.Color{want.bg, want.fg, want.activeBorder, want.inactiveBorder, want.scrollThumb, want.scrollTrack}
	for i := range got.palette {
		names = append(names, fmt.Sprintf("palette %d", i))
		g, w = append(g, got.palette[i]), append(w, want.palette[i])
	}
	for i := range names {
		if g[i] != w[i] {
			return names[i], false
		}
	}
	return "", true
}

// TestThemesMatchOldConfig: with the themes moved to internal/theme, every
// built-in theme (and an unknown name, which selects the default) gives
// the TUI the colours it had before (the oracle in theme_oracle_test.go),
// with and without [ui] overrides and --theme: terminal colours, borders,
// scrollbar, every cell colour vtColor maps, every badge style, and the
// OSC 10/11/12 defaults with and without host colours.
func TestThemesMatchOldConfig(t *testing.T) {
	var oldNames []string
	for name := range oldBuiltinThemes {
		oldNames = append(oldNames, name)
	}
	sort.Strings(oldNames)
	newNames := theme.BuiltinNames()
	sort.Strings(newNames)
	if !slices.Equal(oldNames, newNames) {
		t.Fatalf("built-in themes %q, were %q", newNames, oldNames)
	}
	uis := []struct {
		name string
		toml string
		ui   oldUIOverride
	}{
		{"no overrides", "", oldUIOverride{}},
		{"every override", `[ui]
active_border = "#112233"
inactive_border = "#445566"
scrollbar_thumb = "#778899"
scrollbar_track = "#AABBCC"
`, oldUIOverride{"#112233", "#445566", "#778899", "#AABBCC"}},
		{"some overrides, lower case and without #", `[ui]
active_border = "#a0b1c2"
scrollbar_track = "0f1e2d"
`, oldUIOverride{ActiveBorder: "#a0b1c2", ScrollTrack: "0f1e2d"}},
	}
	hosts := []hostOSCColors{{}, {fg: "rgb:dddd/dddd/dddd", bg: "rgb:1111/1111/1111", cursor: "rgb:ffff/0000/0000"}}
	cells := []vt10x.Color{vt10x.DefaultFG, vt10x.DefaultBG, vt10x.DefaultCursor, vt10x.RGB(1, 2, 3), vt10x.RGB(0, 0, 255)}
	for c := vt10x.Color(0); c < 256; c++ {
		cells = append(cells, c)
	}
	for _, name := range append(oldNames, "no-such-theme") {
		for _, ui := range uis {
			for _, how := range []string{"config", "--theme"} {
				t.Run(name+"/"+ui.name+"/"+how, func(t *testing.T) {
					var cfg Config
					if how == "config" {
						cfg = loadTheme(t, "theme = \""+name+"\"\n"+ui.toml, "")
					} else {
						cfg = loadTheme(t, "theme = \"nord\"\n"+ui.toml, name)
					}
					got, want := cfg.Theme, oldResolveTheme(oldSelectTheme(name, ui.ui))
					if field, ok := sameTUIColors(got, want); !ok {
						t.Fatalf("%s differs: got %+v, want %+v", field, got, want)
					}
					def := tcell.NewRGBColor(9, 8, 7)
					for _, c := range cells {
						if g, w := vtColor(c, def, got), vtColor(c, def, want); g != w {
							t.Errorf("vtColor(%d): %v, was %v", c, g, w)
						}
					}
					// The old blend painted the terminal theme's dimmed badges
					// white; TestTerminalThemeBadgesUseHostColours covers it.
					for _, role := range append(slices.Clone(badges.Roles), badges.RoleNone) {
						if g, w := badgeStyle(role, got), oldBadgeStyle(role, want); g != w && name != theme.Terminal {
							t.Errorf("badge %s: style %v, was %v", role, g, w)
						}
					}
					for _, host := range hosts {
						gf, gb, gc := defaultOSCColors(got, host)
						wf, wb, wc := oldDefaultOSCColors(want, host)
						if gf != wf || gb != wb || gc != wc {
							t.Errorf("OSC defaults with host %+v: %q %q %q, were %q %q %q", host, gf, gb, gc, wf, wb, wc)
						}
					}
					if g, w := needsHostOSCColorProbe(got), needsHostOSCColorProbe(want); g != w {
						t.Errorf("host colour probe %v, was %v", g, w)
					}
					wantName := name
					if name == "no-such-theme" {
						wantName = theme.Default
					}
					if cfg.ThemeName != wantName {
						t.Errorf("theme in force %q, want %q", cfg.ThemeName, wantName)
					}
					if (cfg.ThemeErr != nil) != (name == "no-such-theme") {
						t.Errorf("theme error %v", cfg.ThemeErr)
					}
				})
			}
		}
	}
}

// TestLoadConfigThemes: custom [themes.NAME] tables are selectable by the
// theme key and --theme; a bad custom theme, an unknown name or a bad
// [ui] colour is reported in ThemeErr, and bunk starts with the default
// theme or the theme's own colour instead.
func TestLoadConfigThemes(t *testing.T) {
	const paper = `[themes.paper]
background = "#FDF6E3"
foreground = "#586E75"
cursor = "#D33682"
palette = ["#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
           "#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3"]
`
	rgb := func(v int32) tcell.Color { return tcell.NewHexColor(v) }
	def := builtinTheme(theme.Default)
	for _, c := range []struct {
		name, toml, override string
		check                func(t *testing.T, cfg Config)
		errs                 []string
	}{
		{name: "custom theme", toml: "theme = \"paper\"\n" + paper, check: func(t *testing.T, cfg Config) {
			rt := cfg.Theme
			if cfg.ThemeName != "paper" || rt.bg != rgb(0xFDF6E3) || rt.fg != rgb(0x586E75) || rt.cursor != rgb(0xD33682) ||
				rt.palette[1] != rgb(0xDC322F) || rt.palette[15] != rgb(0xFDF6E3) {
				t.Errorf("paper: %q %+v", cfg.ThemeName, rt)
			}
			// Borders and scrollbar from the palette.
			if rt.activeBorder != rt.palette[7] || rt.inactiveBorder != rt.palette[8] || rt.scrollThumb != rt.palette[7] ||
				rt.scrollTrack != rt.palette[0] {
				t.Errorf("paper borders: %+v", rt)
			}
			if _, _, cur := defaultOSCColors(rt, hostOSCColors{}); cur != "rgb:d3d3/3636/8282" {
				t.Errorf("OSC 12 default %q, want the theme's cursor", cur)
			}
		}},
		{name: "custom theme by --theme, with [ui]", toml: "theme = \"dracula\"\n[ui]\ninactive_border = \"#010203\"\n" + paper,
			override: "paper", check: func(t *testing.T, cfg Config) {
				if cfg.ThemeName != "paper" || cfg.Theme.inactiveBorder != rgb(0x010203) || cfg.Theme.bg != rgb(0xFDF6E3) {
					t.Errorf("--theme paper: %q %+v", cfg.ThemeName, cfg.Theme)
				}
			}},
		{name: "unknown theme", toml: "theme = \"solarized\"\n", errs: []string{`theme = "solarized": no such theme`, "dracula"},
			check: func(t *testing.T, cfg Config) {
				if cfg.ThemeName != theme.Default || cfg.Theme != def {
					t.Errorf("unknown theme: %q %+v, want the default", cfg.ThemeName, cfg.Theme)
				}
			}},
		{name: "bad custom theme", toml: `theme = "broken"
[themes.broken]
background = "#12345"
palette = ["#000000"]
`, errs: []string{"themes.broken.background", "themes.broken.foreground is missing", "themes.broken.palette has 1 colours, want 16",
			`theme = "broken": no such theme`},
			check: func(t *testing.T, cfg Config) {
				if cfg.ThemeName != theme.Default || cfg.Theme != def {
					t.Errorf("bad custom theme: %q %+v, want the default", cfg.ThemeName, cfg.Theme)
				}
			}},
		{name: "bad [ui] colour", toml: "theme = \"nord\"\n[ui]\nactive_border = \"teal\"\nscrollbar_thumb = \"#00FF00\"\n",
			errs: []string{`ui.active_border: "teal" is not #RRGGBB`}, check: func(t *testing.T, cfg Config) {
				nord := builtinTheme("nord")
				if cfg.Theme.activeBorder != nord.activeBorder || cfg.Theme.scrollThumb != rgb(0x00FF00) {
					t.Errorf("bad [ui] colour: %+v; want nord's active border and a green thumb", cfg.Theme)
				}
			}},
		{name: "a custom theme cannot replace a built-in", toml: "[themes.nord]\nbackground = \"#000000\"\n",
			errs: []string{"themes.nord: a built-in theme has this name"}, check: func(t *testing.T, cfg Config) {
				if cfg.Theme != def {
					t.Errorf("got %+v, want the default theme", cfg.Theme)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := loadTheme(t, c.toml, c.override)
			c.check(t, cfg)
			if len(c.errs) == 0 && cfg.ThemeErr != nil {
				t.Errorf("theme error %v", cfg.ThemeErr)
			}
			for _, want := range c.errs {
				if cfg.ThemeErr == nil || !strings.Contains(cfg.ThemeErr.Error(), want) {
					t.Errorf("theme error %v does not mention %q", cfg.ThemeErr, want)
				}
			}
		})
	}
}

// TestThemeParityTUI: every built-in theme, filled from the default theme
// as bunk-gui shows it, gives the TUI the terminal colours and badge role
// colours internal/theme defines. bunk-gui's TestThemeParityGUI checks its
// own colours against the same definitions, so both front ends agree. The
// terminal theme itself leaves every colour to the host.
func TestThemeParityTUI(t *testing.T) {
	def, _ := theme.Builtin(theme.Default)
	hex := func(c tcell.Color) theme.Color {
		r, g, b := c.RGB()
		return theme.RGB(uint32(r)<<16 | uint32(g)<<8 | uint32(b))
	}
	for _, b := range theme.Builtins() {
		t.Run(b.Name, func(t *testing.T) {
			f := b.Filled(def)
			rt := resolveTheme(f)
			if hex(rt.fg) != f.Foreground || hex(rt.bg) != f.Background || hex(rt.cursor) != f.Cursor {
				t.Errorf("fg/bg/cursor %v %v %v, want %v %v %v", hex(rt.fg), hex(rt.bg), hex(rt.cursor), f.Foreground, f.Background, f.Cursor)
			}
			for i, c := range rt.palette {
				if hex(c) != f.ANSI[i] {
					t.Errorf("palette %d: %v, want %v", i, hex(c), f.ANSI[i])
				}
			}
			for _, role := range badges.Roles {
				fg, bg, attrs := badgeStyle(role, rt).Decompose()
				wfg, wbg, wbold := f.Badge(role)
				if hex(fg) != wfg || hex(bg) != wbg || (attrs&tcell.AttrBold != 0) != wbold {
					t.Errorf("badge %s: %v on %v bold %v, want %v on %v bold %v", role, hex(fg), hex(bg), attrs&tcell.AttrBold != 0, wfg, wbg, wbold)
				}
			}
		})
	}
	t.Run("terminal leaves colours to the host", func(t *testing.T) {
		rt := builtinTheme(theme.Terminal)
		if field, ok := sameTUIColors(rt, resolvedTheme{}); !ok || rt.cursor != tcell.ColorDefault {
			t.Errorf("%s is set: %+v", field, rt)
		}
		if !needsHostOSCColorProbe(rt) {
			t.Error("terminal theme does not probe the host colours")
		}
	})
}

// TestTerminalThemeBadgesUseHostColours: with the terminal theme a badge
// takes the host's own palette colour of its slot, undimmed, and fixed
// colours stay; blending unknown host colours used to paint badges white.
func TestTerminalThemeBadgesUseHostColours(t *testing.T) {
	rt := builtinTheme(theme.Terminal)
	white := tcell.NewRGBColor(255, 255, 255)
	want := func(i theme.Ink) tcell.Color {
		if i.ANSI >= 0 {
			return tcell.PaletteColor(i.ANSI)
		}
		return tcellColor(i.Fixed)
	}
	for _, role := range badges.Roles {
		t.Run(role.String(), func(t *testing.T) {
			fi, bi, _ := theme.BadgeInks(role)
			fg, bg, _ := badgeStyle(role, rt).Decompose()
			if fg != want(fi) || bg != want(bi) {
				t.Errorf("%v on %v, want %v on %v", fg, bg, want(fi), want(bi))
			}
			if bg == white && bi.ANSI >= 0 {
				t.Errorf("background is white")
			}
		})
	}
}
