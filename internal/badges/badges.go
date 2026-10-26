// Package badges decides the status badges at the top right of a pane:
// which show, their order, their text and how they shrink to fit. Both
// front ends draw the Row that Layout returns, so a badge reads and fits
// the same in bunk's TUI and in bunk-gui. Colours stay with each front
// end, which paints a badge by its Role.
package badges

import (
	"strconv"
	"strings"
	"time"

	"github.com/rivo/uniseg"

	"bunk/internal/session"
)

// Role is what a badge shows; each front end picks the badge's colours
// from it.
type Role uint8

// Badge roles. Each kind of badge has its own; the agent badge has one per
// state and the context badge one per colour it can take.
const (
	RoleNone      Role = iota
	RolePass           // keyboard passthrough
	RoleScroll         // lines scrolled back
	RoleWorking        // agent working
	RoleIdle           // agent idle
	RoleNeedsYou       // agent needs the user
	RoleDone           // agent reported a finished turn
	RoleFailed         // agent's command failed
	RoleSudo           // context: sudo or su in the foreground
	RoleSSH            // context: ssh in the foreground
	RoleContainer      // context: a container, no ssh, sudo or su
	RoleMosh           // context: mosh outside a container
	RoleZoom           // the pane is zoomed
	RoleFlash          // a short message, such as Copied
)

var roleNames = [...]string{
	RoleNone: "none", RolePass: "pass", RoleScroll: "scroll",
	RoleWorking: "agent-working", RoleIdle: "agent-idle", RoleNeedsYou: "agent-needs",
	RoleDone: "agent-done", RoleFailed: "agent-failed",
	RoleSudo: "sudo", RoleSSH: "ssh", RoleContainer: "container", RoleMosh: "mosh",
	RoleZoom: "zoom", RoleFlash: "flash",
}

// Roles lists every role a badge can have.
var Roles = []Role{RolePass, RoleScroll, RoleWorking, RoleIdle, RoleNeedsYou, RoleDone, RoleFailed,
	RoleSudo, RoleSSH, RoleContainer, RoleMosh, RoleZoom, RoleFlash}

// String returns the role's name, such as "pass" or "agent-needs".
func (r Role) String() string {
	if int(r) < len(roleNames) {
		return roleNames[r]
	}
	return "role-" + strconv.Itoa(int(r))
}

// AgentState returns the agent state an agent badge's role shows; false
// for the other roles.
func (r Role) AgentState() (session.AgentState, bool) {
	switch r {
	case RoleWorking:
		return session.AgentWorking, true
	case RoleIdle:
		return session.AgentIdle, true
	case RoleNeedsYou:
		return session.AgentNeedsYou, true
	case RoleDone:
		return session.AgentDone, true
	case RoleFailed:
		return session.AgentFailed, true
	}
	return session.AgentNone, false
}

// AgentLook returns the dot and the label that show an agent state, and
// the agent badge's role. ok is false for AgentNone: no agent badge.
func AgentLook(state session.AgentState) (dot, label string, role Role, ok bool) {
	switch state {
	case session.AgentWorking:
		return "●", "working", RoleWorking, true
	case session.AgentIdle:
		return "○", "idle", RoleIdle, true
	case session.AgentNeedsYou:
		return "◐", "needs you", RoleNeedsYou, true
	case session.AgentDone:
		return "✓", "done", RoleDone, true
	case session.AgentFailed:
		return "✕", "failed", RoleFailed, true
	}
	return "", "", RoleNone, false
}

const (
	// Copied is the flash text after a copy.
	Copied = "COPIED"
	// FlashFor is how long a flash shows.
	FlashFor = 3 * time.Second

	// noticeSep joins the agent badge and its notice.
	noticeSep = " · "
	// minNoticeCells is the narrowest notice worth showing.
	minNoticeCells = 8
)

// Input is the state of one pane that its badges show.
type Input struct {
	Passthrough  bool
	ScrollOffset int                // lines above the live view; 0 in the live view
	Agent        string             // agent name (session.Status.Agent)
	State        session.AgentState // AgentNone: no agent badge
	Notice       string             // the agent's latest notice
	Process      string             // foreground process name
	// ContainerType is "toolbox", "distrobox", "podman", "lxd" or "" outside
	// a container; ContainerID names the container.
	ContainerType, ContainerID string
	SSHHost                    string // ssh or mosh destination
	Zoomed                     bool
	Flash                      string // a short message such as Copied; "" for none
	Width                      int    // the pane's width in cells, its scrollbar column included
}

