// Package badgetest holds the pane states that the badge tests of both
// front ends draw, with the badges each must show. bunk's TUI draws them
// on a tcell simulation screen and bunk-gui through its badge row; both
// compare with Want, so the two front ends show the same badges.
package badgetest

import (
	"bunk/internal/badges"
	"bunk/internal/session"
)

// Case is a pane state and the badges it shows.
type Case struct {
	Name  string
	In    badges.Input
	Want  []badges.Badge // right to left
	Inset int
}

func b(text string, role badges.Role) badges.Badge { return badges.Badge{Text: text, Role: role} }

const long = "Claude needs your permission to use Bash for npm install"

// Cases covers every badge kind and role, their order, the notice, wide
// characters, and the forms a badge takes or loses in narrow panes.
var Cases = []Case{
	{Name: "nothing", In: badges.Input{Width: 80}},
	{Name: "other process", In: badges.Input{Process: "vim", Width: 80}},
	{Name: "width 0", In: badges.Input{Passthrough: true, Width: 0}},
	{Name: "pass", In: badges.Input{Passthrough: true, Width: 80},
		Want: []badges.Badge{b(" PASS ", badges.RolePass)}},
	{Name: "scroll", In: badges.Input{ScrollOffset: 42, Width: 80},
		Want: []badges.Badge{b(" -42 ", badges.RoleScroll)}, Inset: 1},
	{Name: "scroll fills the row", In: badges.Input{ScrollOffset: 9, Width: 5},
		Want: []badges.Badge{b(" -9 ", badges.RoleScroll)}, Inset: 1},
	{Name: "agent working", In: badges.Input{Agent: "claude", State: session.AgentWorking, Width: 80},
		Want: []badges.Badge{b(" ● claude working ", badges.RoleWorking)}},
	{Name: "agent idle", In: badges.Input{Agent: "codex", State: session.AgentIdle, Width: 80},
		Want: []badges.Badge{b(" ○ codex idle ", badges.RoleIdle)}},
	{Name: "agent needs you with notice", In: badges.Input{Agent: "claude", State: session.AgentNeedsYou,
		Notice: "Approve edit to main.go?", Width: 80},
		Want: []badges.Badge{b(" ◐ claude needs you · Approve edit to main.go? ", badges.RoleNeedsYou)}},
	{Name: "agent needs you, notice cut", In: badges.Input{Agent: "claude", State: session.AgentNeedsYou,
		Notice: long, Width: 90},
		Want: []badges.Badge{b(" ◐ claude needs you · Claude needs your permission to use… ", badges.RoleNeedsYou)}},
	{Name: "agent done, wide notice", In: badges.Input{Agent: "agent", State: session.AgentDone,
		Notice: "完了しました", Width: 60},
		Want: []badges.Badge{b(" ✓ agent done · 完了しました ", badges.RoleDone)}},
	{Name: "agent done, wide notice cut", In: badges.Input{Agent: "agent", State: session.AgentDone,
		Notice: "完了しました。テストはすべて合格です。", Width: 40},
		Want: []badges.Badge{b(" ✓ agent done · 完了しま… ", badges.RoleDone)}},
	{Name: "agent failed, notice cut", In: badges.Input{Agent: "claude", State: session.AgentFailed,
		Notice: "API Error: 429 Too Many Requests, retry after 30 seconds", Width: 60},
		Want: []badges.Badge{b(" ✕ claude failed · API Error: 429 Too… ", badges.RoleFailed)}},
	{Name: "no room for the notice", In: badges.Input{Agent: "claude", State: session.AgentNeedsYou,
		Notice: long, Width: 30},
		Want: []badges.Badge{b(" ◐ claude needs you ", badges.RoleNeedsYou)}},
	{Name: "agent without its name", In: badges.Input{Agent: "claude", State: session.AgentNeedsYou,
		Notice: long, Width: 16},
		Want: []badges.Badge{b(" ◐ needs you ", badges.RoleNeedsYou)}},
	{Name: "agent dot only", In: badges.Input{Agent: "claude", State: session.AgentDone, Width: 4},
		Want: []badges.Badge{b(" ✓ ", badges.RoleDone)}},
	{Name: "agent hidden", In: badges.Input{Agent: "claude", State: session.AgentWorking, Width: 2}},
	{Name: "toolbox", In: badges.Input{ContainerType: "toolbox", ContainerID: "fedora", Width: 80},
		Want: []badges.Badge{b(" ⬡ fedora ", badges.RoleContainer)}},
	{Name: "distrobox without a name", In: badges.Input{ContainerType: "distrobox", Width: 80},
		Want: []badges.Badge{b(" ▣ distrobox ", badges.RoleContainer)}},
	{Name: "ssh", In: badges.Input{Process: "ssh", SSHHost: "build01", Width: 80},
		Want: []badges.Badge{b(" ⇄ build01 ", badges.RoleSSH)}},
	{Name: "ssh without a host", In: badges.Input{Process: "ssh", Width: 80},
		Want: []badges.Badge{b(" ⇄ ssh ", badges.RoleSSH)}},
	{Name: "mosh", In: badges.Input{Process: "mosh", SSHHost: "gpu01", Width: 80},
		Want: []badges.Badge{b(" ⇄ gpu01 ", badges.RoleMosh)}},
	{Name: "mosh in a container", In: badges.Input{Process: "mosh", SSHHost: "gpu01",
		ContainerType: "podman", ContainerID: "web", Width: 80},
		Want: []badges.Badge{b(" ⬡ web · ⇄ gpu01 ", badges.RoleContainer)}},
	{Name: "ssh in a container", In: badges.Input{Process: "ssh", SSHHost: "build01",
		ContainerType: "toolbox", ContainerID: "fedora", Width: 80},
		Want: []badges.Badge{b(" ⬡ fedora · ⇄ build01 ", badges.RoleSSH)}},
	{Name: "sudo", In: badges.Input{Process: "sudo", Width: 80},
		Want: []badges.Badge{b(" sudo ", badges.RoleSudo)}},
	{Name: "su in a container", In: badges.Input{Process: "su", ContainerType: "toolbox", ContainerID: "fedora", Width: 80},
		Want: []badges.Badge{b(" ⬡ fedora · su ", badges.RoleSudo)}},
	{Name: "zoom", In: badges.Input{Zoomed: true, Width: 80},
		Want: []badges.Badge{b(" ZOOM ", badges.RoleZoom)}},
	{Name: "flash", In: badges.Input{Flash: badges.Copied, Width: 80},
		Want: []badges.Badge{b(" COPIED ", badges.RoleFlash)}},
	{Name: "flash too wide", In: badges.Input{Flash: badges.Copied, Width: 5}},
	{Name: "every badge", In: badges.Input{Passthrough: true, ScrollOffset: 7, Agent: "claude",
		State: session.AgentWorking, Notice: "Running tests", Process: "ssh", SSHHost: "build01",
		ContainerType: "toolbox", ContainerID: "fedora", Zoomed: true, Flash: badges.Copied, Width: 120},
		Want: []badges.Badge{b(" PASS ", badges.RolePass),
			b(" -7 ", badges.RoleScroll),
			b(" ● claude working · Running tes… ", badges.RoleWorking),
			b(" ⬡ fedora · ⇄ build01 ", badges.RoleSSH),
			b(" ZOOM ", badges.RoleZoom),
			b(" COPIED ", badges.RoleFlash)}, Inset: 1},
	{Name: "PASS keeps the edge", In: badges.Input{Passthrough: true, Agent: "claude",
		State: session.AgentWorking, Width: 30},
		Want: []badges.Badge{b(" PASS ", badges.RolePass),
			b(" ● claude working ", badges.RoleWorking)}},
	{Name: "narrow: agent drops to its dot", In: badges.Input{Passthrough: true, Agent: "claude",
		State: session.AgentNeedsYou, Notice: long, Zoomed: true, Width: 12},
		Want: []badges.Badge{b(" PASS ", badges.RolePass),
			b(" ◐ ", badges.RoleNeedsYou)}},
	{Name: "narrow: a badge that does not fit hides the rest", In: badges.Input{Passthrough: true,
		ScrollOffset: 12345, Zoomed: true, Width: 14},
		Want: []badges.Badge{b(" PASS ", badges.RolePass)}, Inset: 1},
	{Name: "narrow: zoom does not fit after context", In: badges.Input{Agent: "codex", State: session.AgentIdle,
		Process: "sudo", Zoomed: true, Width: 20},
		Want: []badges.Badge{b(" ○ codex idle ", badges.RoleIdle),
			b(" sudo ", badges.RoleSudo)}},
}

