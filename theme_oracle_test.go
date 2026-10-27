package main

// The oracle for TestThemesMatchOldConfig: the theme registry, resolution,
// [ui] overrides, badge styles and OSC default colours as they were before
// the themes moved to internal/theme, unchanged but for the "old" prefix.
// Do not edit it to follow new behaviour; it records what the TUI drew.

import (
	"bunk/internal/badges"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type oldThemeDef struct {
	Background     string
	Foreground     string
	Palette        [16]string
	ActiveBorder   string
	InactiveBorder string
	ScrollThumb    string
	ScrollTrack    string
}

type oldUIOverride struct {
	ActiveBorder   string
	InactiveBorder string
	ScrollThumb    string
	ScrollTrack    string
}

var oldBuiltinThemes = map[string]oldThemeDef{
	// Terminal: inherit every colour from the host terminal.
	// All fields are empty → hexColor returns tcell.ColorDefault for each.
	"terminal": {},

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
		ActiveBorder:   "#C0BFBC", // light gray (same as scroll thumb)
		InactiveBorder: "#5E5C64",
		ScrollThumb:    "#C0BFBC",
		ScrollTrack:    "#3D3D40",
	},

	// Solarized Dark - Ethan Schoonover
	"solarized-dark": {
		Background: "#002B36",
		Foreground: "#839496",
		Palette: [16]string{
			"#073642", "#DC322F", "#859900", "#B58900",
			"#268BD2", "#D33682", "#2AA198", "#EEE8D5",
			"#002B36", "#CB4B16", "#586E75", "#657B83",
			"#839496", "#6C71C4", "#93A1A1", "#FDF6E3",
		},
		ActiveBorder:   "#93A1A1", // light gray-cyan
		InactiveBorder: "#073642",
		ScrollThumb:    "#657B83",
		ScrollTrack:    "#073642",
	},

	// Dracula - Zeno Rocha
	"dracula": {
		Background: "#282A36",
		Foreground: "#F8F8F2",
		Palette: [16]string{
			"#21222C", "#FF5555", "#50FA7B", "#F1FA8C",
			"#BD93F9", "#FF79C6", "#8BE9FD", "#F8F8F2",
			"#6272A4", "#FF6E6E", "#69FF94", "#FFFFA5",
			"#D6ACFF", "#FF92DF", "#A4FFFF", "#FFFFFF",
		},
		ActiveBorder:   "#6272A4", // comment gray-blue
		InactiveBorder: "#44475A",
		ScrollThumb:    "#6272A4",
		ScrollTrack:    "#21222C",
	},

	// Nord - Arctic Ice Studio
	"nord": {
		Background: "#2E3440",
		Foreground: "#D8DEE9",
		Palette: [16]string{
			"#3B4252", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#88C0D0", "#E5E9F0",
			"#4C566A", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#8FBCBB", "#ECEFF4",
		},
		ActiveBorder:   "#D8DEE9", // snow storm light gray
		InactiveBorder: "#4C566A",
		ScrollThumb:    "#D8DEE9",
		ScrollTrack:    "#3B4252",
	},
}

// oldSelectTheme is LoadConfig's theme lookup and [ui] overrides.
func oldSelectTheme(name string, ui oldUIOverride) oldThemeDef {
	def, ok := oldBuiltinThemes[name]
	if !ok {
		def = oldBuiltinThemes["default"]
	}
	if ui.ActiveBorder != "" {
		def.ActiveBorder = ui.ActiveBorder
	}
	if ui.InactiveBorder != "" {
		def.InactiveBorder = ui.InactiveBorder
	}
	if ui.ScrollThumb != "" {
		def.ScrollThumb = ui.ScrollThumb
	}
	if ui.ScrollTrack != "" {
		def.ScrollTrack = ui.ScrollTrack
	}
	return def
}

func oldResolveTheme(d oldThemeDef) resolvedTheme {
	rt := resolvedTheme{
		bg:             oldHexColor(d.Background),
		fg:             oldHexColor(d.Foreground),
		activeBorder:   oldHexColor(d.ActiveBorder),
		inactiveBorder: oldHexColor(d.InactiveBorder),
		scrollThumb:    oldHexColor(d.ScrollThumb),
		scrollTrack:    oldHexColor(d.ScrollTrack),
	}
	for i, s := range d.Palette {
		rt.palette[i] = oldHexColor(s)
	}
	return rt
}

func oldHexColor(s string) tcell.Color {
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

func oldBadgeStyle(role badges.Role, rt resolvedTheme) tcell.Style {
	dim := func(c tcell.Color) tcell.Color {
		r1, g1, b1 := c.RGB()
		r2, g2, b2 := rt.bg.RGB()
		mix := func(a, b int32) int32 { return (a*60 + b*40) / 100 }
		return tcell.NewRGBColor(mix(r1, r2), mix(g1, g2), mix(b1, b2))
	}
	text := tcell.StyleDefault.Foreground(rt.palette[7]).Bold(true)   // white text
	yellow := tcell.StyleDefault.Foreground(rt.palette[3]).Bold(true) // not dimmed
	switch role {
	case badges.RolePass, badges.RoleContainer:
		return text.Background(dim(rt.palette[6]))
	case badges.RoleScroll, badges.RoleZoom:
		return yellow.Background(rt.palette[0])
	case badges.RoleSudo:
		return text.Background(dim(rt.palette[1]))
	case badges.RoleSSH:
		return text.Background(dim(rt.palette[4]))
	case badges.RoleMosh:
		return text.Background(rt.palette[0])
	case badges.RoleFlash:
		return text.Background(dim(rt.palette[2]))
	}
	if state, ok := role.AgentState(); ok {
		look, _ := oldAgentLookOf(state)
		return look.style
	}
	return tcell.StyleDefault
}

func oldDefaultOSCColors(theme resolvedTheme, host hostOSCColors) (fg, bg, cursor string) {
	fg = tcellColorToXParse(theme.fg)
	if fg == "" {
		fg = host.fg
	}

	bg = tcellColorToXParse(theme.bg)
	if bg == "" {
		bg = host.bg
	}

	// bunk's built-in themes do not model a dedicated cursor colour, so the
	// existing fallback is the theme foreground. In terminal-inherit mode the
	// foreground is unknown, so only use a host-probed cursor colour.
	cursor = tcellColorToXParse(theme.fg)
	if cursor == "" {
		cursor = host.cursor
	}
	return fg, bg, cursor
}
