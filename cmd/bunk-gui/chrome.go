package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"bunk/internal/alert"
	"bunk/internal/badges"
	"bunk/internal/session"
)

// The window chrome follows the prototypes in the design mockups
// (Main.dc.html: rail, Horizontal.dc.html: bar): colours, sizes, glyphs,
// menu items and shortcuts come from there.
const chromeCSS = `
window.bunk-window { background: #16171a; }
.bunk-bar { background: #131416; border-bottom: 1px solid #24262b; min-height: 43px; padding: 0 6px; }
.bunk-rail { background: #131416; border-right: 1px solid #24262b; }
.bunk-rail.collapsed { min-width: 47px; padding: 8px 0; }
.bunk-rail.expanded { min-width: 251px; padding: 6px 8px 8px 8px; }
button.chrome { background: none; box-shadow: none; border: none; outline: none; padding: 0; margin: 0;
	min-width: 34px; min-height: 34px; border-radius: 8px; color: #b4b9c1; }
button.chrome:hover { background: alpha(#ffffff, 0.06); }
button.chrome.wide { padding: 0 10px; font-size: 13px; }
menubutton.chrome > button { background: none; box-shadow: none; border: none; min-width: 34px; min-height: 34px;
	padding: 0; border-radius: 8px; color: #d8dade; }
menubutton.chrome > button:checked, menubutton.chrome > button:hover { background: #2a2c31; }
.closewin { background: #2a2c31; border-radius: 9999px; min-width: 24px; min-height: 24px; color: #d8dade; }
.tab { border-radius: 8px; min-height: 34px; color: #b4b9c1; }
.tab.active { background: #23252a; color: #e6e8eb; }
.tab.needs { background: #2a2216; }
.tab.needs.active { background: #23252a; }
button.tabbtn { background: none; box-shadow: none; border: none; padding: 0 2px 0 10px; min-height: 34px; color: inherit; }
button.tabclose { background: none; box-shadow: none; border: none; min-width: 26px; min-height: 26px; padding: 0;
	margin-right: 4px; border-radius: 6px; color: #7d828a; }
.tab.active button.tabclose { color: #b4b9c1; }
button.tabclose:hover { background: alpha(#ffffff, 0.08); }
.dot { font-size: 14px; min-width: 14px; }
.working { color: #5aa9ff; }
.needs-fg { color: #ffb454; }
.idle { color: #9aa0a8; }
.failed { color: #ff8a8a; }
.done { color: #7ee2a8; }
.findbar { background: #1b1c20; border: 1px solid #2f3238; border-radius: 8px; padding: 4px 4px 4px 8px; margin: 6px 14px 0 0; }
entry.findentry { min-height: 30px; background: #131416; color: #e6e8eb; }
.findcount { font-family: "Noto Sans Mono", monospace; font-size: 12px; color: #9aa0a8; min-width: 48px; }
.badge { padding: 0; margin: 0; min-height: 0; border-radius: 0; }
.passtag { background: #e6e8eb; color: #131416; font-weight: 700; font-size: 9px; padding: 0 4px; border-radius: 3px; }
.agent { font-weight: 600; font-size: 13px; }
.repo { font-family: "Noto Sans Mono", monospace; font-size: 12px; color: #9aa0a8; }
.more { font-size: 12px; color: #9aa0a8; }
.loc { font-family: "Noto Sans Mono", monospace; font-size: 12px; color: #c7b8ff; }
.notice { font-size: 12px; color: #ffb454; }
button.raildot { background: none; box-shadow: none; border: none; padding: 0; min-width: 40px; min-height: 40px;
	border-radius: 8px; font-size: 16px; }
button.raildot.active { background: #26292f; }
button.raildot.needs { background: #2a2216; }
.railmap { min-width: 30px; min-height: 30px; }
.mapcell { font-size: 10px; background: alpha(#ffffff, 0.07); border-radius: 3px; min-width: 6px; min-height: 6px; }
.panecount { font-size: 9px; color: #9aa0a8; }
.railsep { background: #2a2c31; min-height: 1px; min-width: 24px; margin: 4px 0; }
.row { border-radius: 8px; color: #d8dade; }
.row.active { background: #26292f; }
.row.needs { background: #2a2216; }
.row.needs.active { background: #26292f; }
button.rowbtn { background: none; box-shadow: none; border: none; padding: 8px 4px 8px 10px; color: inherit; }
.row .agent { font-size: 14px; color: #e6e8eb; }
.statelabel { font-size: 12px; color: #9aa0a8; }
.statelabel.needs-fg { color: #ffb454; font-weight: 600; }
.hint { font-size: 12px; color: #8a8f98; padding: 8px 6px 2px 6px; }
popover.bunk-menu > contents { background: #1b1c20; border: 1px solid #2f3238; border-radius: 10px; padding: 6px; }
.menusize { color: #b4b9c1; font-size: 13px; padding-left: 6px; }
button.sizebtn { min-width: 36px; min-height: 32px; border: 1px solid #34373e; border-radius: 6px; background: #131416;
	color: #e6e8eb; box-shadow: none; padding: 0; }
.sizelabel { font-family: "Noto Sans Mono", monospace; font-size: 13px; min-width: 52px; color: #e6e8eb; }
.menugroup { border-top: 1px solid #2a2c31; padding: 4px 0; }
button.menuitem { background: none; box-shadow: none; border: none; min-height: 34px; padding: 0 10px; border-radius: 6px;
	color: #e6e8eb; font-size: 14px; }
button.menuitem:hover { background: #2a2c31; }
checkbutton.menucheck { min-height: 34px; padding: 0 10px 0 6px; border-radius: 6px; color: #e6e8eb; font-size: 14px; }
checkbutton.menucheck:hover { background: #2a2c31; }
.keys { font-family: "Noto Sans Mono", monospace; font-size: 12px; color: #9aa0a8; }
.switcher-bg { background: rgba(8, 9, 11, 0.62); }
.switcher { background: #1b1c20; border: 1px solid #2f3238; border-radius: 10px; padding: 10px; }
.switcher-title { font-size: 12px; color: #9aa0a8; padding: 4px 6px 0 6px; }
entry.switcher-entry { background: #131416; border: 1px solid #34373e; border-radius: 6px; color: #e6e8eb;
	font-family: "Noto Sans Mono", monospace; font-size: 14px; min-height: 40px; margin: 0 6px 8px 6px; }
button.switchrow { background: none; box-shadow: none; border: none; min-height: 44px; padding: 8px 10px; border-radius: 6px;
	color: inherit; font-size: 14px; }
button.switchrow.active { background: #26292f; }
button.switchrow:hover { background: #23252a; }
button.switchrow .swtitle { font-weight: 600; color: #e6e8eb; }
.swbranch { font-family: "Noto Sans Mono", monospace; font-size: 12px; color: #9aa0a8; }
.swagent { font-size: 12px; color: #c4c8ce; }
.swpath { font-family: "Noto Sans Mono", monospace; font-size: 12px; color: #9aa0a8; }
button.switchrow.cursor { background: #30343b; box-shadow: inset 0 0 0 1px #4a4f58; }
button.switchclose { min-height: 36px; padding: 0 12px; border: 1px solid #34373e; border-radius: 6px; background: none;
	color: #b4b9c1; box-shadow: none; margin-top: 4px; }
paned > separator { min-width: 1px; min-height: 1px; }
.dragged { opacity: 0.5; }
.bunk-rail .drop-before { box-shadow: inset 0 2px #5aa9ff; }
.bunk-rail .drop-after { box-shadow: inset 0 -2px #5aa9ff; }
.bunk-bar .drop-before { box-shadow: inset 2px 0 #5aa9ff; }
.bunk-bar .drop-after { box-shadow: inset -2px 0 #5aa9ff; }
.splitratio { background: alpha(#1b1c20, 0.94); border: 1px solid #34373e; border-radius: 6px; padding: 4px 8px;
	font-family: "Noto Sans Mono", monospace; font-size: 13px; color: #e6e8eb; }
.splitratio > separator { background: #4a4f58; margin: 0 8px; }
.splitratio.vertical > separator { margin: 4px 0; }
.shortcutrow { padding: 6px 4px; border-bottom: 1px solid #2a2c31; }
.shortcutdesc { font-size: 14px; color: #e6e8eb; }
.collide { color: #ffb454; font-size: 15px; }
button.shortcutkeys, button.keyadd { font-family: "Noto Sans Mono", monospace; font-size: 12px; }
button.keyremove { padding: 0 8px; color: #9aa0a8; }
button.tabbtn label, button.rowbtn label, button.menuitem label, button.switchrow label, button.raildot label { font-weight: normal; }
button label.agent { font-weight: 600; }
`

