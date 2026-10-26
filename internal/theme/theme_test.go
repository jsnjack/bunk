package theme

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"bunk/internal/badges"
	"bunk/internal/badges/badgetest"
)

func TestParseColor(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Color
		ok   bool
	}{
		{"#C01C28", RGB(0xC01C28), true},
		{"#c01c28", RGB(0xC01C28), true},
		{"c01c28", RGB(0xC01C28), true},
		{"#000000", RGB(0), true},
		{"", Unset, false},
		{"#12345", Unset, false},
		{"#1234567", Unset, false},
		{"#GG0000", Unset, false},
		{"red", Unset, false},
		{"#+12345", Unset, false},
	} {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseColor(c.in)
			if got != c.want || (err == nil) != c.ok {
				t.Errorf("ParseColor(%q) = %v, %v; want %v, ok %v", c.in, got, err, c.want, c.ok)
			}
		})
	}
}

func TestColor(t *testing.T) {
	black := RGB(0)
	if !black.IsSet() || black.Hex() != 0 || black.String() != "#000000" {
		t.Errorf("black: set %v, hex %06x, %q", black.IsSet(), black.Hex(), black)
	}
	if Unset.IsSet() || Unset.String() != "" {
		t.Errorf("Unset: set %v, %q", Unset.IsSet(), Unset)
	}
	if c := RGB(0x1FFFFFF); c.Hex() != 0xFFFFFF {
		t.Errorf("RGB keeps 24 bits: %06x", c.Hex())
	}
}

func TestMix(t *testing.T) {
	for _, c := range []struct {
		name    string
		c, base Color
		pct     uint32
		want    Color
	}{
		{"40% toward the default background", RGB(0x0AB9DC), RGB(0x1C1C1F), 40, RGB(0x117A90)},
		{"0%", RGB(0x123456), RGB(0xFFFFFF), 0, RGB(0x123456)},
		{"100%", RGB(0x123456), RGB(0xABCDEF), 100, RGB(0xABCDEF)},
		{"rounds down", RGB(0x010101), RGB(0x000000), 50, RGB(0x000000)},
		{"unset colour", Unset, RGB(0x1C1C1F), 40, Unset},
		{"unset base", RGB(0x0AB9DC), Unset, 40, Unset},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Mix(c.c, c.base, c.pct); got != c.want {
				t.Errorf("Mix(%v, %v, %d) = %v, want %v", c.c, c.base, c.pct, got, c.want)
			}
		})
	}
}

// TestBuiltins: the built-in themes in menu order; terminal leaves every
// colour unset; the others set every colour, with the foreground as the
// cursor.
func TestBuiltins(t *testing.T) {
	want := []string{"terminal", "default", "solarized-dark", "dracula", "nord"}
	if got := BuiltinNames(); !slices.Equal(got, want) {
		t.Fatalf("names %q, want %q", got, want)
	}
	for i, b := range Builtins() {
		t.Run(b.Name, func(t *testing.T) {
			if b.Name != want[i] {
				t.Fatalf("theme %d is %q, want %q", i, b.Name, want[i])
			}
			if got, ok := Builtin(b.Name); !ok || got != b {
				t.Errorf("Builtin(%q) = %+v, %v", b.Name, got, ok)
			}
			colors := append([]Color{b.Foreground, b.Background, b.Cursor, b.ActiveBorder, b.InactiveBorder,
				b.ScrollThumb, b.ScrollTrack}, b.ANSI[:]...)
			for j, c := range colors {
				if c.IsSet() == (b.Name == Terminal) {
					t.Errorf("colour %d: set %v", j, c.IsSet())
				}
			}
			if b.Name != Terminal && b.Cursor != b.Foreground {
				t.Errorf("cursor %v, want the foreground %v", b.Cursor, b.Foreground)
			}
		})
	}
	def, _ := Builtin(Default)
	if def.Background != RGB(0x1C1C1F) || def.Foreground != RGB(0xFFFFFF) || def.ANSI[Red] != RGB(0xC01C28) || def.ANSI[15] != RGB(0xF6F5F4) {
		t.Errorf("default theme %+v", def)
	}
	if _, ok := Builtin("no-such-theme"); ok {
		t.Error("Builtin found an unknown theme")
	}
	bs := Builtins()
	bs[0].Name = "changed"
	if b, _ := Builtin(Terminal); b.Name != Terminal {
		t.Error("Builtins returns the registry itself")
	}
}

