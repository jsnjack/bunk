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
	"bunk/internal/theme"
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

// Agent state colours (internal/theme), the same in both front ends. They
// do not follow the theme.
var (
	colorAgentBadge   = tcellColor(theme.AgentBadge) // badge background for quiet states
	colorAgentInk     = tcellColor(theme.AgentInk)   // text on the needs-you and done badges
	colorAgentWorking = tcellColor(theme.AgentWorking)
	colorAgentNeeds   = tcellColor(theme.AgentNeeds)
	colorAgentIdle    = tcellColor(theme.AgentIdle)
	colorAgentFailed  = tcellColor(theme.AgentFailed)
	colorAgentDone    = tcellColor(theme.AgentDone)
)

// agentBorder is the colour of the separators around a pane whose agent
// is in state: the badge's colour for states that want the user,
// tcell.ColorDefault (the normal separator colour) otherwise.
func agentBorder(state session.AgentState) tcell.Color {
	switch state {
	case session.AgentNeedsYou:
		return colorAgentNeeds
	case session.AgentDone:
		return colorAgentDone
	}
	return tcell.ColorDefault
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
		style := tcell.StyleDefault.Foreground(agentBorder(want)).Background(rt.bg)
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

// badgeStyle is how the TUI paints a badge of role, with the colours
// internal/theme gives the role (theme.BadgeInks): palette colours, most
// backgrounds blended theme.BadgeDim percent toward the theme background,
// and fixed agent colours.
func badgeStyle(role badges.Role, rt resolvedTheme) tcell.Style {
	fg, bg, bold := theme.BadgeInks(role)
	return tcell.StyleDefault.Foreground(rt.ink(fg)).Background(rt.ink(bg)).Bold(bold)
}

// ink resolves a badge colour in the theme's tcell colours. A colour the
// theme leaves to the host blends as tcell's RGB of it, -1 per channel,
// as the TUI always did.
func (rt resolvedTheme) ink(i theme.Ink) tcell.Color {
	c := tcellColor(i.Fixed)
	if i.ANSI >= 0 {
		c = rt.palette[i.ANSI]
	}
	if !i.Dim {
		return c
	}
	r1, g1, b1 := c.RGB()
	r2, g2, b2 := rt.bg.RGB()
	mix := func(a, b int32) int32 { return (a*(100-theme.BadgeDim) + b*theme.BadgeDim) / 100 }
	return tcell.NewRGBColor(mix(r1, r2), mix(g1, g2), mix(b1, b2))
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