// Badge is one badge as drawn. Text includes the padding spaces at both
// ends.
type Badge struct {
	Text string
	Role Role
}

// Row is the badges of a pane that fit its width.
type Row struct {
	// Badges run right to left: Badges[0] touches the right edge, less
	// Inset.
	Badges []Badge
	// Inset is the cells left free at the right edge: the scrollbar column
	// while the pane is scrolled back.
	Inset int
}

// Cells returns the width of the badges in cells.
func (r Row) Cells() int {
	n := 0
	for _, b := range r.Badges {
		n += uniseg.StringWidth(b.Text)
	}
	return n
}

// candidate is a badge before fitting: its forms, widest first.
type candidate struct {
	forms []string
	role  Role
}

// Layout returns the badges of a pane in right-to-left order:
//
//	PASS              keyboard passthrough
//	-N                lines scrolled back
//	● claude working  the agent, its state, then its notice when there is room
//	⬡ box · ⇄ host    container, ssh or mosh host, sudo or su
//	ZOOM              the pane is zoomed
//	COPIED            a flash message
//
// A badge that does not fit hides itself and every badge after it. The
// agent badge drops the agent's name, then its label, before it gives up.
// The notice takes the room left over, up to two thirds of the row, so the
// pane's own top line stays partly visible.
func Layout(in Input) Row {
	var cands []candidate
	add := func(role Role, forms ...string) {
		cands = append(cands, candidate{forms, role})
	}
	if in.Passthrough {
		add(RolePass, " PASS ")
	}
	if in.ScrollOffset > 0 {
		add(RoleScroll, " -"+strconv.Itoa(in.ScrollOffset)+" ")
	}
	agentAt := -1
	if dot, label, role, ok := AgentLook(in.State); ok {
		agentAt = len(cands)
		add(role,
			" "+dot+" "+in.Agent+" "+label+" ",
			" "+dot+" "+label+" ",
			" "+dot+" ")
	}
	if text, role := contextBadge(in); text != "" {
		add(role, text)
	}
	if in.Zoomed {
		add(RoleZoom, " ZOOM ")
	}
	if in.Flash != "" {
		add(RoleFlash, " "+in.Flash+" ")
	}

	var row Row
	if in.ScrollOffset > 0 {
		row.Inset = 1
	}
	if len(cands) == 0 {
		return row
	}
	room := in.Width - row.Inset
	used := 0
	for _, c := range cands {
		text := ""
		for _, f := range c.forms {
			if used+uniseg.StringWidth(f) <= room {
				text = f
				break
			}
		}
		if text == "" {
			break
		}
		row.Badges = append(row.Badges, Badge{text, c.role})
		used += uniseg.StringWidth(text)
	}

	if agentAt >= 0 && agentAt < len(row.Badges) && in.Notice != "" && row.Badges[agentAt].Text == cands[agentAt].forms[0] {
		spare := max(used, room*2/3) - used - uniseg.StringWidth(noticeSep)
		if spare >= minNoticeCells {
			b := &row.Badges[agentAt]
			b.Text = strings.TrimSuffix(b.Text, " ") + noticeSep + fitCells(in.Notice, spare) + " "
		}
	}
	return row
}

// contextBadge returns the text and role of the context badge: the
// container, then ssh or mosh and its host, or sudo or su, joined by " · ".
// It returns "" when there is no context to show.
func contextBadge(in Input) (string, Role) {
	var parts []string
	if in.ContainerType != "" {
		icon := "⬡"
		if in.ContainerType == "distrobox" {
			icon = "▣"
		}
		label := in.ContainerType
		if in.ContainerID != "" {
			label = in.ContainerID
		}
		parts = append(parts, icon+" "+label)
	}
	switch in.Process {
	case "ssh", "mosh":
		host := in.SSHHost
		if host == "" {
			host = in.Process
		}
		parts = append(parts, "⇄ "+host)
	case "sudo", "su":
		parts = append(parts, in.Process)
	}
	if len(parts) == 0 {
		return "", RoleNone
	}
	role := RoleMosh
	switch {
	case in.Process == "sudo" || in.Process == "su":
		role = RoleSudo
	case in.Process == "ssh":
		role = RoleSSH
	case in.ContainerType != "":
		role = RoleContainer
	}
	return " " + strings.Join(parts, " · ") + " ", role
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
