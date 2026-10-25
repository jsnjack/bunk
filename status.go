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
//	⬡ <container-name>  - Toolbox / Podman container
//	▣ <container-name>  - Distrobox container
//	ssh / sudo / su / root - notable foreground process
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
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
	return fmt.Sprintf("%t|%t|%s|%s|%s|%s|%s",
		p.passthrough,
		tempActive,
		p.statusMsg,
		p.FgProcess,
		p.ContainerType,
		p.ContainerID,
		p.SSHHost,
	)
}

// drawPaneStatus draws compact status badges in the top-right corner of
// pane p.  Badges are drawn right-to-left so the highest-priority badge
// (passthrough, then scroll count) is closest to the edge and always visible.
//
// Badge order (right to left):
//
//	[ PASS ]                - keyboard passthrough (toggle remains reserved)
//	[-N]                    - scrollback line count (yellow on black)
//	[ COPIED ]              - temporary flash message (white on green)
//	[⬡ my-toolbox]          - container/sudo/ssh context badge
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

	// A badge is a styled run of text.
	type badge struct {
		text  string
		style tcell.Style
	}
	// Collect badges left-to-right in display order (rightmost drawn last,
	// closest to the edge).
	var badges []badge

	// 1. Temporary flash message (e.g. "COPIED") - leftmost badge.
	if tempActive && tempMsg != "" {
		badges = append(badges, badge{
			" " + tempMsg + " ",
			tcell.StyleDefault.Foreground(badgeText).Background(colorGreen).Bold(true),
		})
	}

	// 1b. Zoom indicator.
	if zoomed {
		badges = append(badges, badge{
			" ZOOM ",
			tcell.StyleDefault.Foreground(colorYellow).Background(colorDark).Bold(true),
		})
	}

	// 2. Container / SSH / sudo context badge.
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
			badges = append(badges, badge{
				" " + strings.Join(parts, " · ") + " ",
				tcell.StyleDefault.Foreground(badgeText).Background(bg).Bold(true),
			})
		}
	}

	// 3. Scroll line count (below passthrough in priority).
	if sbOff > 0 {
		badges = append(badges, badge{
			fmt.Sprintf(" -%d ", sbOff),
			tcell.StyleDefault.Foreground(colorYellow).Background(colorDark).Bold(true),
		})
	}

	if passthrough {
		badges = append(badges, badge{
			" PASS ",
			tcell.StyleDefault.Foreground(badgeText).Background(colorCyan).Bold(true),
		})
	}

	if len(badges) == 0 {
		return
	}

	// Draw right-to-left: last badge in the slice goes closest to the edge.
	rightEdge := px + pw
	if sbOff > 0 {
		rightEdge-- // reserve scrollbar column
	}
	for i := len(badges) - 1; i >= 0; i-- {
		runes := []rune(badges[i].text)
		w := len(runes)
		startX := rightEdge - w
		if startX < px {
			break // no more room
		}
		for j, ch := range runes {
			scr.SetContent(startX+j, py, ch, nil, badges[i].style)
		}
		rightEdge = startX
	}
}