// loadCSS styles the chrome. The colours that follow the theme (the
// content background, dividers and badges) come from the theme's CSS
// (themes.go).
func loadCSS() {
	// The chrome and the terminal are dark whatever the desktop's style:
	// popovers, toasts and tooltips follow.
	adw.StyleManagerGetDefault().SetColorScheme(adw.ColorSchemeForceDark)
	css := gtk.NewCSSProvider()
	css.LoadFromString(chromeCSS)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

// look is how a state is shown: dot glyph, label and colour class.
type look struct{ dot, label, cls string }

// commandLook is how a command mark's state is shown: an agent state's dot
// and colour, labelled as the mark's badge is (badges.CommandLook).
func commandLook(state session.AgentState) look {
	l := lookOf(state)
	if _, label, _, ok := badges.CommandLook(state); ok {
		l.label = label
	}
	return l
}

// markOf is the command mark a pane's tab shows: none while an agent
// runs in the pane, whose state shows instead.
func markOf(p *Pane) alert.Mark {
	if p.status().Agent != "" {
		return alert.Mark{}
	}
	return p.mark
}

// markName names a mark's command in a tab's tooltip and notice.
func markName(m alert.Mark) string {
	if m.Name != "" {
		return m.Name
	}
	return "command"
}

// markText says what a mark's command is and how it went, for a tooltip:
// "make -j8 · running since 14:03", "tar xf a.tar · finished after 2m 14s".
func markText(m alert.Mark) string {
	what := m.Line
	if what == "" {
		what = markName(m)
	}
	if m.State == session.AgentWorking {
		return what + " · running since " + m.Started.Format("15:04")
	}
	return what + " · " + m.Summary
}

// lookOf takes the dot and label of the pane's agent badge
// (internal/badges).
func lookOf(state session.AgentState) look {
	dot, label, _, ok := badges.AgentLook(state)
	switch state {
	case session.AgentWorking:
		return look{dot, label, "working"}
	case session.AgentNeedsYou:
		return look{dot, label, "needs-fg"}
	case session.AgentFailed:
		return look{dot, label, "failed"}
	case session.AgentDone:
		return look{dot, label, "done"}
	}
	if !ok {
		dot = "○" // a plain shell
	}
	return look{dot, label, "idle"}
}

// tabInfo is the label data of one tab. The name, repo and location come
// from its agent pane (Tab.agentPane), so the label stays put while
// agents in other panes change state; the state shown is the most urgent
// of all its agents' (stateOrder), with that agent's notice. A pane
// without an agent shows its command mark (alert.Mark) in place of an
// agent state: running, or done or failed until the user looks.
type tabInfo struct {
	t                *Tab
	index            int
	st               session.Status // the agent pane's
	state            session.AgentState
	command          bool   // state is a command mark's, not an agent's
	notice           string // of the agent in state; "agent: notice" when not the agent pane's
	more             int    // agents in the tab's other panes, shown as "+N"
	look             look
	agent, repo, loc string // agent: the agent, else the process, else "shell"
	path             string // the agent pane's directory, ~ for home
	name             string // the user's name for the tab; shown in place of agent
	locGlyph         string
	active, needs    bool
	pass             bool // a pane of the tab sends all keys to the terminal
	panes            int
	// paneMap is the tab's panes as the collapsed rail draws them
	// (paneMap), "" for a single pane or more than railMapMax.
	paneMap string
	// arcs are the tab's panes' arcs in the collapsed rail's ring
	// (railring.go).
	arcs               []ringArc
	tooltip, accessKey string
}

func (a *App) tabInfos() []tabInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Log(context.Background(), LevelTrace, "tab labels keep full paths", "err", err)
	}
	out := make([]tabInfo, 0, len(a.tabs))
	for i, t := range a.tabs {
		p := t.agentPane()
		if p == nil {
			continue
		}
		st := p.status()
		in := tabInfo{t: t, index: i, st: st, state: st.State, notice: st.Notice, active: t == a.active, name: t.name}
		if in.panes = len(t.panes()); in.panes > 1 && in.panes <= railMapMax {
			in.paneMap = paneMap(t.root, mapDot)
		}
		in.arcs = ringArcs(t.root, mapDot)
		own := markOf(p) // the agent pane's command mark, without an agent
		if own.State != session.AgentNone {
			in.state, in.command, in.notice = own.State, true, own.Summary
		}
		var others []string // tooltip lines and names of the other panes' agents and commands
		for _, q := range t.panes() {
			in.pass = in.pass || q.passthrough
			if q == p {
				continue
			}
			qs := q.status()
			if m := markOf(q); m.State != session.AgentNone {
				if stateOrder[m.State] < stateOrder[in.state] {
					in.state, in.command, in.notice = m.State, true, ""
					if m.Summary != "" {
						in.notice = markName(m) + ": " + m.Summary
					}
				}
				others = append(others, markName(m)+" · "+markText(m))
				continue
			}
			if qs.Agent == "" {
				continue
			}
			in.more++
			if stateOrder[qs.State] < stateOrder[in.state] {
				in.state, in.command, in.notice = qs.State, false, ""
				if qs.Notice != "" {
					in.notice = qs.Agent + ": " + qs.Notice
				}
			}
			line := []string{qs.Agent}
			if l := lookOf(qs.State).label; l != "" {
				line = append(line, l)
			}
			if qs.Notice != "" {
				line = append(line, qs.Notice)
			}
			others = append(others, strings.Join(line, " · "))
		}
		in.look, in.needs = lookOf(in.state), in.state == session.AgentNeedsYou
		if in.command {
			in.look = commandLook(in.state)
		}
		in.agent = st.Agent
		if in.agent == "" && own.Name != "" {
			in.agent = own.Name // the command stays named until the user looks
		}
		if in.agent == "" {
			in.agent = st.Process
		}
		if in.agent == "" {
			in.agent = "shell"
		}
		in.path = st.Cwd
		if home != "" && strings.HasPrefix(in.path, home) {
			in.path = "~" + strings.TrimPrefix(in.path, home)
		}
		switch {
		case st.Repo != "" && st.Branch != "":
			in.repo = st.Repo + " ⎇ " + st.Branch
		case st.Repo != "":
			in.repo = st.Repo
		default:
			in.repo = in.path
		}
		switch {
		case st.Container != "":
			in.loc, in.locGlyph = "⬡ "+st.Container, "⬡"
		case st.SSHHost != "":
			in.loc, in.locGlyph = "⇄ ssh "+st.SSHHost, "⇄"
		case st.Host != "":
			// A remote agent reporting in-band through a client bunk does
			// not recognise as ssh.
			in.loc, in.locGlyph = "⇄ "+st.Host, "⇄"
		}
		parts := []string{in.agent, in.repo}
		if in.name != "" {
			parts = append([]string{in.name}, parts...)
		}
		if in.loc != "" {
			parts = append(parts, in.loc)
		}
		ownLook := lookOf(st.State)
		if own.State != session.AgentNone {
			ownLook = commandLook(own.State)
		}
		if ownLook.label != "" {
			parts = append(parts, ownLook.label)
		}
		if in.pass {
			parts = append(parts, "all keys go to the terminal")
		}
		in.tooltip = strings.Join(parts, " · ")
		switch {
		case own.State != session.AgentNone:
			in.tooltip += "\n" + markText(own)
		case st.Notice != "":
			in.tooltip += "\n" + st.Notice
		}
		for _, line := range others {
			in.tooltip += "\n" + line
		}
		in.accessKey = strings.ToLower(in.name + " " + in.agent + " " + in.repo + " " + in.loc + " " + strings.Join(others, " "))
		out = append(out, in)
	}
	return out
}

