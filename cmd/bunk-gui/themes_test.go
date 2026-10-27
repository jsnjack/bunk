package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"bunk/internal/badges"
	"bunk/internal/theme"
)

// TestThemeParityGUI: every built-in theme, as bunk-gui shows it (filled
// from the default theme), gives the terminal view the palette and
// default colours, and the badges the role colours, that internal/theme
// defines; the CSS sets the badge, background and divider colours. bunk's
// TestThemeParityTUI checks the TUI against the same definitions, so both
// front ends agree.
func TestThemeParityGUI(t *testing.T) {
	def := defaultTheme()
	for _, b := range theme.Builtins() {
		t.Run(b.Name, func(t *testing.T) {
			th := b.Filled(def)
			f := newFixture(10, 2)
			f.tv = newTermView(&th)
			f.frame()
			tv := f.tv
			for i := range 16 {
				if got, want := tv.pal[i], rgba(th.ANSI[i].Hex()); got != want {
					t.Errorf("palette %d: %08x, want %08x", i, got, want)
				}
			}
			if tv.defFG != rgba(th.Foreground.Hex()) || tv.defBG != rgba(th.Background.Hex()) || tv.curSet {
				t.Errorf("fg %08x bg %08x cursor set %v", tv.defFG, tv.defBG, tv.curSet)
			}
			if tv.pal[196] != rgba(0xff0000) || tv.pal[244] != rgba(0x808080) {
				t.Errorf("xterm colours: 196 %08x, 244 %08x", tv.pal[196], tv.pal[244])
			}
			css := themeCSS(&th)
			for _, rule := range []string{
				".bunk-content { background: " + th.Background.String() + "; }",
				"paned > separator { background: " + dividerColor(&th).String() + "; box-shadow: none; }",
			} {
				if !strings.Contains(css, rule) {
					t.Errorf("CSS lacks %q", rule)
				}
			}
			for _, role := range badges.Roles {
				fg, bg, bold := badgeLook(&th, role)
				wfg, wbg, wbold := th.Badge(role)
				if fg != wfg.Hex() || bg != wbg.Hex() || bold != wbold {
					t.Errorf("badge %s: #%06x on #%06x bold %v, want %v on %v bold %v", role, fg, bg, bold, wfg, wbg, wbold)
				}
				weight := map[bool]int{false: 400, true: 700}[bold]
				rule := fmt.Sprintf(".badge.%s { color: #%06x; background: #%06x; font-weight: %d; }", badgeClass(role), fg, bg, weight)
				if !strings.Contains(css, rule) {
					t.Errorf("CSS lacks %q", rule)
				}
			}
		})
	}
	t.Run("terminal is the default theme's colours", func(t *testing.T) {
		term, _ := theme.Builtin(theme.Terminal)
		got := term.Filled(def)
		got.Name = def.Name
		if got != def {
			t.Errorf("terminal in bunk-gui: %+v, want %+v", got, def)
		}
	})
}

// TestThemeKeepsOverrides: colours a program set (OSC 4, 10, 11, 12) stay
// in front of the theme, before and after a theme change; a theme with a
// cursor colour of its own sets the block cursor's colour.
func TestThemeKeepsOverrides(t *testing.T) {
	th := defaultTheme()
	f := newFixture(10, 2)
	f.tv = newTermView(&th)
	f.write("\x1b]4;1;#010203\x07\x1b]10;#0a0b0c\x07\x1b]11;#0d0e0f\x07")
	f.frame()
	check := func(label string) {
		t.Helper()
		tv := f.tv
		if tv.pal[1] != rgba(0x010203) || tv.defFG != rgba(0x0a0b0c) || tv.defBG != rgba(0x0d0e0f) {
			t.Errorf("%s: red %08x fg %08x bg %08x; want the program's colours", label, tv.pal[1], tv.defFG, tv.defBG)
		}
		if tv.pal[2] != rgba(th.ANSI[2].Hex()) {
			t.Errorf("%s: green %08x, want the theme's %v", label, tv.pal[2], th.ANSI[2])
		}
	}
	check("default theme")
	th, _ = theme.Builtin("dracula")
	f.tv.loadColors(f.term) // as Pane.themeChanged does
	check("dracula")
	if f.tv.curSet {
		t.Error("dracula set a cursor colour; its cursor is its foreground")
	}

	r, err := theme.NewRegistry(map[string]theme.Def{"ink": {Background: "#000000", Foreground: "#FFFFFF", Cursor: "#FF00FF",
		Palette: slices.Repeat([]string{"#808080"}, 16)}})
	if err != nil {
		t.Fatal(err)
	}
	th, _ = r.Lookup("ink")
	f.tv.loadColors(f.term)
	if !f.tv.curSet || f.tv.defCur != rgba(0xFF00FF) {
		t.Errorf("ink: cursor set %v, %08x; want the theme's #ff00ff", f.tv.curSet, f.tv.defCur)
	}
	f.write("\x1b]12;#123456\x07")
	f.frame()
	if f.tv.defCur != rgba(0x123456) {
		t.Errorf("OSC 12 under ink: cursor %08x, want the program's", f.tv.defCur)
	}
}