func TestFilled(t *testing.T) {
	def, _ := Builtin(Default)
	term, _ := Builtin(Terminal)
	got := term.Filled(def)
	want := def
	want.Name = Terminal
	if got != want {
		t.Errorf("terminal filled from default: %+v, want %+v", got, want)
	}
	nord, _ := Builtin("nord")
	if got := nord.Filled(def); got != nord {
		t.Errorf("a theme with every colour changed: %+v", got)
	}
}

// paper is a valid custom theme definition.
func paper() Def {
	return Def{
		Background: "#FDF6E3", Foreground: "#586E75",
		Palette: []string{"#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
			"#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3"},
	}
}

// TestNewRegistry: custom themes join the built-in ones, sorted by name;
// optional colours come from the palette or the foreground; each invalid
// custom theme is left out and every problem is listed.
func TestNewRegistry(t *testing.T) {
	withCursor := paper()
	withCursor.Cursor, withCursor.ScrollTrack = "#D33682", "#EEE8D5"
	badHex := paper()
	badHex.Palette = slices.Clone(badHex.Palette)
	badHex.Palette[3], badHex.InactiveBorder = "#B5890", "grey"
	short := paper()
	short.Palette = short.Palette[:8]
	for _, c := range []struct {
		name  string
		defs  map[string]Def
		names []string // custom themes in the registry
		errs  []string
	}{
		{name: "none"},
		{name: "valid", defs: map[string]Def{"paper": paper(), "ink": withCursor}, names: []string{"ink", "paper"}},
		{name: "missing colours", defs: map[string]Def{"empty": {}, "paper": paper()}, names: []string{"paper"},
			errs: []string{"themes.empty.background is missing", "themes.empty.foreground is missing", "themes.empty.palette has 0 colours, want 16"}},
		{name: "bad colours", defs: map[string]Def{"bad": badHex}, errs: []string{
			`themes.bad.palette[3]: "#B5890" is not #RRGGBB`, `themes.bad.inactive_border: "grey" is not #RRGGBB`}},
		{name: "eight colours", defs: map[string]Def{"short": short}, errs: []string{"themes.short.palette has 8 colours, want 16"}},
		{name: "a built-in name", defs: map[string]Def{"dracula": paper(), "default": paper()},
			errs: []string{"themes.default: a built-in theme has this name", "themes.dracula: a built-in theme has this name"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := NewRegistry(c.defs)
			if got, want := r.Names(), append(BuiltinNames(), c.names...); !slices.Equal(got, want) {
				t.Errorf("names %q, want %q", got, want)
			}
			if len(c.errs) == 0 {
				if err != nil {
					t.Fatalf("error %v", err)
				}
				return
			}
			var te *Error
			if !errors.As(err, &te) || len(te.Problems) != len(c.errs) {
				t.Fatalf("error %#v, want the problems %q", err, c.errs)
			}
			for i, want := range c.errs {
				if te.Problems[i] != want {
					t.Errorf("problem %d: %q, want %q", i, te.Problems[i], want)
				}
			}
			if err.Error() != strings.Join(c.errs, "; ") {
				t.Errorf("message %q", err)
			}
		})
	}
	r, err := NewRegistry(map[string]Def{"paper": paper(), "ink": withCursor})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.Lookup("paper")
	if !ok || p.Name != "paper" || p.Background != RGB(0xFDF6E3) || p.Cursor != p.Foreground || p.ANSI[Red] != RGB(0xDC322F) ||
		p.ActiveBorder != p.ANSI[White] || p.InactiveBorder != p.ANSI[BrightBlack] || p.ScrollThumb != p.ANSI[White] ||
		p.ScrollTrack != p.ANSI[Black] {
		t.Errorf("paper: %+v", p)
	}
	if ink, _ := r.Lookup("ink"); ink.Cursor != RGB(0xD33682) || ink.ScrollTrack != RGB(0xEEE8D5) {
		t.Errorf("ink: cursor %v, track %v", ink.Cursor, ink.ScrollTrack)
	}
	if d, ok := r.Lookup("dracula"); !ok || d.Background != RGB(0x282A36) {
		t.Errorf("dracula through the registry: %+v", d)
	}
	var none *Registry
	if _, ok := none.Lookup("paper"); ok || !slices.Equal(none.Names(), BuiltinNames()) {
		t.Error("a nil registry is not the built-in themes")
	}
}