// agentLabel is the tab's name, else its agent's, in bold, then "+N" for
// the agents in its other panes.
func (in tabInfo) agentLabel() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 4)
	name := in.name
	if name == "" {
		name = in.agent
	}
	box.Append(label(name, "agent"))
	if in.more > 0 {
		box.Append(label(fmt.Sprintf("+%d", in.more), "more"))
	}
	return box
}

// switchTitle is a switcher row's first line: where the tab works, as
// the tab's name, else its repository, else its directory's base name,
// then the branch.
func (in tabInfo) switchTitle() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 8)
	title := in.name
	if title == "" {
		title = in.st.Repo
	}
	if title == "" && in.path != "" {
		title = filepath.Base(in.path)
	}
	if title == "" {
		title = in.agent
	}
	t := label(title, "swtitle")
	t.SetEllipsize(pango.EllipsizeEnd)
	box.Append(t)
	if in.st.Branch != "" {
		b := label("⎇ "+in.st.Branch, "swbranch")
		b.SetEllipsize(pango.EllipsizeEnd)
		box.Append(b)
	}
	return box
}

// switchDetail is a switcher row's second line: the agent, "+N" for the
// agents in other panes, and the directory, cut in the middle.
func (in tabInfo) switchDetail() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.Append(label(in.agent, "swagent"))
	if in.more > 0 {
		box.Append(label(fmt.Sprintf("+%d", in.more), "more"))
	}
	if in.path != "" {
		box.Append(label("·", "swpath"))
		p := label(in.path, "swpath")
		p.SetEllipsize(pango.EllipsizeMiddle)
		p.SetXAlign(0)
		p.SetHExpand(true)
		box.Append(p)
	}
	return box
}

// passTag marks a tab whose pane sends all keys to the terminal.
func passTag() *gtk.Label {
	l := label("PASS", "passtag")
	l.SetVAlign(gtk.AlignCenter)
	l.SetHAlign(gtk.AlignCenter)
	return l
}

func label(text string, classes ...string) *gtk.Label {
	l := gtk.NewLabel(text)
	for _, c := range classes {
		l.AddCSSClass(c)
	}
	return l
}

