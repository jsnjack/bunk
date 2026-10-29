package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bunk/internal/session"
)

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSettings(t *testing.T) {
	def := defaultSettings()

	st, err := loadSettings(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || st.Tabs != def.Tabs || st.Font != def.Font || st.FontSize != def.FontSize {
		t.Fatalf("missing file: %+v, %v; want defaults and no error", st, err)
	}

	st, err = loadSettings(writeConfig(t, `
tabs = "bar"
rail_open = true
font = "JetBrains Mono"
font_size = 13.5

[[agents]]
name = "aider"
processes = ["aider"]
scripts = ["aider/main.py"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tabs != "bar" || !st.RailOpen || st.Font != "JetBrains Mono" || st.FontSize != 13.5 {
		t.Fatalf("got %+v", st)
	}
	if len(st.Agents) != 1 || st.Agents[0].Name != "aider" || st.Agents[0].Processes[0] != "aider" ||
		st.Agents[0].Scripts[0] != "aider/main.py" {
		t.Fatalf("agents: %+v", st.Agents)
	}

	// Bad values and unknown keys are reported; good keys still apply.
	st, err = loadSettings(writeConfig(t, `
tabs = "sideways"
font_size = 200
font = "Fira Code"
colour = "red"

[[agents]]
name = "nothing"
`))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"colour", `tabs = "sideways"`, "font_size = 200", "agents[0]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if st.Tabs != def.Tabs || st.FontSize != def.FontSize || st.Font != "Fira Code" || len(st.Agents) != 0 {
		t.Fatalf("got %+v", st)
	}

	// A file that does not parse gives the defaults.
	st, err = loadSettings(writeConfig(t, "tabs = [\n"))
	if err == nil || st.Tabs != def.Tabs || st.Font != def.Font {
		t.Fatalf("broken file: %+v, %v", st, err)
	}
}

func TestLookOf(t *testing.T) {
	for _, tc := range []struct {
		state      session.AgentState
		dot, label string
		cls        string
	}{
		{session.AgentWorking, "●", "working", "working"},
		{session.AgentNeedsYou, "◐", "needs you", "needs-fg"},
		{session.AgentIdle, "○", "idle", "idle"},
		{session.AgentFailed, "✕", "failed", "failed"},
		{session.AgentNone, "○", "", "idle"},
	} {
		if got := lookOf(tc.state); got != (look{tc.dot, tc.label, tc.cls}) {
			t.Errorf("lookOf(%d) = %+v", tc.state, got)
		}
	}
	// The chrome CSS defines the colour of every state class.
	for cls, colour := range map[string]string{"working": "#5aa9ff", "needs-fg": "#ffb454", "idle": "#9aa0a8", "failed": "#ff8a8a"} {
		if !strings.Contains(chromeCSS, "."+cls+" { color: "+colour+"; }") {
			t.Errorf("chromeCSS: .%s is not %s", cls, colour)
		}
	}
}

// A 2x2 tree built by hand, as split() builds it: tree order, lookups,
// the agent pane and dimming.
// TestPaneMap: the collapsed rail's map places each pane's dot as the
// splits place the panes.
func TestPaneMap(t *testing.T) {
	a, b, c, d := &Pane{id: 1}, &Pane{id: 2}, &Pane{id: 3}, &Pane{id: 4}
	dots := map[*Pane]look{a: lookOf(session.AgentNeedsYou), b: lookOf(session.AgentWorking), c: lookOf(session.AgentNone), d: commandLook(session.AgentDone)}
	dot := func(p *Pane) look { return dots[p] }
	leaf := func(p *Pane) *node { return &node{pane: p} }
	for _, tc := range []struct {
		tree *node
		want string
	}{
		{leaf(a), "◐"},
		{leaf(c), "○"},
		{&node{a: leaf(a), b: leaf(b)}, "(◐|●)"},
		{&node{a: leaf(a), b: &node{a: leaf(b), b: leaf(c), vertical: true}}, "(◐|(●/○))"},
		{&node{vertical: true, a: &node{a: leaf(a), b: leaf(b)}, b: &node{a: leaf(c), b: leaf(d)}}, "((◐|●)/(○|✓))"},
	} {
		if got := paneMap(tc.tree, dot); got != tc.want {
			t.Errorf("paneMap = %q, want %q", got, tc.want)
		}
	}
}

func TestTabTree(t *testing.T) {
	p := make([]*Pane, 4)
	for i := range p {
		p[i] = &Pane{id: i}
	}
	tab := &Tab{}
	root := &node{vertical: false}
	left := &node{parent: root, vertical: true}
	right := &node{parent: root, vertical: true}
	root.a, root.b = left, right
	left.a, left.b = &node{pane: p[0], parent: left}, &node{pane: p[3], parent: left}
	right.a, right.b = &node{pane: p[1], parent: right}, &node{pane: p[2], parent: right}
	tab.root, tab.focus = root, p[3]
	for _, q := range p {
		q.tab = tab
	}

	got := tab.panes()
	if len(got) != 4 || got[0] != p[0] || got[1] != p[3] || got[2] != p[1] || got[3] != p[2] {
		t.Fatalf("panes() order: %v", got)
	}
	if n := tab.find(p[1]); n == nil || n.pane != p[1] || n.parent != right {
		t.Fatalf("find(p1) = %+v", n)
	}
	if tab.firstPane(right) != p[1] || tab.firstPane(root) != p[0] {
		t.Fatal("firstPane")
	}
	if tab.agentPane() != p[3] {
		t.Fatal("agentPane without agents: want the focused pane")
	}
	p[2].st = session.Status{Agent: "claude", State: session.AgentNeedsYou}
	if tab.agentPane() != p[2] {
		t.Fatal("agentPane: want the first pane running an agent")
	}
	p[1].statusOverride = &session.Status{Agent: "codex"}
	if tab.agentPane() != p[1] {
		t.Fatal("agentPane: an override counts")
	}
	for _, q := range p {
		if want := q != p[3]; tab.dimmed(q) != want {
			t.Errorf("dimmed(%v) = %v", q, !want)
		}
	}
	tab.zoom = p[0]
	if tab.dimmed(p[1]) {
		t.Error("nothing is dimmed while a pane is zoomed")
	}
	single := &Tab{root: &node{pane: p[0]}, focus: p[0]}
	if single.dimmed(p[0]) {
		t.Error("a tab with one pane is not dimmed")
	}
}
