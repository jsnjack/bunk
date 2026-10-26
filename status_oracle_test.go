package main

// The oracle for TestBadgesMatchOldRenderer: drawPaneStatus as it was before
// the badge rules moved to internal/badges, with its helpers and colours,
// unchanged but for the "old" prefix. Do not edit it to follow new
// behaviour; it records what the TUI drew.

import (
	"bunk/internal/session"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

var (
	oldColorAgentBadge   = tcell.NewRGBColor(0x24, 0x26, 0x2b)
	oldColorAgentInk     = tcell.NewRGBColor(0x13, 0x14, 0x16)
	oldColorAgentWorking = tcell.NewRGBColor(0x5a, 0xa9, 0xff)
	oldColorAgentNeeds   = tcell.NewRGBColor(0xff, 0xb4, 0x54)
	oldColorAgentIdle    = tcell.NewRGBColor(0x9a, 0xa0, 0xa8)
	oldColorAgentFailed  = tcell.NewRGBColor(0xff, 0x8a, 0x8a)
	oldColorAgentDone    = tcell.NewRGBColor(0x7e, 0xe2, 0xa8)
)

type oldAgentLook struct {
	dot, label string
	style      tcell.Style
	border     tcell.Color
}

func oldAgentLookOf(state session.AgentState) (oldAgentLook, bool) {
	quiet := tcell.StyleDefault.Background(oldColorAgentBadge)
	loud := tcell.StyleDefault.Foreground(oldColorAgentInk).Bold(true)
	switch state {
	case session.AgentWorking:
		return oldAgentLook{"●", "working", quiet.Foreground(oldColorAgentWorking), tcell.ColorDefault}, true
	case session.AgentIdle:
		return oldAgentLook{"○", "idle", quiet.Foreground(oldColorAgentIdle), tcell.ColorDefault}, true
	case session.AgentNeedsYou:
		return oldAgentLook{"◐", "needs you", loud.Background(oldColorAgentNeeds), oldColorAgentNeeds}, true
	case session.AgentDone:
		return oldAgentLook{"✓", "done", loud.Background(oldColorAgentDone), oldColorAgentDone}, true
	case session.AgentFailed:
		return oldAgentLook{"✕", "failed", quiet.Foreground(oldColorAgentFailed).Bold(true), tcell.ColorDefault}, true
	}
	return oldAgentLook{}, false
}

const (
	oldNoticeSep      = " · "
	oldMinNoticeCells = 8
)

func oldDrawPaneStatus(scr tcell.Screen, p *Pane, isActive bool, rt resolvedTheme, zoomed bool) {
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

	dim := func(c tcell.Color) tcell.Color {
		r1, g1, b1 := c.RGB()
		r2, g2, b2 := rt.bg.RGB()
		mix := func(a, b int32) int32 { return (a*60 + b*40) / 100 }
		return tcell.NewRGBColor(mix(r1, r2), mix(g1, g2), mix(b1, b2))
	}
	badgeText := rt.palette[7]
	colorRed := dim(rt.palette[1])
	colorGreen := dim(rt.palette[2])
	colorYellow := rt.palette[3]
	colorBlue := dim(rt.palette[4])
	colorCyan := dim(rt.palette[6])
	colorDark := rt.palette[0]

	type badge struct {
		forms []string
		style tcell.Style
	}
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
	look, hasAgent := oldAgentLookOf(agent.state)
	if hasAgent {
		agentAt = len(badges)
		add(look.style,
			" "+look.dot+" "+agent.name+" "+look.label+" ",
			" "+look.dot+" "+look.label+" ",
			" "+look.dot+" ")
	}

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

	if tempActive && tempMsg != "" {
		add(tcell.StyleDefault.Foreground(badgeText).Background(colorGreen).Bold(true), " "+tempMsg+" ")
	}

	if len(badges) == 0 {
		return
	}

	rightEdge := px + pw
	if sbOff > 0 {
		rightEdge--
	}
	room := rightEdge - px

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
			break
		}
		texts = append(texts, text)
		used += uniseg.StringWidth(text)
	}

	if agentAt >= 0 && agentAt < len(texts) && agent.notice != "" && texts[agentAt] == badges[agentAt].forms[0] {
		spare := max(used, room*2/3) - used - uniseg.StringWidth(oldNoticeSep)
		if spare >= oldMinNoticeCells {
			notice := oldFitCells(agent.notice, spare)
			texts[agentAt] = strings.TrimSuffix(texts[agentAt], " ") + oldNoticeSep + notice + " "
		}
	}

	x := rightEdge
	for i, text := range texts {
		x -= uniseg.StringWidth(text)
		oldDrawCells(scr, x, py, text, badges[i].style)
	}
}

func oldFitCells(s string, width int) string {
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

func oldDrawCells(scr tcell.Screen, x, y int, s string, style tcell.Style) {
	for s != "" {
		var w int
		s, w = scr.Put(x, y, s, style)
		if w == 0 {
			return
		}
		x += w
	}
}