func iconButton(icon string, size int, tooltip string, classes ...string) *gtk.Button {
	b := gtk.NewButton()
	img := gtk.NewImageFromIconName(icon)
	img.SetPixelSize(size)
	b.SetChild(img)
	b.SetTooltipText(tooltip)
	for _, c := range classes {
		b.AddCSSClass(c)
	}
	b.SetFocusOnClick(false)
	return b
}

// closeWindowButton is the round close button GNOME users expect.
func (a *App) closeWindowButton(size int) *gtk.Button {
	b := gtk.NewButton()
	b.AddCSSClass("chrome")
	b.SetSizeRequest(size, 34)
	img := gtk.NewImageFromIconName("window-close-symbolic")
	img.SetPixelSize(12)
	circle := gtk.NewBox(gtk.OrientationHorizontal, 0)
	circle.AddCSSClass("closewin")
	circle.SetHAlign(gtk.AlignCenter)
	circle.SetVAlign(gtk.AlignCenter)
	img.SetHExpand(true)
	circle.Append(img)
	b.SetChild(circle)
	b.SetTooltipText("Close window")
	b.SetFocusOnClick(false)
	// The icon expands to centre in the circle; without this the button
	// inherits that and takes a share of the bar's spare width.
	b.SetHExpand(false)
	b.ConnectClicked(func() { a.win.Close() })
	a.closeWinBtn = b
	return b
}

func (a *App) optionsButton(width int) *gtk.MenuButton {
	m := gtk.NewMenuButton()
	m.SetIconName("open-menu-symbolic")
	m.AddCSSClass("chrome")
	m.SetSizeRequest(width, 34)
	m.SetTooltipText("Options")
	m.SetFocusOnClick(false)
	m.SetPopover(a.buildMenu())
	a.optionsBtn = m
	return m
}

func dragArea(horizontal bool) *gtk.WindowHandle {
	h := gtk.NewWindowHandle()
	filler := gtk.NewBox(gtk.OrientationHorizontal, 0)
	h.SetChild(filler)
	h.SetTooltipText("Drag to move the window")
	if horizontal {
		h.SetHExpand(true)
		h.SetSizeRequest(24, -1)
	} else {
		h.SetVExpand(true)
	}
	return h
}

// buildChrome creates the tab chrome for the current layout and puts it in
// the root box: a 44 px bar on top, or a rail on the left (48 px collapsed,
// 268 px expanded).
func (a *App) buildChrome() {
	if a.chrome != nil {
		a.rootBox.Remove(a.chrome)
	}
	a.tabsBox, a.chromeKey = nil, ""
	if a.layout == "bar" {
		a.rootBox.SetOrientation(gtk.OrientationVertical)
		bar := gtk.NewBox(gtk.OrientationHorizontal, 4)
		bar.AddCSSClass("bunk-bar")
		a.tabsBox = gtk.NewBox(gtk.OrientationHorizontal, 2)
		a.tabsBox.SetVAlign(gtk.AlignCenter)
		a.tabsBox.SetHExpand(false) // tabs keep their own width; the drag area takes the rest
		plus := iconButton("list-add-symbolic", 16, a.withKeys("New tab", "new_tab"), "chrome")
		plus.SetVAlign(gtk.AlignCenter)
		plus.ConnectClicked(func() { a.newTab("", a.cwdForNew()) })
		a.newTabBtn = plus
		bar.Append(a.tabsBox)
		bar.Append(plus)
		bar.Append(dragArea(true))
		opts := a.optionsButton(34)
		opts.SetVAlign(gtk.AlignCenter)
		bar.Append(opts)
		cw := a.closeWindowButton(34)
		cw.SetVAlign(gtk.AlignCenter)
		bar.Append(cw)
		a.chrome = bar
	} else {
		a.rootBox.SetOrientation(gtk.OrientationHorizontal)
		rail := gtk.NewBox(gtk.OrientationVertical, 4)
		rail.AddCSSClass("bunk-rail")
		rail.SetHExpand(false) // children that expand must not widen the rail
		if a.railOpen {
			rail.AddCSSClass("expanded")
			rail.SetSizeRequest(268, -1)
			rail.SetSpacing(2)
			head := gtk.NewBox(gtk.OrientationHorizontal, 4)
			head.SetSizeRequest(-1, 40)
			newTab := gtk.NewButton()
			nb := gtk.NewBox(gtk.OrientationHorizontal, 6)
			img := gtk.NewImageFromIconName("list-add-symbolic")
			img.SetPixelSize(16)
			nb.Append(img)
			nb.Append(gtk.NewLabel("New tab"))
			newTab.SetChild(nb)
			newTab.AddCSSClass("chrome")
			newTab.AddCSSClass("wide")
			newTab.SetTooltipText(a.withKeys("New tab", "new_tab"))
			newTab.SetFocusOnClick(false)
			newTab.ConnectClicked(func() { a.newTab("", a.cwdForNew()) })
			a.newTabBtn = newTab
			head.Append(newTab)
			head.Append(dragArea(true))
			head.Append(a.closeWindowButton(34))
			rail.Append(head)
			a.tabsBox = gtk.NewBox(gtk.OrientationVertical, 2)
			rail.Append(a.tabsBox)
			spacer := dragArea(false)
			rail.Append(spacer)
			// The options button sits bottom left in both rail states, so
			// it stays under the pointer when the rail opens or closes.
			foot := gtk.NewBox(gtk.OrientationHorizontal, 4)
			opts := a.optionsButton(34)
			opts.SetDirection(gtk.ArrowUp)
			opts.SetVAlign(gtk.AlignEnd)
			foot.Append(opts)
			hint := label(a.railHint(), "hint")
			hint.SetWrap(true)
			hint.SetMaxWidthChars(24) // a wrapping label is as wide as its text otherwise
			hint.SetXAlign(0)
			hint.SetHExpand(true)
			foot.Append(hint)
			rail.Append(foot)
		} else {
			rail.AddCSSClass("collapsed")
			rail.SetSizeRequest(48, -1)
			cw := a.closeWindowButton(40)
			cw.SetHAlign(gtk.AlignCenter)
			rail.Append(cw)
			sep := gtk.NewBox(gtk.OrientationHorizontal, 0)
			sep.AddCSSClass("railsep")
			sep.SetHAlign(gtk.AlignCenter)
			rail.Append(sep)
			a.tabsBox = gtk.NewBox(gtk.OrientationVertical, 4)
			a.tabsBox.SetHAlign(gtk.AlignCenter)
			rail.Append(a.tabsBox)
			rail.Append(dragArea(false))
			plus := iconButton("list-add-symbolic", 16, a.withKeys("New tab", "new_tab"), "chrome")
			plus.SetSizeRequest(40, 40)
			plus.SetHAlign(gtk.AlignCenter)
			plus.ConnectClicked(func() { a.newTab("", a.cwdForNew()) })
			a.newTabBtn = plus
			rail.Append(plus)
			opts := a.optionsButton(40)
			opts.SetDirection(gtk.ArrowUp)
			opts.SetHAlign(gtk.AlignCenter)
			rail.Append(opts)
		}
		a.chrome = rail
	}
	a.rootBox.Prepend(a.chrome)
	a.tabDrag = nil
	a.watchTabDrag()
	a.watchTabRename()
	a.refreshChrome()
}

