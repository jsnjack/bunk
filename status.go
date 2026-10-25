// status.go - foreground process tracking and container/context detection.
//
// For each pane we poll the foreground process group of its PTY every second
// by reading /proc/<shellPid>/stat (field tpgid = terminal foreground PGID),
// then reading /proc/<tpgid>/comm for the process name.
//
// Container context is detected at pane startup by reading the shell process's
// /proc/<pid>/environ.  Toolbox, Distrobox, Podman, and LXD each leave
// distinctive markers in the environment or filesystem.
//
// The status badge is rendered last in each render pass (on top of everything
// else) in the top-right corner of each pane.  It shows:
//
//	◐ claude needs you  - the agent in the pane and its state (session.Status)
//	⬡ <container-name>  - Toolbox / Podman container
//	▣ <container-name>  - Distrobox container
//	ssh / sudo / su / root - notable foreground process
package main

import (
	"bunk/internal/session"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// ---------------------------------------------------------------------------
// Status badge rendering
// ---------------------------------------------------------------------------

// drawAllPaneStatus renders the status badge for every pane in the subtree.
// Must be called AFTER all other drawing so the badge paints on top.
func drawAllPaneStatus(scr tcell.Screen, n *Node, active *Pane, rt resolvedTheme, zoomed bool) {
	if n.isLeaf() {
		drawPaneStatus(scr, n.pane, n.pane == active, rt, zoomed)
		return
	}
	drawAllPaneStatus(scr, n.left, active, rt, zoomed)
	drawAllPaneStatus(scr, n.right, active, rt, zoomed)
}

// statusOverlayKeyLocked returns a stable signature for the badge content that
// overlays the pane's top row. renderPane uses this to force a repaint when
// status badges change even if vt10x itself has not marked any rows dirty.
//
// p.mu must be held by the caller.
func statusOverlayKeyLocked(p *Pane, now time.Time) string {
	tempActive := !p.statusMsgEnd.IsZero() && now.Before(p.statusMsgEnd) && p.statusMsg != ""
	return fmt.Sprintf("%t|%t|%s|%s|%s|%s|%s|%s|%d|%s",
		p.passthrough,
		tempActive,
		p.statusMsg,
		p.FgProcess,
		p.ContainerType,
		p.ContainerID,
		p.SSHHost,
		p.agent.name,
		p.agent.state,
		p.agent.notice,
	)
}

// Agent state colours, shared with bunk-gui's chrome so a state looks the
// same in both front ends. They do not follow the theme.
var (
	colorAgentBadge   = tcell.NewRGBColor(0x24, 0x26, 0x2b) // badge background for quiet states
	colorAgentInk     = tcell.NewRGBColor(0x13, 0x14, 0x16) // text on the needs-you and done badges
	colorAgentWorking = tcell.NewRGBColor(0x5a, 0xa9, 0xff)
	colorAgentNeeds   = tcell.NewRGBColor(0xff, 0xb4, 0x54)
	colorAgentIdle    = tcell.NewRGBColor(0x9a, 0xa0, 0xa8)
	colorAgentFailed  = tcell.NewRGBColor(0xff, 0x8a, 0x8a)
	colorAgentDone    = tcell.NewRGBColor(0x7e, 0xe2, 0xa8)
)

// agentLook is how a pane shows its agent's state: a glyph and a label on
// the badge, the badge colours, and for states that want the user, the
// colour of the separators around the pane.
type agentLook struct {
	dot, label string
	style      tcell.Style
	border     tcell.Color // tcell.ColorDefault keeps the normal separator colour
}

// agentLookOf returns the look of state; false for a pane without an agent.
func agentLookOf(state session.AgentState) (agentLook, bool) {
	quiet := tcell.StyleDefault.Background(colorAgentBadge)
	loud := tcell.StyleDefault.Foreground(colorAgentInk).Bold(true)
	switch state {
	case session.AgentWorking:
		return agentLook{"●", "working", quiet.Foreground(colorAgentWorking), tcell.ColorDefault}, true
	case session.AgentIdle:
		return agentLook{"○", "idle", quiet.Foreground(colorAgentIdle), tcell.ColorDefault}, true
	case session.AgentNeedsYou:
		return agentLook{"◐", "needs you", loud.Background(colorAgentNeeds), colorAgentNeeds}, true
	case session.AgentDone:
		return agentLook{"✓", "done", loud.Background(colorAgentDone), colorAgentDone}, true
	case session.AgentFailed:
		return agentLook{"✕", "failed", quiet.Foreground(colorAgentFailed).Bold(true), tcell.ColorDefault}, true
	}
	return agentLook{}, false
}

const (
	// noticeSep joins the agent badge and its notice.
	noticeSep = " · "
	// minNoticeCells is the narrowest notice worth showing.
	minNoticeCells = 8
)

// paintAgentBorders colours the separators around every pane whose agent
// needs the user or is done, over the active-pane colour. Needs-you wins
// where a done pane and a needs-you pane share a separator. Call it under
// app.mu, after renderPane has read every pane's status.
func paintAgentBorders(scr tcell.Screen, root *Node, rt resolvedTheme) {
	if root == nil || root.isLeaf() {
		return
	}
	leaves := root.leaves()
	for _, want := range []session.AgentState{session.AgentDone, session.AgentNeedsYou} {
		look, _ := agentLookOf(want)
		style := tcell.StyleDefault.Foreground(look.border).Background(rt.bg)
		for _, leaf := range leaves {
			p := leaf.pane
			p.Lock()
			state := p.agent.state
			p.Unlock()
			if state == want {
				paintPaneEdges(scr, root, p, style)
			}
		}
	}
}

// paintPaneEdges re-colours the parts of the separators under n that touch
// pane p: a vertical separator next to p's left or right edge, a horizontal
// one next to its top or bottom edge, over the rows or columns p spans.
func paintPaneEdges(scr tcell.Screen, n *Node, p *Pane, style tcell.Style) {
	if n.isLeaf() {
		return
	}
	if n.dir == splitVertical {
		bx := n.left.x + n.left.w
		if p.x+p.w == bx || p.x == bx+1 {
			for y := max(n.y, p.y); y < min(n.y+n.h, p.y+p.h); y++ {
				scr.SetContent(bx, y, tcell.RuneVLine, nil, style)
			}
		}
	} else {
		by := n.left.y + n.left.h
		if p.y+p.h == by || p.y == by+1 {
			for x := max(n.x, p.x); x < min(n.x+n.w, p.x+p.w); x++ {
				scr.SetContent(x, by, tcell.RuneHLine, nil, style)
			}
		}
	}
	paintPaneEdges(scr, n.left, p, style)
	paintPaneEdges(scr, n.right, p, style)
}

// drawPaneStatus draws compact status badges in the top-right corner of
// pane p.  Badges are drawn right-to-left so the highest-priority badge
// (passthrough, then scroll count) is closest to the edge and always visible.
//
// Badge order (right to left):
//
//	[ PASS ]                - keyboard passthrough (toggle remains reserved)
//	[-N]                    - scrollback line count (yellow on black)
//	[ ◐ claude needs you ]  - the agent and its state, then its notice if room
//	[⬡ my-toolbox]          - container/sudo/ssh context badge
//	[ ZOOM ]                - the pane is zoomed
//	[ COPIED ]              - temporary flash message (white on green)
//
// A badge that does not fit hides itself and every badge after it. The
// agent badge drops the agent's name, then the label, before it gives up.
func drawPaneStatus(scr tcell.Screen, p *Pane, isActive bool, rt resolvedTheme, zoomed bool) {
	p.Lock()
	fgProc := p.FgProcess
	containerID := p.ContainerID
	containerType := p.ContainerType
	px, py, pw := p.x, p.y, p.w
	sbOff := p.ScrollOffset
	tempMsg := p.statusMsg
	tempActive := !p.statusMsgEnd.IsZero() && time.Now().Before(p.statusMsgEnd)
	sshHost := p.SSHHost
	passthrough := p.passthrough
	agent := p.agent
	p.Unlock()

	// Badge colors derived from the theme palette so they adapt automatically.
	// See the palette index table in config.go (ThemeDef).
	// Backgrounds are blended 40% toward the theme bg to soften intensity.
	dim := func(c tcell.Color) tcell.Color {
		r1, g1, b1 := c.RGB()
		r2, g2, b2 := rt.bg.RGB()
		mix := func(a, b int32) int32 { return (a*60 + b*40) / 100 }
		return tcell.NewRGBColor(mix(r1, r2), mix(g1, g2), mix(b1, b2))
	}
	badgeText := rt.palette[7]       // white — badge foreground
	colorRed := dim(rt.palette[1])   // sudo / su
	colorGreen := dim(rt.palette[2]) // success (COPIED)
	colorYellow := rt.palette[3]     // scroll count foreground (not dimmed)
	colorBlue := dim(rt.palette[4])  // ssh
	colorCyan := dim(rt.palette[6])  // container
	colorDark := rt.palette[0]       // scroll count background

	// A badge is a styled run of text in one of its forms.
	type badge struct {
		forms []string // widest first; the first that fits is drawn
		style tcell.Style
	}
	// Collect badges in priority order: the first is drawn closest to the
	// right edge.
	var badges []badge
	add := func(style tcell.Style, forms ...string) {
		badges = append(badges, badge{forms, style})
	}

	if passthrough {
		add(tcell.StyleDefault.Foreground(badgeText).Background(colorCyan).Bold(true), " PASS ")
	}

	if sbOff > 0 {
		add(tcell.StyleDefault.Foreground(colorYellow).Background(colorDark).Bold(true), fmt.Sprintf(" -%d ", sbOff))
	}

	agentAt := -1
	look, hasAgent := agentLookOf(agent.state)
	if hasAgent {
		agentAt = len(badges)
		add(look.style,
			" "+look.dot+" "+agent.name+" "+look.label+" ",
			" "+look.dot+" "+look.label+" ",
			" "+look.dot+" ")
	}

	// Container / SSH / sudo context badge.
	{
		var parts []string
		if containerType != "" {
			icon := "⬡"
			if containerType == "distrobox" {
				icon = "▣"
			}
			label := containerType
			if containerID != "" {
				label = containerID
			}
			parts = append(parts, icon+" "+label)
		}
		switch fgProc {
		case "ssh", "mosh":
			label := "⇄"
			if sshHost != "" {
				label += " " + sshHost
			} else {
				label += " " + fgProc
			}
			parts = append(parts, label)
		case "sudo":
			parts = append(parts, "sudo")
		case "su":
			parts = append(parts, "su")
		}
		if len(parts) > 0 {
			var bg tcell.Color
			switch {
			case fgProc == "sudo" || fgProc == "su":
				bg = colorRed
			case fgProc == "ssh":
				bg = colorBlue
			case containerType != "":
				bg = colorCyan
			default:
				bg = colorDark
			}
			add(tcell.StyleDefault.Foreground(badgeText).Background(bg).Bold(true), " "+strings.Join(parts, " · ")+" ")
		}
	}

	if zoomed {
		add(tcell.StyleDefault.Foreground(colorYellow).Background(colorDark).Bold(true), " ZOOM ")
	}

	// Temporary flash message (e.g. "COPIED").
	if tempActive && tempMsg != "" {
		add(tcell.StyleDefault.Foreground(badgeText).Background(colorGreen).Bold(true), " "+tempMsg+" ")
	}

	if len(badges) == 0 {
		return
	}

	rightEdge := px + pw
	if sbOff > 0 {
		rightEdge-- // reserve scrollbar column
	}
	room := rightEdge - px

	// Pick each badge's widest form that still fits, in priority order.
	texts := make([]string, 0, len(badges))
	used := 0
	for _, b := range badges {
		text := ""
		for _, f := range b.forms {
			if used+uniseg.StringWidth(f) <= room {
				text = f
				break
			}
		}
		if text == "" {
			break // no more room
		}
		texts = append(texts, text)
		used += uniseg.StringWidth(text)
	}

	// The agent's notice takes the room left over, up to two thirds of the
	// row, so the pane's own top line stays partly visible.
	if agentAt >= 0 && agentAt < len(texts) && agent.notice != "" && texts[agentAt] == badges[agentAt].forms[0] {
		spare := max(used, room*2/3) - used - uniseg.StringWidth(noticeSep)
		if spare >= minNoticeCells {
			notice := fitCells(agent.notice, spare)
			texts[agentAt] = strings.TrimSuffix(texts[agentAt], " ") + noticeSep + notice + " "
		}
	}

	// Draw right-to-left: the first badge goes closest to the edge.
	x := rightEdge
	for i, text := range texts {
		x -= uniseg.StringWidth(text)
		drawCells(scr, x, py, text, badges[i].style)
	}
}

// fitCells returns s, or s cut to at most width cells with an ellipsis.
func fitCells(s string, width int) string {
	if uniseg.StringWidth(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	state := -1
	for s != "" {
		var g string
		var w int
		g, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		if used+w > width-1 {
			break
		}
		b.WriteString(g)
		used += w
	}
	return strings.TrimRight(b.String(), " ") + "…"
}

// drawCells draws s from column x on row y, one grapheme per cell or two.
func drawCells(scr tcell.Screen, x, y int, s string, style tcell.Style) {
	for s != "" {
		var w int
		s, w = scr.Put(x, y, s, style)
		if w == 0 {
			return
		}
		x += w
	}
}