// TestLoadSettingsTheme: the theme key selects a built-in or custom theme;
// an unknown name or an invalid [themes.NAME] table is reported and keeps
// the default theme; unknown keys inside a theme table are reported.
func TestLoadSettingsTheme(t *testing.T) {
	const paper = `[themes.paper]
background = "#FDF6E3"
foreground = "#586E75"
palette = ["#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
           "#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3"]
`
	for _, c := range []struct {
		name, toml, want string
		themes           []string // custom themes offered
		errs             []string
	}{
		{name: "no key", toml: "", want: theme.Default},
		{name: "built-in", toml: "theme = \"nord\"\n", want: "nord"},
		{name: "terminal", toml: "theme = \"terminal\"\n", want: theme.Terminal},
		{name: "custom", toml: "theme = \"paper\"\n" + paper, want: "paper", themes: []string{"paper"}},
		{name: "unknown", toml: "theme = \"solarized\"\n", want: theme.Default,
			errs: []string{`theme = "solarized" (want one of terminal, default, solarized-dark, dracula, nord)`}},
		{name: "invalid custom", toml: "theme = \"bad\"\n[themes.bad]\nbackground = \"#00000\"\n" + paper, want: theme.Default,
			themes: []string{"paper"}, errs: []string{`themes.bad.background: "#00000" is not #RRGGBB`,
				"themes.bad.foreground is missing", `theme = "bad"`}},
		{name: "unknown key in a theme", toml: "theme = \"paper\"\n" + paper + "colour = \"red\"\n", want: "paper",
			themes: []string{"paper"}, errs: []string{"unknown keys: themes.paper.colour"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := loadSettings(writeConfig(t, c.toml))
			if st.Theme != c.want {
				t.Errorf("theme %q, want %q", st.Theme, c.want)
			}
			if got, want := st.themes.Names(), append(theme.BuiltinNames(), c.themes...); !slices.Equal(got, want) {
				t.Errorf("themes %q, want %q", got, want)
			}
			if len(c.errs) == 0 && err != nil {
				t.Errorf("error %v", err)
			}
			for _, want := range c.errs {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error %v does not mention %q", err, want)
				}
			}
		})
	}
}

// TestSelectTheme: --theme takes priority over the settings; an unknown
// --theme gives the default theme and an error naming the flag; the theme
// shown has every colour set.
func TestSelectTheme(t *testing.T) {
	st, err := loadSettings(writeConfig(t, "theme = \"nord\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	nord, _ := theme.Builtin("nord")
	for _, c := range []struct {
		flag string
		want theme.Theme
		err  string
	}{
		{"", nord, ""},
		{"dracula", func() theme.Theme { d, _ := theme.Builtin("dracula"); return d }(), ""},
		{"terminal", func() theme.Theme { d := defaultTheme(); d.Name = theme.Terminal; return d }(), ""},
		{"nope", defaultTheme(), `--theme: theme = "nope": no such theme`},
	} {
		t.Run("flag "+c.flag, func(t *testing.T) {
			a := &App{settings: st, cfg: config{theme: c.flag}}
			got, err := a.selectTheme()
			if got != c.want {
				t.Errorf("theme %+v, want %+v", got, c.want)
			}
			var te *theme.Error
			if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.HasPrefix(err.Error(), c.err) || !errors.As(err, &te)) {
				t.Errorf("error %v, want %q", err, c.err)
			}
		})
	}
}

// TestThemePreview: a theme's preview shows its background, foreground
// and palette colours.
func TestThemePreview(t *testing.T) {
	d, _ := theme.Builtin("dracula")
	m := themePreview(d)
	for _, want := range []string{`background="#282a36"`, `foreground="#f8f8f2"`, `foreground="#ff5555"`, `foreground="#8be9fd"`} {
		if !strings.Contains(m, want) {
			t.Errorf("preview %q lacks %q", m, want)
		}
	}
}