func (a *App) cwdForNew() string {
	if p := a.activePane(); p != nil {
		return p.s.Cwd()
	}
	return ""
}

// refreshChrome rebuilds the tab items from the panes' cached status. It
// runs on events (tab changes, focus, StatusChanged), never per frame, and
// does nothing when no shown text, state or selection changed (a status
// report about a field the chrome does not show, such as a short-lived
// foreground process of a hidden tab). The desktop notifications follow
// the same events (alerts.go).
func (a *App) refreshChrome() {
	a.syncAlerts()
	if a.tabsBox == nil {
		return
	}
	if d := a.tabDrag; d != nil {
		d.stale = true // a tab is pressed (tabdrag.go); its release refreshes
		return
	}
	infos := a.tabInfos()
	var key strings.Builder
	key.WriteString(a.settings.RailStyle)
	for _, in := range infos {
		fmt.Fprintf(&key, "%p|%s|%v|%s|%d|%s|%s|%d|%s|%s|%s|%s|%s|%v|%v|%v\x00", in.t, in.look, in.arcs, in.paneMap, in.panes, in.name, in.agent, in.more, in.repo, in.path, in.loc, in.notice, in.tooltip, in.active, in.needs, in.pass)
	}
	if key.String() == a.chromeKey {
		return
	}
	a.chromeKey = key.String()
	a.chromeGen++
	for c := a.tabsBox.FirstChild(); c != nil; c = a.tabsBox.FirstChild() {
		a.tabsBox.Remove(c)
	}
	a.tabItems = a.tabItems[:0]
	for _, in := range infos {
		var it tabItem
		switch {
		case a.layout == "bar":
			it = a.barTab(in)
		case a.railOpen:
			it = a.railRow(in)
		default:
			it = a.railDot(in)
		}
		a.tabsBox.Append(it.item)
		a.tabItems = append(a.tabItems, it)
	}
	if a.switcher != nil {
		a.switcher.refresh()
	}
}

// tabItem is one tab's widget in the chrome and its close button (nil in
// the collapsed rail).
type tabItem struct{ item, close gtk.Widgetter }

func (a *App) barTab(in tabInfo) tabItem {
	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.AddCSSClass("tab")
	if in.active {
		box.AddCSSClass("active")
	}
	if in.needs {
		box.AddCSSClass("needs")
	}
	box.SetSizeRequest(132, 34)
	box.SetTooltipText(in.tooltip)
	inner := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inner.Append(label(in.look.dot, "dot", in.look.cls))
	inner.Append(in.agentLabel())
	if in.pass {
		inner.Append(passTag())
	}
	repo := label(in.repo, "repo")
	repo.SetEllipsize(pango.EllipsizeEnd)
	repo.SetMaxWidthChars(22)
	repo.SetXAlign(0)
	repo.SetHExpand(true)
	inner.Append(repo)
	if in.locGlyph != "" {
		inner.Append(label(in.locGlyph, "loc"))
	}
	btn := gtk.NewButton()
	btn.AddCSSClass("tabbtn")
	btn.SetChild(inner)
	btn.SetHExpand(true)
	btn.SetFocusOnClick(false)
	t := in.t
	btn.ConnectClicked(func() { a.selectTab(t) })
	box.Append(btn)
	cl := iconButton("window-close-symbolic", 14, "Close tab", "tabclose")
	cl.SetVAlign(gtk.AlignCenter)
	cl.ConnectClicked(func() { a.closeTab(t) })
	box.Append(cl)
	return tabItem{box, cl}
}

func (a *App) railDot(in tabInfo) tabItem {
	b := gtk.NewButton()
	ring := a.ringShown(in)
	if !ring && in.paneMap == "" && in.panes <= railMapMax && !in.pass {
		b.SetLabel(in.look.dot)
	} else {
		box := gtk.NewBox(gtk.OrientationVertical, 1)
		box.SetVAlign(gtk.AlignCenter)
		count := in.panes > railMapMax
		switch {
		case ring:
			box.Append(railRing(in.arcs))
			count = in.panes > railRingCount
		case in.paneMap != "":
			m := railMap(in.t.root)
			m.AddCSSClass("railmap")
			m.SetVExpand(false) // a cell's expand must not stretch the rail item
			box.Append(m)
		default:
			box.Append(gtk.NewLabel(in.look.dot))
		}
		if count {
			box.Append(label(strconv.Itoa(in.panes), "panecount"))
		}
		if in.pass {
			box.Append(passTag())
		}
		b.SetChild(box)
	}
	b.AddCSSClass("raildot")
	b.AddCSSClass(in.look.cls)
	if in.active {
		b.AddCSSClass("active")
	}
	if in.needs {
		b.AddCSSClass("needs")
	}
	b.SetTooltipText(in.tooltip)
	b.SetFocusOnClick(false)
	t := in.t
	b.ConnectClicked(func() { a.selectTab(t) })
	return tabItem{b, nil}
}

