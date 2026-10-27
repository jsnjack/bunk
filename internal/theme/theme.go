// Package theme holds the colour themes of both front ends: the built-in
// themes, custom themes from a config file, the [ui] overrides, and the
// colours that badges and borders take from a theme. bunk's TUI turns a
// Theme into tcell colours; bunk-gui draws it with OpenGL and CSS.
package theme

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Color is a colour as 0xRRGGBB plus a flag bit that marks it set. The
// zero value is Unset.
type Color uint32

// Unset is a colour that a theme leaves to the host terminal: bunk's TUI
// paints the host's own colour there. Every colour of the terminal theme
// is Unset.
const Unset Color = 0

const setBit Color = 1 << 24

// RGB returns the colour 0xRRGGBB.
func RGB(rgb uint32) Color { return Color(rgb&0xffffff) | setBit }

// IsSet reports whether c is a colour rather than Unset.
func (c Color) IsSet() bool { return c&setBit != 0 }

// Hex returns c as 0xRRGGBB, or 0 for Unset.
func (c Color) Hex() uint32 { return uint32(c & 0xffffff) }

// String returns c as "#rrggbb", or "" for Unset.
func (c Color) String() string {
	if !c.IsSet() {
		return ""
	}
	return fmt.Sprintf("#%06x", c.Hex())
}

