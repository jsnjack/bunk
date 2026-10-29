// Package restore keeps a snapshot of each bunk-gui window on disk while
// it runs, so a later start can bring its tabs, splits, directories,
// agents and scrollback back.
//
// Each window is a slot in a Store: <id>.json holds the layout (Window),
// <id>/ the scrollback of each pane, and <id>.lock a lock the running
// window holds. A slot nobody holds is a window that ended, by a quit, a
// crash or a logout, and Saved lists those. A window writes its snapshot
// as it changes, so a crash loses at most its last seconds.
//
// The package knows nothing of GTK: bunk-gui maps its tabs to these types,
// and bunk's TUI can use the same ones.
package restore

import (
	"strconv"
	"strings"
	"time"
)

// Version is the "v" field of every snapshot. A snapshot of another
// version is not offered.
const Version = 1

// Window is one window's snapshot.
type Window struct {
	V     int       `json:"v"`
	Saved time.Time `json:"saved"`
	// Width and Height are the window's size in logical pixels, 0 when
	// unknown; Maximized wins over them.
	Width     int  `json:"width,omitempty"`
	Height    int  `json:"height,omitempty"`
	Maximized bool `json:"maximized,omitempty"`
	// Selected is the index of the selected tab.
	Selected int   `json:"selected"`
	Tabs     []Tab `json:"tabs"`
}

// Tab is one tab: its user-given name, its split tree, and its focused
// pane as an index in Panes order. Zoom shows the focused pane alone.
type Tab struct {
	Name  string `json:"name,omitempty"`
	Focus int    `json:"focus"`
	Zoom  bool   `json:"zoom,omitempty"`
	Root  *Node  `json:"root"`
}

// Node is a pane (Pane set) or a split of two nodes. Ratio is the share of
// the first one, A: the left or top one, Vertical when they are stacked.
type Node struct {
	Pane     *Pane   `json:"pane,omitempty"`
	Vertical bool    `json:"vertical,omitempty"`
	Ratio    float64 `json:"ratio,omitempty"`
	A        *Node   `json:"a,omitempty"`
	B        *Node   `json:"b,omitempty"`
}

// Pane is what a pane ran, and where.
type Pane struct {
	Cwd string `json:"cwd,omitempty"`
	// Agent is the agent in the foreground (session.Status.Agent), and
	// AgentSession its conversation, from its hooks; "" when none.
	Agent        string `json:"agent,omitempty"`
	AgentSession string `json:"agent_session,omitempty"`
	// Host is where a remote agent ran: it cannot be resumed here.
	Host string `json:"host,omitempty"`
	// Remote is the argv of ssh or mosh in the foreground, which a
	// restored pane types without running.
	Remote []string `json:"remote,omitempty"`
	// History names the file of the pane's scrollback in the slot's
	// directory, "" without one.
	History string `json:"history,omitempty"`
}

// Panes lists the panes under n, left or top first.
func (n *Node) Panes() []*Pane {
	var out []*Pane
	var walk func(n *Node)
	walk = func(n *Node) {
		switch {
		case n == nil:
		case n.Pane != nil:
			out = append(out, n.Pane)
		default:
			walk(n.A)
			walk(n.B)
		}
	}
	walk(n)
	return out
}

// Valid reports whether the tree is whole: every node a pane or a split
// of two valid nodes with a ratio in (0, 1).
func (n *Node) Valid() bool {
	switch {
	case n == nil:
		return false
	case n.Pane != nil:
		return n.A == nil && n.B == nil
	}
	return n.Ratio > 0 && n.Ratio < 1 && n.A.Valid() && n.B.Valid()
}

// Counts returns the windows', tabs' and agents' totals in ws.
func Counts(ws []Window) (windows, tabs, agents int) {
	for _, w := range ws {
		windows++
		tabs += len(w.Tabs)
		for _, t := range w.Tabs {
			for _, p := range t.Root.Panes() {
				if p.Agent != "" {
					agents++
				}
			}
		}
	}
	return windows, tabs, agents
}

// Summary says what ws hold: "2 windows, 7 tabs, 3 agents".
func Summary(ws []Window) string {
	windows, tabs, agents := Counts(ws)
	parts := []string{plural(tabs, "tab")}
	if windows > 1 {
		parts = append([]string{plural(windows, "window")}, parts...)
	}
	if agents > 0 {
		parts = append(parts, plural(agents, "agent"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