// ringShown reports whether the collapsed rail draws the tab as a ring
// of its panes (railring.go): every tab, unless rail_style asks for the
// map.
func (a *App) ringShown(in tabInfo) bool {
	return in.panes > 0 && a.settings.RailStyle != "map"
}

// railMapMax is the most panes the collapsed rail draws as a map of
// their tab. A tab with more shows its most urgent dot over the count.
const railMapMax = 4

// mapDot is a pane's dot in the map of its tab: its agent's state, else
// its command mark's, else the hollow dot of a plain shell, as the tab's
// dot shows a tab of one shell.
func mapDot(p *Pane) look {
	if p.status().Agent == "" && p.mark.State != session.AgentNone {
		return commandLook(p.mark.State)
	}
	return lookOf(p.status().State)
}

// paneMap writes the panes under n as the collapsed rail draws them:
// each pane's dot, "(a|b)" for panes side by side and "(a/b)" for panes
// stacked. The report shows it as the tab's map.
func paneMap(n *node, dot func(*Pane) look) string {
	if n.pane != nil {
		return dot(n.pane).dot
	}
	sep := "|"
	if n.vertical {
		sep = "/"
	}
	return "(" + paneMap(n.a, dot) + sep + paneMap(n.b, dot) + ")"
}

// railMap draws the panes under n as cells placed like the panes: each
// split halves its space, whatever its divider's position, so the map
// keeps its shape while a divider moves. A cell shows its pane's dot in
// the state's colour.
func railMap(n *node) *gtk.Box {
	if n.pane != nil {
		l := mapDot(n.pane)
		cell := gtk.NewBox(gtk.OrientationHorizontal, 0)
		cell.AddCSSClass("mapcell")
		d := label(l.dot, l.cls)
		d.SetHExpand(true)
		cell.Append(d)
		return cell
	}
	box := gtk.NewBox(gtk.OrientationHorizontal, 2)
	if n.vertical {
		box.SetOrientation(gtk.OrientationVertical)
	}
	box.SetHomogeneous(true)
	box.Append(railMap(n.a))
	box.Append(railMap(n.b))
	return box
}

func (a *App) railRow(in tabInfo) tabItem {
	row := gtk.NewBox(gtk.OrientationHorizontal, 0)
	row.AddCSSClass("row")
	if in.active {
		row.AddCSSClass("active")
	}
	if in.needs {
		row.AddCSSClass("needs")
	}
	row.SetTooltipText(in.tooltip)
	lines := gtk.NewBox(gtk.OrientationVertical, 3)
	top := gtk.NewBox(gtk.OrientationHorizontal, 8)
	top.Append(label(in.look.dot, "dot", in.look.cls))
	top.Append(in.agentLabel())
	if in.pass {
		top.Append(passTag())
	}
	state := label(in.look.label, "statelabel")
	if in.needs {
		state.AddCSSClass("needs-fg")
	}
	state.SetHExpand(true)
	state.SetXAlign(1)
	top.Append(state)
	lines.Append(top)
	add := func(text string, cls string) {
		l := label(text, cls)
		l.SetEllipsize(pango.EllipsizeEnd)
		l.SetMaxWidthChars(24) // keeps the rail at 268 px
		l.SetXAlign(0)
		l.SetMarginStart(22)
		lines.Append(l)
	}
	add(in.repo, "repo")
	if in.loc != "" {
		add(in.loc, "loc")
	}
	if in.notice != "" {
		add(in.notice, "notice")
	}
	btn := gtk.NewButton()
	btn.AddCSSClass("rowbtn")
	btn.SetChild(lines)
	btn.SetHExpand(true)
	btn.SetFocusOnClick(false)
	t := in.t
	btn.ConnectClicked(func() { a.selectTab(t) })
	row.Append(btn)
	cl := iconButton("window-close-symbolic", 14, "Close tab", "tabclose")
	cl.SetVAlign(gtk.AlignStart)
	cl.SetMarginTop(8)
	cl.ConnectClicked(func() { a.closeTab(t) })
	row.Append(cl)
	return tabItem{row, cl}
}

// setLayout switches between the bar and the rail at runtime (options menu).
func (a *App) setLayout(layout string, railOpen bool) {
	a.layout, a.railOpen = layout, railOpen
	if a.optionsBtn != nil {
		a.optionsBtn.Popdown()
	}
	a.buildChrome()
	if p := a.activePane(); p != nil && !a.modalOpen() && a.find == nil {
		p.widget.GrabFocus()
	}
}

func (a *App) toggleFullscreen() {
	if a.win.IsFullscreen() {
		a.win.Unfullscreen()
	} else {
		a.win.Fullscreen()
	}
}

// ---- options menu ----

type menuItem struct {
	label, action string // action: the bound action whose keys the item shows
	run           func()
	check         bool // a check item (passthrough)
}

// firstKey is the first key bound to an action, as menus show it.
func (a *App) firstKey(action string) string {
	if cs := a.keys().bind[action]; len(cs) > 0 {
		return cs[0].Label()
	}
	return ""
}

func (a *App) withKeys(text, action string) string {
	if k := a.firstKey(action); k != "" {
		return text + " · " + k
	}
	return text
}

// railHint lists the rail's main keys, as bound.
func (a *App) railHint() string {
	var parts []string
	for _, h := range []struct{ action, what string }{
		{"toggle_rail", "rail"}, {"next_needs_you", "next needing you"}, {"switcher", "switch"},
	} {
		if k := a.firstKey(h.action); k != "" {
			parts = append(parts, k+" "+h.what)
		}
	}
	if k := a.firstKey("select_tab_1"); k != "" {
		parts = append(parts, k+"…9 tab")
	}
	return strings.Join(parts, " · ")
}

