// status.go - per-pane status badges and the agent-state separator colours.
//
// Which badges a pane shows, their order, text and fitting come from
// internal/badges, shared with bunk-gui. This file maps each badge's role
// to theme colours and draws the row in the pane's top-right corner, last
// in each render pass so it paints on top.
package main

import (
	"bunk/internal/badges"
	"bunk/internal/session"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

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

// agentLook is how a pane shows its agent's state: the badge colours, and
// for states that want the user, the colour of the separators around the
// pane. The badge's glyph and label come from badges.AgentLook.
type agentLook struct {
	style  tcell.Style
	border tcell.Color // tcell.ColorDefault keeps the normal separator colour
}

// agentLookOf returns the look of state; false for a pane without an agent.
func agentLookOf(state session.AgentState) (agentLook, bool) {
	quiet := tcell.StyleDefault.Background(colorAgentBadge)
	loud := tcell.StyleDefault.Foreground(colorAgentInk).Bold(true)
	switch state {
	case session.AgentWorking:
		return agentLook{quiet.Foreground(colorAgentWorking), tcell.ColorDefault}, true
	case session.AgentIdle:
		return agentLook{quiet.Foreground(colorAgentIdle), tcell.ColorDefault}, true
	case session.AgentNeedsYou:
		return agentLook{loud.Background(colorAgentNeeds), colorAgentNeeds}, true
	case session.AgentDone:
		return agentLook{loud.Background(colorAgentDone), colorAgentDone}, true
	case session.AgentFailed:
		return agentLook{quiet.Foreground(colorAgentFailed).Bold(true), tcell.ColorDefault}, true
	}
	return agentLook{}, false
}

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

// drawPaneStatus draws pane p's badges (internal/badges) right to left
// from its top-right corner, in the colours badgeStyle gives their roles.
func drawPaneStatus(scr tcell.Screen, p *Pane, isActive bool, rt resolvedTheme, zoomed bool) {
	p.Lock()
	in := badges.Input{
		Passthrough:   p.passthrough,
		ScrollOffset:  p.ScrollOffset,
		Agent:         p.agent.name,
		State:         p.agent.state,
		Notice:        p.agent.notice,
		Process:       p.FgProcess,
		ContainerType: p.ContainerType,
		ContainerID:   p.ContainerID,
		SSHHost:       p.SSHHost,
		Zoomed:        zoomed,
		Width:         p.w,
	}
	if !p.statusMsgEnd.IsZero() && time.Now().Before(p.statusMsgEnd) {
		in.Flash = p.statusMsg
	}
	px, py, pw := p.x, p.y, p.w
	p.Unlock()

	row := badges.Layout(in)
	x := px + pw - row.Inset
	for _, b := range row.Badges {
		x -= uniseg.StringWidth(b.Text)
		drawCells(scr, x, py, b.Text, badgeStyle(b.Role, rt))
	}
}

// badgeStyle is how the TUI paints a badge of role. Colours come from the
// theme palette (the palette index table in config.go), so they adapt to
// any theme; most backgrounds are blended 40% toward the theme background
// to soften them. The agent badge keeps its fixed state colours.
func badgeStyle(role badges.Role, rt resolvedTheme) tcell.Style {
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
		look, _ := agentLookOf(state)
		return look.style
	}
	return tcell.StyleDefault
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
