package theme

import "bunk/internal/badges"

// BadgeDim is how far a dimmed badge background blends toward the theme
// background, in percent. It softens the palette colours behind badge
// text.
const BadgeDim = 40

// Agent state colours. They are the same in every theme, so a state looks
// alike on badges, on the TUI's separators and in bunk-gui's tabs.
var (
	AgentBadge   = RGB(0x24262b) // badge background of the quiet states
	AgentInk     = RGB(0x131416) // text on the needs-you and done badges
	AgentWorking = RGB(0x5aa9ff)
	AgentNeeds   = RGB(0xffb454) // also the separators around a pane that needs you
	AgentIdle    = RGB(0x9aa0a8)
	AgentFailed  = RGB(0xff8a8a)
	AgentDone    = RGB(0x5fd35f) // also the separators around a pane that is done
)

// Ink is where a badge colour comes from: an ANSI palette colour of the
// theme, or a fixed colour. Dim blends it BadgeDim percent toward the
// theme background.
type Ink struct {
	ANSI  int // palette index 0-15, or -1 for Fixed
	Fixed Color
	Dim   bool
}

func ansi(i int) Ink    { return Ink{ANSI: i} }
func dimmed(i int) Ink  { return Ink{ANSI: i, Dim: true} }
func fixed(c Color) Ink { return Ink{ANSI: -1, Fixed: c} }

// BadgeInks returns where the text and background colours of a badge role
// come from, and whether its text is bold. Theme.Badge resolves them; a
// front end with its own colour type, such as the TUI's tcell colours,
// reads this table and does the same. RoleNone has Unset colours.
func BadgeInks(role badges.Role) (fg, bg Ink, bold bool) {
	switch role {
	case badges.RolePass, badges.RoleContainer:
		return ansi(White), dimmed(Cyan), true
	case badges.RoleScroll, badges.RoleZoom:
		return ansi(Yellow), ansi(Black), true
	case badges.RoleSudo:
		return ansi(White), dimmed(Red), true
	case badges.RoleSSH:
		return ansi(White), dimmed(Blue), true
	case badges.RoleMosh:
		return ansi(White), ansi(Black), true
	case badges.RoleFlash:
		return ansi(White), dimmed(Green), true
	case badges.RoleWorking:
		return fixed(AgentWorking), fixed(AgentBadge), false
	case badges.RoleIdle:
		return fixed(AgentIdle), fixed(AgentBadge), false
	case badges.RoleFailed:
		return fixed(AgentFailed), fixed(AgentBadge), true
	case badges.RoleNeedsYou:
		return fixed(AgentInk), fixed(AgentNeeds), true
	case badges.RoleDone:
		return fixed(AgentInk), fixed(AgentDone), true
	}
	return fixed(Unset), fixed(Unset), false
}

// Color resolves i in t.
func (t Theme) Color(i Ink) Color {
	c := i.Fixed
	if i.ANSI >= 0 {
		c = t.ANSI[i.ANSI]
	}
	if i.Dim {
		c = Mix(c, t.Background, BadgeDim)
	}
	return c
}

// Badge returns the text and background colours of a badge role in t,
// and whether its text is bold.
func (t Theme) Badge(role badges.Role) (fg, bg Color, bold bool) {
	fi, bi, bold := BadgeInks(role)
	return t.Color(fi), t.Color(bi), bold
}