func (a *App) menuGroups() [][]menuItem {
	layout := []menuItem{}
	if a.layout == "rail" {
		if a.railOpen {
			layout = append(layout, menuItem{label: "Collapse rail", action: "toggle_rail", run: func() { a.setLayout("rail", false) }})
		} else {
			layout = append(layout, menuItem{label: "Expand rail", action: "toggle_rail", run: func() { a.setLayout("rail", true) }})
		}
		layout = append(layout, menuItem{label: "Tabs on top", run: func() { a.setLayout("bar", a.railOpen) }})
	} else {
		layout = append(layout, menuItem{label: "Tabs on the side", run: func() { a.setLayout("rail", a.railOpen) }})
	}
	layout = append(layout,
		menuItem{label: "Switch tab", action: "switcher", run: a.openSwitcher},
		menuItem{label: "Next needing you", action: "next_needs_you", run: a.jumpNeedingYou})
	return [][]menuItem{
		{
			{label: "New tab", action: "new_tab", run: func() { a.newTab("", a.cwdForNew()) }},
			{label: "New window", action: "new_window", run: a.openWindow},
			{label: "Rename tab…", action: "rename_tab", run: func() { a.renameTab(a.active) }},
			{label: "Close tab", action: "close_tab", run: func() { a.closeTab(a.active) }},
		},
		{
			{label: "Split pane", action: "split", run: func() { a.splitActive(false) }},
			{label: "Split in same container", action: "split_context", run: func() { a.splitActive(true) }},
			{label: "Zoom pane", action: "zoom", run: func() { a.active.toggleZoom(a.activePane()) }},
			{label: "Send all keys to terminal", action: "passthrough", check: true},
			{label: "Find", action: "search", run: a.openFind},
		},
		layout,
		{
			{label: "Preferences", action: "preferences", run: a.showPreferences},
			{label: "Keyboard shortcuts", action: "shortcuts", run: a.showShortcuts},
			{label: "About bunk-gui", run: a.showAbout},
			{label: "Quit", action: "quit", run: a.requestQuit},
		},
	}
}

func (a *App) buildMenu() *gtk.Popover {
	pop := gtk.NewPopover()
	pop.AddCSSClass("bunk-menu")
	pop.SetHasArrow(false)
	pop.ConnectClosed(func() {
		// An item that opened a dialog keeps the focus there.
		if p := a.activePane(); p != nil && a.switcher == nil && !a.modalOpen() {
			p.widget.GrabFocus()
		}
	})
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.SetSizeRequest(308, -1)
	size := gtk.NewBox(gtk.OrientationHorizontal, 6)
	size.SetMarginBottom(8)
	size.SetMarginTop(4)
	sl := label("Text size", "menusize")
	sl.SetHExpand(true)
	sl.SetXAlign(0)
	size.Append(sl)
	minus := gtk.NewButton()
	minus.SetLabel("−")
	minus.AddCSSClass("sizebtn")
	minus.SetTooltipText(a.withKeys("Smaller text", "font_smaller"))
	plus := gtk.NewButton()
	plus.SetLabel("+")
	plus.AddCSSClass("sizebtn")
	plus.SetTooltipText(a.withKeys("Larger text", "font_bigger"))
	a.zoomLabel = label(a.zoomText(), "sizelabel")
	minus.ConnectClicked(func() { a.zoom(-1) })
	plus.ConnectClicked(func() { a.zoom(+1) })
	a.menuButtons = map[string]*gtk.Button{"−": minus, "+": plus}
	size.Append(minus)
	size.Append(a.zoomLabel)
	size.Append(plus)
	box.Append(size)
	for _, group := range a.menuGroups() {
		g := gtk.NewBox(gtk.OrientationVertical, 0)
		g.AddCSSClass("menugroup")
		for _, it := range group {
			row := gtk.NewBox(gtk.OrientationHorizontal, 12)
			l := gtk.NewLabel(it.label)
			l.SetHExpand(true)
			l.SetXAlign(0)
			row.Append(l)
			row.Append(label(a.firstKey(it.action), "keys"))
			if it.check {
				// Send all keys to terminal: follows the focused pane.
				c := gtk.NewCheckButton()
				c.AddCSSClass("menucheck")
				c.SetChild(row)
				c.ConnectToggled(func() {
					if p := a.activePane(); p != nil && !a.syncingMenu && c.Active() != p.passthrough {
						a.setPassthrough(p, c.Active())
						pop.Popdown()
					}
				})
				a.passCheck = c
				g.Append(c)
				continue
			}
			b := gtk.NewButton()
			b.AddCSSClass("menuitem")
			b.SetChild(row)
			run := it.run
			b.ConnectClicked(func() {
				pop.Popdown()
				run()
			})
			a.menuButtons[it.label] = b
			g.Append(b)
		}
		box.Append(g)
	}
	pop.ConnectShow(a.syncPassCheck)
	pop.SetChild(box)
	a.menuBox = box
	return pop
}

func (a *App) zoomText() string {
	return fmt.Sprintf("%d%%", int(a.fontSize/a.settings.FontSize*100+0.5))
}

// zoom changes the text size by one step (Ctrl+= / Ctrl+- / Ctrl+0 for 0).
func (a *App) zoom(dir int) {
	base := a.settings.FontSize
	switch {
	case dir == 0:
		a.fontSize = a.settings.startSize()
	case dir > 0:
		a.fontSize = min(base*2, a.fontSize+base/10)
	default:
		a.fontSize = max(base*0.6, a.fontSize-base/10)
	}
	a.fontSize = math.Round(a.fontSize*10) / 10
	a.fontDesc = a.font()
	if a.zoomLabel != nil {
		a.zoomLabel.SetText(a.zoomText())
	}
	for _, p := range a.panes {
		if p != nil && !p.removeOnce {
			p.queueRender() // render rebuilds the atlas for the new size
		}
	}
}

func (a *App) showAbout() {
	d := adw.NewAboutDialog()
	d.SetApplicationName("bunk-gui")
	d.SetApplicationIcon("utilities-terminal")
	d.SetVersion(Version)
	d.SetComments("A GTK terminal for coding agents, on bunk's session core.")
	d.SetDeveloperName("bunk")
	d.ConnectClosed(func() { a.about = nil })
	a.about = d
	d.Present(a.win)
}

func (a *App) toast(text string) {
	if a.toasts == nil {
		fmt.Fprintln(os.Stderr, "bunk-gui:", text)
		return
	}
	t := adw.NewToast(text)
	t.SetTimeout(3)
	a.toasts.AddToast(t)
	a.lastToast, a.toastButton, a.toastAct = text, "", nil
	a.toastLog = append(a.toastLog, text)
}

// ---- switcher ----