// TestSelect: a name picks its theme and "" the default; an unknown name
// picks the default theme; [ui] overrides replace border and scrollbar
// colours, and a bad one keeps the theme's colour. Problems come back as
// an *Error.
func TestSelect(t *testing.T) {
	r, err := NewRegistry(map[string]Def{"paper": paper()})
	if err != nil {
		t.Fatal(err)
	}
	nord, _ := Builtin("nord")
	def, _ := Builtin(Default)
	for _, c := range []struct {
		name, theme string
		ui          UI
		want        func() Theme
		errs        []string
	}{
		{name: "built-in", theme: "nord", want: func() Theme { return nord }},
		{name: "empty", theme: "", want: func() Theme { return def }},
		{name: "custom", theme: "paper", want: func() Theme { p, _ := r.Lookup("paper"); return p }},
		{name: "unknown", theme: "solarized", want: func() Theme { return def },
			errs: []string{`theme = "solarized": no such theme (have terminal, default, solarized-dark, dracula, nord, paper)`}},
		{name: "overrides", theme: "nord", ui: UI{ActiveBorder: "#010203", InactiveBorder: "040506", ScrollThumb: "#070809", ScrollTrack: "#0A0B0C"},
			want: func() Theme {
				t := nord
				t.ActiveBorder, t.InactiveBorder, t.ScrollThumb, t.ScrollTrack = RGB(0x010203), RGB(0x040506), RGB(0x070809), RGB(0x0A0B0C)
				return t
			}},
		{name: "bad override", theme: "nord", ui: UI{ActiveBorder: "teal", ScrollTrack: "#0A0B0C"},
			want: func() Theme {
				t := nord
				t.ScrollTrack = RGB(0x0A0B0C)
				return t
			}, errs: []string{`ui.active_border: "teal" is not #RRGGBB`}},
		{name: "unknown with overrides", theme: "x", ui: UI{ScrollThumb: "#FFFFFF"},
			want: func() Theme {
				t := def
				t.ScrollThumb = RGB(0xFFFFFF)
				return t
			}, errs: []string{`theme = "x": no such theme`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.Select(c.theme, c.ui)
			if want := c.want(); got != want {
				t.Errorf("Select(%q) = %+v, want %+v", c.theme, got, want)
			}
			var te *Error
			if len(c.errs) == 0 {
				if err != nil {
					t.Errorf("error %v", err)
				}
				return
			}
			if !errors.As(err, &te) || len(te.Problems) != len(c.errs) {
				t.Fatalf("error %#v, want %q", err, c.errs)
			}
			for i, want := range c.errs {
				if !strings.HasPrefix(te.Problems[i], want) {
					t.Errorf("problem %q, want %q", te.Problems[i], want)
				}
			}
		})
	}
}

// TestBadgeColors: under the default theme each role has the colours in
// badgetest.DefaultLooks, which both front ends' badge tests check too;
// RoleNone has no colours; agent roles keep their colours in every theme.
func TestBadgeColors(t *testing.T) {
	def, _ := Builtin(Default)
	for _, role := range badges.Roles {
		t.Run(role.String(), func(t *testing.T) {
			fg, bg, bold := def.Badge(role)
			got := badgetest.Look{FG: fg.Hex(), BG: bg.Hex(), Bold: bold}
			if want := badgetest.DefaultLooks[role]; got != want || !fg.IsSet() || !bg.IsSet() {
				t.Errorf("look %+v, want %+v", got, want)
			}
			if _, ok := role.AgentState(); ok {
				dracula, _ := Builtin("dracula")
				if dfg, dbg, _ := dracula.Badge(role); dfg != fg || dbg != bg {
					t.Errorf("agent role in dracula: %v on %v, want %v on %v", dfg, dbg, fg, bg)
				}
			}
		})
	}
	if fg, bg, bold := def.Badge(badges.RoleNone); fg.IsSet() || bg.IsSet() || bold {
		t.Errorf("RoleNone: %v on %v bold %v", fg, bg, bold)
	}
	fi, bi, _ := BadgeInks(badges.RoleSSH)
	if fi != (Ink{ANSI: White}) || bi != (Ink{ANSI: Blue, Dim: true}) {
		t.Errorf("ssh inks %+v %+v", fi, bi)
	}
}
