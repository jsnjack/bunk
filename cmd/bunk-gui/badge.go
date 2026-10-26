package main

// Per-pane status badges, as in bunk's TUI: internal/badges decides which
// show, their order, text and fitting from the pane's state and width in
// cells; here they become GTK widgets in a row at the top right of the
// pane, over its texture, in the terminal font. Changing a badge changes
// those widgets only: GSK reuses the pane's texture, so the pane draws no
// frame.
//
// The row follows the pane's state after every frame (App.syncBadges, from
// after-paint), which is where scrolling and a new grid width show up, and
// at once on events that may draw no frame: status reports, passthrough,
// zoom, the find bar and the Copied flash. A sync that finds nothing
// changed touches no widget, so an idle window stays without frames.

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/rivo/uniseg"

	"bunk/internal/badges"
)

// Agent badge colours (0xRRGGBB), those of the chrome's state classes
// and of bunk's TUI; they do not follow the palette.
const (
	agentBadgeBG = 0x24262b // background of the quiet states
	agentInk     = 0x131416 // text on the needs-you and done badges
	agentWorking = 0x5aa9ff
	agentNeeds   = 0xffb454
	agentIdle    = 0x9aa0a8
	agentFailed  = 0xff8a8a
	agentDone    = 0x7ee2a8
)

// dimRGB blends c 40% toward the terminal background, as bunk's TUI
// softens badge backgrounds.
func dimRGB(c uint32) uint32 {
	mix := func(shift uint) uint32 {
		a, b := c>>shift&0xff, theme.bg>>shift&0xff
		return (a*60 + b*40) / 100 << shift
	}
	return mix(16) | mix(8) | mix(0)
}

// badgeLook returns how a badge of role is painted: text and background
// colours (0xRRGGBB) and bold. They follow bunk's TUI over the palette
// both front ends share (theme).
func badgeLook(role badges.Role) (fg, bg uint32, bold bool) {
	text, yellow, dark := theme.palette[7], theme.palette[3], theme.palette[0]
	switch role {
	case badges.RolePass, badges.RoleContainer:
		return text, dimRGB(theme.palette[6]), true
	case badges.RoleScroll, badges.RoleZoom:
		return yellow, dark, true
	case badges.RoleSudo:
		return text, dimRGB(theme.palette[1]), true
	case badges.RoleSSH:
		return text, dimRGB(theme.palette[4]), true
	case badges.RoleMosh:
		return text, dark, true
	case badges.RoleFlash:
		return text, dimRGB(theme.palette[2]), true
	case badges.RoleWorking:
		return agentWorking, agentBadgeBG, false
	case badges.RoleIdle:
		return agentIdle, agentBadgeBG, false
	case badges.RoleFailed:
		return agentFailed, agentBadgeBG, true
	case badges.RoleNeedsYou:
		return agentInk, agentNeeds, true
	case badges.RoleDone:
		return agentInk, agentDone, true
	}
	return theme.fg, theme.bg, false
}

// badgeClass is the CSS class of a role's colours.
func badgeClass(role badges.Role) string { return "badge-" + role.String() }

// badgeCSS styles each role's class with its colours.
func badgeCSS() string {
	var b strings.Builder
	for _, r := range badges.Roles {
		fg, bg, bold := badgeLook(r)
		weight := 400
		if bold {
			weight = 700
		}
		fmt.Fprintf(&b, ".badge.%s { color: #%06x; background: #%06x; font-weight: %d; }\n", badgeClass(r), fg, bg, weight)
	}
	return b.String()
}

// badgeKey is everything the badge row shows; the row changes only when
// it does.
type badgeKey struct {
	in    badges.Input
	font  string  // the terminal font
	cellW float64 // logical width of a cell
	top   int     // logical pixels above the row: the find bar while it is open
}

// badgeLabel is one badge of the row and what it shows. A GtkInscription
// takes the size of its text in characters of its font, not of its glyphs:
// with the terminal font a badge is exactly one cell high and a cell wide
// per column of text, as in the TUI, even where a glyph comes from a
// taller fallback font.
type badgeLabel struct {
	l    *gtk.Inscription
	text string
	role badges.Role
}

// newBadgeRow creates the pane's badge row, empty, in its frame overlay.
func (p *Pane) newBadgeRow() {
	p.badgeRow = gtk.NewBox(gtk.OrientationHorizontal, 0)
	p.badgeRow.SetHAlign(gtk.AlignEnd)
	p.badgeRow.SetVAlign(gtk.AlignStart)
	p.badgeRow.SetCanTarget(false) // clicks go to the terminal below
	p.badgeRow.SetVisible(false)
	p.frame.AddOverlay(p.badgeRow)
}