type switcher struct {
	shown []*Tab // rows in the order shown
	rows  []*gtk.Button
	a     *App
	bg    *gtk.Box
	entry *gtk.Entry
	list  *gtk.Box
	cur   int // the row Enter picks; Up and Down move it
}

var stateOrder = map[session.AgentState]int{
	session.AgentNeedsYou: 0, session.AgentDone: 1, session.AgentFailed: 2, session.AgentWorking: 3, session.AgentIdle: 4,
	session.AgentNone: 5,
}

// openSwitcher shows the tab switcher: a filter field and the tabs, those
// needing you first. Up and Down move the cursor, which starts on the
// first match; Enter picks its tab; Esc closes.
func (a *App) openSwitcher() {
	if a.switcher != nil {
		a.closeSwitcher()
		return
	}
	sw := &switcher{a: a}
	sw.bg = gtk.NewBox(gtk.OrientationVertical, 0)
	sw.bg.AddCSSClass("switcher-bg")
	sw.bg.SetHExpand(true)
	sw.bg.SetVExpand(true)
	dlg := gtk.NewBox(gtk.OrientationVertical, 4)
	dlg.AddCSSClass("switcher")
	dlg.SetHAlign(gtk.AlignCenter)
	dlg.SetVAlign(gtk.AlignStart)
	dlg.SetMarginTop(96)
	dlg.SetSizeRequest(600, -1)
	title := label("Switch to agent, repo or branch", "switcher-title")
	title.SetXAlign(0)
	dlg.Append(title)
	sw.entry = gtk.NewEntry()
	sw.entry.AddCSSClass("switcher-entry")
	sw.entry.SetPlaceholderText("Type to filter")
	sw.entry.ConnectChanged(func() {
		sw.shown = sw.shown[:0] // a new filter puts the cursor on its first match
		sw.refresh()
	})
	sw.entry.ConnectActivate(sw.pick)
	dlg.Append(sw.entry)
	sw.list = gtk.NewBox(gtk.OrientationVertical, 0)
	dlg.Append(sw.list)
	cl := gtk.NewButton()
	cl.SetLabel("Close · Esc")
	cl.AddCSSClass("switchclose")
	cl.SetHAlign(gtk.AlignEnd)
	cl.ConnectClicked(a.closeSwitcher)
	dlg.Append(cl)
	sw.bg.Append(dlg)
	// The capture phase sees Up and Down before the entry, whose
	// keyboard navigation would move the focus out of it.
	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, _ uint, _ gdk.ModifierType) bool {
		switch keyval {
		case gdk.KEY_Escape:
			a.closeSwitcher()
		case gdk.KEY_Up, gdk.KEY_KP_Up:
			sw.move(-1)
		case gdk.KEY_Down, gdk.KEY_KP_Down:
			sw.move(+1)
		default:
			return false
		}
		return true
	})
	sw.bg.AddController(keys)
	a.switcher = sw
	sw.refresh()
	a.overlay.AddOverlay(sw.bg)
	sw.entry.GrabFocus()
}

// pick selects the tab under the cursor (Enter).
func (sw *switcher) pick() {
	if sw.cur < len(sw.shown) {
		t := sw.shown[sw.cur]
		sw.a.closeSwitcher()
		sw.a.selectTab(t)
	}
}

// move steps the cursor by d rows, wrapping at either end.
func (sw *switcher) move(d int) {
	n := len(sw.rows)
	if n == 0 {
		return
	}
	sw.rows[sw.cur].RemoveCSSClass("cursor")
	sw.cur = (sw.cur + d + n) % n
	sw.rows[sw.cur].AddCSSClass("cursor")
}

func (a *App) closeSwitcher() {
	if a.switcher == nil {
		return
	}
	a.overlay.RemoveOverlay(a.switcher.bg)
	a.switcher = nil
	if p := a.activePane(); p != nil {
		p.widget.GrabFocus()
	}
}

func (sw *switcher) refresh() {
	a := sw.a
	for c := sw.list.FirstChild(); c != nil; c = sw.list.FirstChild() {
		sw.list.Remove(c)
	}
	// A status change reorders the rows; the cursor stays on its tab.
	var cur *Tab
	if sw.cur < len(sw.shown) {
		cur = sw.shown[sw.cur]
	}
	f := strings.ToLower(strings.TrimSpace(sw.entry.Text()))
	infos := a.tabInfos()
	sort.SliceStable(infos, func(i, j int) bool { return stateOrder[infos[i].state] < stateOrder[infos[j].state] })
	sw.cur, sw.shown, sw.rows = 0, sw.shown[:0], sw.rows[:0]
	for _, in := range infos {
		if f != "" && !strings.Contains(in.accessKey, f) {
			continue
		}
		if in.t == cur {
			sw.cur = len(sw.shown)
		}
		sw.shown = append(sw.shown, in.t)
		b := gtk.NewButton()
		b.AddCSSClass("switchrow")
		if in.active {
			b.AddCSSClass("active")
		}
		row := gtk.NewBox(gtk.OrientationHorizontal, 10)
		dot := label(in.look.dot, "dot", in.look.cls)
		dot.SetVAlign(gtk.AlignStart)
		row.Append(dot)
		text := gtk.NewBox(gtk.OrientationVertical, 2)
		text.SetHExpand(true)
		text.Append(in.switchTitle())
		text.Append(in.switchDetail())
		if in.notice != "" {
			n := label(in.notice, "notice")
			n.SetEllipsize(pango.EllipsizeEnd)
			n.SetXAlign(0)
			text.Append(n)
		}
		row.Append(text)
		row.Append(label(in.loc, "loc"))
		st := label(in.look.label, "statelabel")
		if in.needs {
			st.AddCSSClass("needs-fg")
		}
		row.Append(st)
		b.SetChild(row)
		t := in.t
		b.ConnectClicked(func() {
			a.closeSwitcher()
			a.selectTab(t)
		})
		sw.list.Append(b)
		sw.rows = append(sw.rows, b)
	}
	if len(sw.rows) > 0 {
		sw.rows[sw.cur].AddCSSClass("cursor")
	} else {
		none := label("No tab matches.", "statelabel")
		none.SetXAlign(0)
		none.SetMarginStart(8)
		sw.list.Append(none)
	}
}