// Look is a badge's colours: 0xRRGGBB.
type Look struct {
	FG, BG uint32
	Bold   bool
}

// DefaultLooks is each role's colours under bunk's "default" theme, the
// palette bunk-gui uses: theme colours, most backgrounds blended 40%
// toward the theme background, and the agent colours of both front ends'
// chrome.
var DefaultLooks = map[badges.Role]Look{
	badges.RolePass:      {0xC0BFBC, 0x117A90, true},
	badges.RoleScroll:    {0xF5C211, 0x241F31, true},
	badges.RoleWorking:   {0x5AA9FF, 0x24262B, false},
	badges.RoleIdle:      {0x9AA0A8, 0x24262B, false},
	badges.RoleNeedsYou:  {0x131416, 0xFFB454, true},
	badges.RoleDone:      {0x131416, 0x7EE2A8, true},
	badges.RoleFailed:    {0xFF8A8A, 0x24262B, true},
	badges.RoleSudo:      {0xC0BFBC, 0x7E1C24, true},
	badges.RoleSSH:       {0xC0BFBC, 0x1D5395, true},
	badges.RoleContainer: {0xC0BFBC, 0x117A90, true},
	badges.RoleMosh:      {0xC0BFBC, 0x241F31, true},
	badges.RoleZoom:      {0xF5C211, 0x241F31, true},
	badges.RoleFlash:     {0xC0BFBC, 0x267F58, true},
}