// badgeInput is the pane's state as its badges show it: the scroll
// offset and width of the last frame, the status as shown (status
// overrides included), passthrough, zoom and the flash.
func (p *Pane) badgeInput() badges.Input {
	st := p.status()
	// Status.Container is "TYPE NAME"; a type is one word.
	ctype, cid, _ := strings.Cut(st.Container, " ")
	return badges.Input{
		Passthrough:   p.passthrough,
		ScrollOffset:  p.tv.sbOff,
		Agent:         st.Agent,
		State:         st.State,
		Notice:        st.Notice,
		Process:       st.Process,
		ContainerType: ctype,
		ContainerID:   cid,
		SSHHost:       st.SSHHost,
		Zoomed:        p.tab != nil && p.tab.zoom == p,
		Flash:         p.flash,
		Width:         p.tv.cols,
	}
}

// badgeTop is how far below the pane's top the row sits: under the find
// bar while it is open on this pane, at the top otherwise.
func (p *Pane) badgeTop() int {
	if f := p.app.find; f != nil && f.p == p {
		return f.height + 4
	}
	return 0
}

// syncBadges brings the badge row up to date. It changes no widget when
// nothing it shows changed.
func (p *Pane) syncBadges() {
	if p.badgeRow == nil || p.removeOnce || p.s == nil {
		return
	}
	k := badgeKey{in: p.badgeInput(), font: p.app.fontDesc, top: p.badgeTop()}
	if p.atlas != nil && p.atlas.Scale > 0 {
		k.cellW = float64(p.atlas.CellW) / p.atlas.Scale
	}
	if k == p.badgeKey {
		return
	}
	p.badgeKey = k
	setBadgeFont(k.font)
	row := badges.Layout(k.in)
	for len(p.badgeLabels) < len(row.Badges) {
		l := gtk.NewInscription("")
		l.AddCSSClass("badge")
		l.SetMinLines(1)
		l.SetNatLines(1)
		l.SetVisible(false)
		p.badgeRow.Prepend(l) // Badges run right to left
		p.badgeLabels = append(p.badgeLabels, badgeLabel{l: l})
	}
	for i := range p.badgeLabels {
		b := &p.badgeLabels[i]
		if i >= len(row.Badges) {
			b.l.SetVisible(false)
			continue
		}
		want := row.Badges[i]
		if b.text != want.Text {
			b.text = want.Text
			b.l.SetText(want.Text)
			cells := uint(uniseg.StringWidth(want.Text))
			b.l.SetMinChars(cells)
			b.l.SetNatChars(cells)
		}
		if b.role != want.Role {
			if b.role != badges.RoleNone {
				b.l.RemoveCSSClass(badgeClass(b.role))
			}
			b.role = want.Role
			b.l.AddCSSClass(badgeClass(want.Role))
		}
		b.l.SetVisible(true)
	}
	p.badgeRow.SetMarginEnd(int(math.Round(float64(row.Inset) * k.cellW)))
	p.badgeRow.SetMarginTop(k.top)
	p.badgeRow.SetVisible(len(row.Badges) > 0)
}

// badgeFontCSS sets the badges in the terminal font; badgeFontNow is the
// font it holds.
var (
	badgeFontCSS *gtk.CSSProvider
	badgeFontNow string
)

// setBadgeFont sets the badges in font (a Pango description such as
// "Noto Sans Mono 11") at its size in logical pixels, as the glyph atlas
// does (96 dpi), whatever the desktop's text scaling, so a badge spans
// the cells the model counted.
func setBadgeFont(font string) {
	if font == badgeFontNow {
		return
	}
	badgeFontNow = font
	desc := pango.FontDescriptionFromString(font)
	px := float64(desc.Size()) / pango.SCALE
	if !desc.SizeIsAbsolute() {
		px = px * 96 / 72
	}
	var families []string
	for _, f := range strings.Split(desc.Family(), ",") {
		if f = strings.TrimSpace(f); f != "" {
			families = append(families, strconv.Quote(f))
		}
	}
	css := fmt.Sprintf(".badge { font-family: %s; font-size: %.3fpx; }", strings.Join(append(families, "monospace"), ", "), px)
	if badgeFontCSS == nil {
		badgeFontCSS = gtk.NewCSSProvider()
		gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), badgeFontCSS, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	}
	badgeFontCSS.LoadFromString(css)
}

// showFlash shows text as the pane's flash badge for badges.FlashFor, as
// the TUI does after a copy.
func (p *Pane) showFlash(text string) {
	p.flash = text
	p.flashGen++
	gen := p.flashGen
	p.syncBadges()
	coreglib.TimeoutAdd(uint(badges.FlashFor.Milliseconds()), func() bool {
		if p.flashGen == gen {
			p.flash = ""
			p.syncBadges()
		}
		return false
	})
}

// syncBadges updates the badge rows of the selected tab's panes. It runs
// after every frame: a frame is where a new scroll offset or grid width
// shows up. It allocates nothing when no row changes.
func (a *App) syncBadges() {
	if a.active != nil && !a.finished {
		syncBadgeTree(a.active.root)
	}
}

func syncBadgeTree(n *node) {
	if n == nil {
		return
	}
	if n.pane != nil {
		n.pane.syncBadges()
		return
	}
	syncBadgeTree(n.a)
	syncBadgeTree(n.b)
}