// ParseColor reads a "#RRGGBB" hex colour. The "#" is optional, as it
// always was in bunk's config.
func ParseColor(s string) (Color, error) {
	h := strings.TrimPrefix(s, "#")
	if len(h) != 6 {
		return Unset, fmt.Errorf("%q is not #RRGGBB", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return Unset, fmt.Errorf("%q is not #RRGGBB", s)
	}
	return RGB(uint32(v)), nil
}

// Mix returns c blended pct percent toward base, channel by channel and
// rounded down. If either colour is Unset, it returns Unset.
func Mix(c, base Color, pct uint32) Color {
	if !c.IsSet() || !base.IsSet() {
		return Unset
	}
	mix := func(shift uint) uint32 {
		a, b := c.Hex()>>shift&0xff, base.Hex()>>shift&0xff
		return (a*(100-pct) + b*pct) / 100 << shift
	}
	return RGB(mix(16) | mix(8) | mix(0))
}

// ANSI colour indexes. The 16-colour palette is a convention that every
// terminal theme follows: each theme picks its own values, but the
// meaning of each slot is fixed, so badges that take palette colours suit
// any theme. 8-15 are the bright variants of 0-7.
const (
	Black   = 0 // dark background, empty space
	Red     = 1 // errors, danger, destructive operations
	Green   = 2 // success, confirmations
	Yellow  = 3 // warnings, attention
	Blue    = 4 // information, links
	Magenta = 5 // special, syntax accents
	Cyan    = 6 // secondary information, labels
	White   = 7 // foreground text
	// BrightBlack is the first bright colour.
	BrightBlack = 8
)

// Theme is a resolved theme: the terminal's colours, and the colours of
// the borders and scrollbar of bunk's TUI.
type Theme struct {
	Name       string
	Foreground Color
	Background Color
	// Cursor is the cursor colour. The built-in themes use the foreground.
	Cursor Color
	// ANSI holds the palette colours 0-15.
	ANSI [16]Color
	// ActiveBorder is the TUI's border around the focused pane.
	ActiveBorder Color
	// InactiveBorder is the TUI's other borders and bunk-gui's dividers
	// between panes.
	InactiveBorder Color
	ScrollThumb    Color
	ScrollTrack    Color
}

// Filled returns t with every Unset colour taken from base. bunk-gui has
// no host terminal to inherit colours from, so it shows each theme filled
// from the default theme: there, the terminal theme looks like the
// default one.
func (t Theme) Filled(base Theme) Theme {
	fill := func(c *Color, from Color) {
		if !c.IsSet() {
			*c = from
		}
	}
	fill(&t.Foreground, base.Foreground)
	fill(&t.Background, base.Background)
	fill(&t.Cursor, base.Cursor)
	for i := range t.ANSI {
		fill(&t.ANSI[i], base.ANSI[i])
	}
	fill(&t.ActiveBorder, base.ActiveBorder)
	fill(&t.InactiveBorder, base.InactiveBorder)
	fill(&t.ScrollThumb, base.ScrollThumb)
	fill(&t.ScrollTrack, base.ScrollTrack)
	return t
}

// Names of the themes with a special meaning.
const (
	// Default is the theme used when the config names none.
	Default = "default"
	// Terminal leaves every colour to the host terminal.
	Terminal = "terminal"
)

// builtinDefs are the shipped themes in the order menus show them. The
// terminal theme has no colours.
var builtinDefs = []struct {
	name string
	def  Def
}{
	{Terminal, Def{}},
	// The author's Tilix palette, from yauhen.cc/posts/my-tillix.
	{Default, Def{
		Background: "#1C1C1F",
		Foreground: "#FFFFFF",
		Palette: []string{
			"#241F31", "#C01C28", "#2EC27E", "#F5C211",
			"#1E78E4", "#9841BB", "#0AB9DC", "#C0BFBC",
			"#5E5C64", "#ED333B", "#57E389", "#F8E45C",
			"#51A1FF", "#C061CB", "#4FD2FD", "#F6F5F4",
		},
		ActiveBorder:   "#C0BFBC",
		InactiveBorder: "#5E5C64",
		ScrollThumb:    "#C0BFBC",
		ScrollTrack:    "#3D3D40",
	}},
	// Solarized Dark by Ethan Schoonover.
	{"solarized-dark", Def{
		Background: "#002B36",
		Foreground: "#839496",
		Palette: []string{
			"#073642", "#DC322F", "#859900", "#B58900",
			"#268BD2", "#D33682", "#2AA198", "#EEE8D5",
			"#002B36", "#CB4B16", "#586E75", "#657B83",
			"#839496", "#6C71C4", "#93A1A1", "#FDF6E3",
		},
		ActiveBorder:   "#93A1A1",
		InactiveBorder: "#073642",
		ScrollThumb:    "#657B83",
		ScrollTrack:    "#073642",
	}},
	// Dracula by Zeno Rocha.
	{"dracula", Def{
		Background: "#282A36",
		Foreground: "#F8F8F2",
		Palette: []string{
			"#21222C", "#FF5555", "#50FA7B", "#F1FA8C",
			"#BD93F9", "#FF79C6", "#8BE9FD", "#F8F8F2",
			"#6272A4", "#FF6E6E", "#69FF94", "#FFFFA5",
			"#D6ACFF", "#FF92DF", "#A4FFFF", "#FFFFFF",
		},
		ActiveBorder:   "#6272A4",
		InactiveBorder: "#44475A",
		ScrollThumb:    "#6272A4",
		ScrollTrack:    "#21222C",
	}},
	// Nord by Arctic Ice Studio.
	{"nord", Def{
		Background: "#2E3440",
		Foreground: "#D8DEE9",
		Palette: []string{
			"#3B4252", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#88C0D0", "#E5E9F0",
			"#4C566A", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#8FBCBB", "#ECEFF4",
		},
		ActiveBorder:   "#D8DEE9",
		InactiveBorder: "#4C566A",
		ScrollThumb:    "#D8DEE9",
		ScrollTrack:    "#3B4252",
	}},
}

var builtins = func() []Theme {
	out := make([]Theme, 0, len(builtinDefs))
	for _, b := range builtinDefs {
		if b.name == Terminal {
			out = append(out, Theme{Name: Terminal})
			continue
		}
		t, problems := b.def.resolve(b.name)
		if len(problems) > 0 {
			panic("theme: bad built-in theme: " + strings.Join(problems, "; "))
		}
		out = append(out, t)
	}
	return out
}()

// Builtins returns the built-in themes in the order menus show them:
// terminal, default, solarized-dark, dracula, nord.
func Builtins() []Theme { return slices.Clone(builtins) }

// Builtin returns the built-in theme called name.
func Builtin(name string) (Theme, bool) {
	for _, t := range builtins {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

// BuiltinNames lists the names of the built-in themes in menu order.
func BuiltinNames() []string {
	names := make([]string, len(builtins))
	for i, t := range builtins {
		names[i] = t.Name
	}
	return names
}

// Def is a custom theme as a config file writes it, in a [themes.NAME]
// table:
//
//	[themes.paper]
//	background = "#FDF6E3"
//	foreground = "#586E75"
//	cursor     = "#D33682"       # optional; the foreground otherwise
//	palette    = ["#073642", …]  # 16 colours: 0-7, then bright 8-15
//	active_border   = "#93A1A1"  # optional, as are the next three
//	inactive_border = "#EEE8D5"
//	scrollbar_thumb = "#93A1A1"
//	scrollbar_track = "#EEE8D5"
//
// A theme without border or scrollbar colours takes them from its
// palette: white (7) for the active border and the thumb, bright black
// (8) for the other borders and black (0) for the track.
type Def struct {
	Background     string   `toml:"background"`
	Foreground     string   `toml:"foreground"`
	Cursor         string   `toml:"cursor"`
	Palette        []string `toml:"palette"`
	ActiveBorder   string   `toml:"active_border"`
	InactiveBorder string   `toml:"inactive_border"`
	ScrollThumb    string   `toml:"scrollbar_thumb"`
	ScrollTrack    string   `toml:"scrollbar_track"`
}

// resolve parses d as the theme called name. It returns what is wrong
// with d, each problem prefixed with the theme's table.
func (d Def) resolve(name string) (Theme, []string) {
	t := Theme{Name: name}
	var problems []string
	where := "themes." + name
	color := func(dst *Color, key, s string, required bool) {
		if s == "" {
			if required {
				problems = append(problems, fmt.Sprintf("%s.%s is missing", where, key))
			}
			return
		}
		c, err := ParseColor(s)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s.%s: %v", where, key, err))
			return
		}
		*dst = c
	}
	color(&t.Background, "background", d.Background, true)
	color(&t.Foreground, "foreground", d.Foreground, true)
	color(&t.Cursor, "cursor", d.Cursor, false)
	if len(d.Palette) != len(t.ANSI) {
		problems = append(problems, fmt.Sprintf("%s.palette has %d colours, want %d", where, len(d.Palette), len(t.ANSI)))
	} else {
		for i, s := range d.Palette {
			color(&t.ANSI[i], "palette["+strconv.Itoa(i)+"]", s, true)
		}
	}
	color(&t.ActiveBorder, "active_border", d.ActiveBorder, false)
	color(&t.InactiveBorder, "inactive_border", d.InactiveBorder, false)
	color(&t.ScrollThumb, "scrollbar_thumb", d.ScrollThumb, false)
	color(&t.ScrollTrack, "scrollbar_track", d.ScrollTrack, false)
	if len(problems) > 0 {
		return Theme{}, problems
	}
	return t.Filled(Theme{
		Cursor:         t.Foreground,
		ActiveBorder:   t.ANSI[White],
		InactiveBorder: t.ANSI[BrightBlack],
		ScrollThumb:    t.ANSI[White],
		ScrollTrack:    t.ANSI[Black],
	}), nil
}

// UI holds the [ui] overrides of bunk's config: "#RRGGBB" colours for the
// borders and the scrollbar, or "" for the theme's own.
type UI struct {
	ActiveBorder   string `toml:"active_border"`
	InactiveBorder string `toml:"inactive_border"`
	ScrollThumb    string `toml:"scrollbar_thumb"`
	ScrollTrack    string `toml:"scrollbar_track"`
}

// Error lists what is wrong with a config file's theme settings.
type Error struct {
	Problems []string
}

// Error joins the problems with "; ".
func (e *Error) Error() string { return strings.Join(e.Problems, "; ") }

// errorOf returns problems as an *Error, or nil without any.
func errorOf(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return &Error{Problems: problems}
}

// Registry holds the themes a config file can select: the built-in themes
// and the file's custom ones. A nil Registry holds the built-in themes.
type Registry struct {
	custom []Theme // by name
}

// NewRegistry returns the built-in themes plus the custom themes in defs,
// the [themes.NAME] tables of a config file. A custom theme that is
// invalid, or that has a built-in theme's name, is left out, and the error
// (an *Error) says what is wrong with it.
func NewRegistry(defs map[string]Def) (*Registry, error) {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	r := &Registry{}
	var problems []string
	for _, name := range names {
		if _, ok := Builtin(name); ok {
			problems = append(problems, fmt.Sprintf("themes.%s: a built-in theme has this name", name))
			continue
		}
		t, p := defs[name].resolve(name)
		if len(p) > 0 {
			problems = append(problems, p...)
			continue
		}
		r.custom = append(r.custom, t)
	}
	return r, errorOf(problems)
}

// Names lists every theme: the built-in ones in menu order, then the
// custom ones by name.
func (r *Registry) Names() []string {
	names := BuiltinNames()
	if r == nil {
		return names
	}
	for _, t := range r.custom {
		names = append(names, t.Name)
	}
	return names
}

// Lookup returns the theme called name.
func (r *Registry) Lookup(name string) (Theme, bool) {
	if t, ok := Builtin(name); ok {
		return t, true
	}
	if r == nil {
		return Theme{}, false
	}
	for _, t := range r.custom {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

// Select returns the theme called name, or the default theme for "", with
// the [ui] overrides applied. An unknown name selects the default theme,
// and an invalid override keeps the theme's colour; the error (an *Error)
// says what was wrong.
func (r *Registry) Select(name string, ui UI) (Theme, error) {
	var problems []string
	if name == "" {
		name = Default
	}
	t, ok := r.Lookup(name)
	if !ok {
		problems = append(problems, fmt.Sprintf("theme = %q: no such theme (have %s)", name, strings.Join(r.Names(), ", ")))
		t, _ = Builtin(Default)
	}
	override := func(dst *Color, key, s string) {
		if s == "" {
			return
		}
		c, err := ParseColor(s)
		if err != nil {
			problems = append(problems, fmt.Sprintf("ui.%s: %v", key, err))
			return
		}
		*dst = c
	}
	override(&t.ActiveBorder, "active_border", ui.ActiveBorder)
	override(&t.InactiveBorder, "inactive_border", ui.InactiveBorder)
	override(&t.ScrollThumb, "scrollbar_thumb", ui.ScrollThumb)
	override(&t.ScrollTrack, "scrollbar_track", ui.ScrollTrack)
	return t, errorOf(problems)
}
