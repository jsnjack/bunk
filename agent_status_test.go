package main

import (
	"bunk/internal/notify"
	"bunk/internal/session"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// bunkOnPath returns a directory holding "bunk", which runs this test
// binary through a symlink named bunk: TestMain then runs the bunk command
// line, so a pane whose PATH starts with the directory can run "bunk
// notify". The wrapper turns off the race detector's one-second sleep at
// exit.
func bunkOnPath(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link, dir := filepath.Join(t.TempDir(), "bunk"), t.TempDir()
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nGORACE=\"$GORACE atexit_sleep_ms=0\" exec '" + link + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bunk"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// listenNotify starts a notify server in a fresh, short XDG_RUNTIME_DIR:
// socket paths must stay under 108 bytes.
func listenNotify(t *testing.T) *notify.Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "bn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("XDG_RUNTIME_DIR", dir)
	srv, err := notify.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	return srv
}

// report is one call to a reportTarget.
type report struct {
	ev   session.Event
	text string
}

// reportTarget records the reports a notify server routes to it.
type reportTarget chan report

func (r reportTarget) Notify(ev session.Event, text string) { r <- report{ev, text} }

// newStatusApp returns an App drawing to a w×h simulation screen, with the
// default keys and no panes.
func newStatusApp(t *testing.T, w, h int) (*App, tcell.SimulationScreen) {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(w, h)
	app := &App{
		screen: scr, theme: testTheme(), keys: resolveKeybindings(nil),
		scrollback: 100, cellAspect: 2,
		redraw: make(chan struct{}, 1), paneDead: make(chan *Pane, 8), done: make(chan struct{}),
		oscBuf: newOSCBuffer(), hostOut: io.Discard,
	}
	t.Cleanup(func() { close(app.done) })
	return app, scr
}

// spawn starts a real pane running args through app.newPane, as a split
// does, and closes it when the test ends.
func spawn(t *testing.T, app *App, x, y, w, h int, args ...string) *Pane {
	t.Helper()
	app.mu.Lock()
	p, err := app.newPane(x, y, w, h, "", args)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// waitStatus polls p's status until ok accepts it.
func waitStatus(t *testing.T, scr tcell.SimulationScreen, p *Pane, what string, ok func(session.Status) bool) session.Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := p.Status()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			w, h := scr.Size()
			var rows []string
			for y := range h {
				rows = append(rows, strings.TrimRight(screenRowString(scr, y, w), " "))
			}
			t.Fatalf("%s: status %+v; screen:\n%s", what, st, strings.Join(rows, "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// cellsText returns the text of row y from column x0 up to x1, skipping
// the second cell of wide characters.
func cellsText(scr tcell.SimulationScreen, y, x0, x1 int) string {
	var b strings.Builder
	for x := x0; x < x1; {
		mainc, combc, _, width := scr.GetContent(x, y) //nolint:staticcheck // rune-level access
		if mainc == 0 {
			mainc = ' '
		}
		b.WriteRune(mainc)
		for _, r := range combc {
			b.WriteRune(r)
		}
		x += max(1, width)
	}
	return b.String()
}

// styleAt returns the style of cell (x, y).
func styleAt(scr tcell.SimulationScreen, x, y int) tcell.Style {
	_, _, style, _ := scr.GetContent(x, y) //nolint:staticcheck // rune-level access
	return style
}

// expectBadge checks that row y ends at rightEdge with text in fg on bg,
// and returns the column where the badge starts.
func expectBadge(t *testing.T, scr tcell.SimulationScreen, y, rightEdge int, text string, fg, bg tcell.Color) int {
	t.Helper()
	x := rightEdge - uniseg.StringWidth(text)
	if got := cellsText(scr, y, x, rightEdge); got != text {
		w, _ := scr.Size()
		t.Fatalf("badge %q, want %q; row %q", got, text, screenRowString(scr, y, w))
	}
	for _, cx := range []int{x, x + 1, rightEdge - 1} {
		gotFG, gotBG, _ := styleAt(scr, cx, y).Decompose()
		if gotFG != fg || gotBG != bg {
			t.Fatalf("badge %q cell %d: colours %v on %v, want %v on %v", text, cx, gotFG, gotBG, fg, bg)
		}
	}
	return x
}

// expectBorder checks the separator cells of row y from x0 to x1.
func expectBorder(t *testing.T, scr tcell.SimulationScreen, y, x0, x1 int, want tcell.Color) {
	t.Helper()
	for x := x0; x < x1; x++ {
		mainc, _, style, _ := scr.GetContent(x, y) //nolint:staticcheck // rune-level access
		if fg, _, _ := style.Decompose(); mainc != tcell.RuneHLine || fg != want {
			t.Fatalf("separator cell (%d,%d): %q in %v, want %q in %v", x, y, mainc, fg, tcell.RuneHLine, want)
		}
	}
}

// TestAgentStatusRealPane runs a fake claude in a real pane, above a plain
// shell pane. It goes idle → working → needs you (OSC 9) → acknowledged by
// focus → needs you (BEL) → acknowledged by typing → done ("bunk notify
// done", through the TUI's notify socket) → failed (OSC 133;D). After each
// step the rendered badge, its colours and the separator are checked.
func TestAgentStatusRealPane(t *testing.T) {
	srv := listenNotify(t)
	bin := bunkOnPath(t)
	script := "#!/bin/sh\nPATH='" + bin + "':$PATH\n" + `IFS= read -r line
echo "fake claude working"
IFS= read -r line
printf '\033]9;Approve edit to main.go?\007'
IFS= read -r line
printf '\007'
IFS= read -r line
bunk notify done "Fixed the race; all tests pass."
echo "notify exit=$?"
IFS= read -r line
printf '\033]133;D;3\007'
sleep 30
`
	claudePath := filepath.Join(bin, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	const w, h = 100, 20
	app, scr := newStatusApp(t, w, h)
	app.notify = srv
	app.timing = fastTiming // the first poll detects claude
	// claude on top (0,0,100,10), a shell below (0,11,100,9); separator row 10.
	claude := spawn(t, app, 0, 0, w, h/2, claudePath)
	shell := spawn(t, app, 0, h/2+1, w, h-h/2-1, "/bin/sh", "-c", "sleep 30")
	app.root = newLeaf(claude, 0, 0, w, h)
	app.root.split(shell, splitHorizontal)
	app.active = shell
	if claude.token == "" || shell.token == "" || claude.token == shell.token {
		t.Fatalf("notify tokens %q and %q", claude.token, shell.token)
	}
	const sepRow = h / 2
	rt := app.theme

	// The script has printed nothing yet: claude is idle.
	waitStatus(t, scr, claude, "agent detected", func(st session.Status) bool {
		return st.Agent == "claude" && st.State == session.AgentIdle
	})
	app.render()
	expectBadge(t, scr, 0, w, " ○ claude idle ", colorAgentIdle, colorAgentBadge)
	expectBorder(t, scr, sepRow, 0, w, rt.activeBorder)
	if got := cellsText(scr, 11, w-12, w); strings.TrimSpace(got) != "" {
		t.Fatalf("the shell pane has a badge: %q", got)
	}

	if err := claude.WriteInput([]byte("go\r")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, scr, claude, "working", func(st session.Status) bool { return st.State == session.AgentWorking })
	app.render()
	expectBadge(t, scr, 0, w, " ● claude working ", colorAgentWorking, colorAgentBadge)

	if err := claude.WriteInput([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, scr, claude, "OSC 9", func(st session.Status) bool {
		return st.State == session.AgentNeedsYou && st.Notice == "Approve edit to main.go?"
	})
	app.render()
	x := expectBadge(t, scr, 0, w, " ◐ claude needs you · Approve edit to main.go? ", colorAgentInk, colorAgentNeeds)
	if _, _, attrs := styleAt(scr, x+1, 0).Decompose(); attrs&tcell.AttrBold == 0 {
		t.Fatal("needs-you badge is not bold")
	}
	expectBorder(t, scr, sepRow, 0, w, colorAgentNeeds)

	// Focusing the pane acknowledges the request.
	app.handleKey(keyEv(tcell.KeyUp, tcell.ModAlt))
	if app.active != claude {
		t.Fatal("Alt+Up did not focus the claude pane")
	}
	if st := claude.Status(); st.State == session.AgentNeedsYou || st.Notice != "" {
		t.Fatalf("focus did not acknowledge: %+v", st)
	}
	app.render()
	if got := cellsText(scr, 0, w-20, w); strings.Contains(got, "needs you") {
		t.Fatalf("badge after focus: %q", got)
	}
	expectBorder(t, scr, sepRow, 0, w, rt.activeBorder)

	// Enter is typed into the pane; the script rings the bell.
	app.handleKey(keyEv(tcell.KeyEnter, 0))
	waitStatus(t, scr, claude, "BEL", func(st session.Status) bool { return st.State == session.AgentNeedsYou })
	app.render()
	expectBadge(t, scr, 0, w, " ◐ claude needs you ", colorAgentInk, colorAgentNeeds)
	// Needs-you takes priority over the active pane's colour.
	expectBorder(t, scr, sepRow, 0, w, colorAgentNeeds)

	// Typing acknowledges, and the key still reaches the pane.
	app.handleKey(runeEv('y', 0))
	if st := claude.Status(); st.State == session.AgentNeedsYou {
		t.Fatalf("typing did not acknowledge: %+v", st)
	}
	app.handleKey(keyEv(tcell.KeyEnter, 0))
	st := waitStatus(t, scr, claude, "bunk notify done", func(st session.Status) bool { return st.State == session.AgentDone })
	if !st.Reported || st.Notice != "Fixed the race; all tests pass." || st.Agent != "claude" {
		t.Fatalf("done report: %+v", st)
	}
	waitStatus(t, scr, claude, "notify output", func(session.Status) bool {
		claude.Lock()
		defer claude.Unlock()
		for y := range 10 {
			if strings.Contains(captureRowText(claude, y), "notify exit=0") {
				return true
			}
		}
		return false
	})
	app.render()
	expectBadge(t, scr, 0, w, " ✓ claude done · Fixed the race; all tests pass. ", colorAgentInk, colorAgentDone)
	expectBorder(t, scr, sepRow, 0, w, colorAgentDone)
	claude.Lock()
	var rows []string
	for y := range 10 {
		rows = append(rows, captureRowText(claude, y))
	}
	claude.Unlock()
	if !slices.Contains(rows, "y") {
		t.Fatalf("typed y did not reach the pane: %q", rows)
	}

	// Enter acknowledges done; the script then reports a failed command.
	app.handleKey(keyEv(tcell.KeyEnter, 0))
	waitStatus(t, scr, claude, "OSC 133;D", func(st session.Status) bool { return st.State == session.AgentFailed })
	app.render()
	expectBadge(t, scr, 0, w, " ✕ claude failed ", colorAgentFailed, colorAgentBadge)
	expectBorder(t, scr, sepRow, 0, w, rt.activeBorder)
}

// captureRowText returns live row y of p's terminal without trailing
// blanks. p must be locked.
func captureRowText(p *Pane, y int) string {
	cols, _ := p.Term.Size()
	var b strings.Builder
	for _, g := range session.CaptureRow(p.Term, y, cols) {
		if g.Width >= 0 && g.Char != 0 {
			b.WriteRune(g.Char)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// threePanes lays out a | (b / c) on a 60×10 screen with fake sessions;
// a is active. Panes use files as PTYs, so typed keys can be read back.
func threePanes(t *testing.T, keys map[string]string) (*App, [3]*Pane, [3]string) {
	t.Helper()
	app, _ := newStatusApp(t, 60, 10)
	app.keys = resolveKeybindings(keys)
	var panes [3]*Pane
	var ptys [3]string
	for i := range panes {
		panes[i] = regressionPane(20, 4)
		f, err := os.CreateTemp(t.TempDir(), "pty")
		if err != nil {
			t.Fatal(err)
		}
		// removePane closes a removed pane's PTY itself.
		t.Cleanup(func() {
			if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		})
		panes[i].PTY, ptys[i] = f, f.Name()
	}
	a, b, c := panes[0], panes[1], panes[2]
	app.root = newLeaf(a, 0, 0, 60, 10)
	b.x, b.y, b.w, b.h = 31, 0, 29, 10
	app.root.split(b, splitVertical)
	c.x, c.y, c.w, c.h = 31, 6, 29, 4
	app.root.right.split(c, splitHorizontal)
	app.active = a
	return app, panes, ptys
}

// TestAgentAcknowledge: focusing a pane (Alt+arrow, a click, the jump key)
// and typing into it acknowledge needs-you and done, as in bunk-gui.
// Working and failed reports stay.
func TestAgentAcknowledge(t *testing.T) {
	t.Run("navigate", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		p[1].Notify(session.EventNeedsYou, "Allow npm install?")
		app.handleKey(keyEv(tcell.KeyRight, tcell.ModAlt))
		if app.active != p[1] {
			t.Fatal("Alt+Right did not focus b")
		}
		if st := p[1].Status(); st.State != session.AgentNone || st.Reported {
			t.Fatalf("b after focus: %+v", st)
		}
	})
	t.Run("click", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		p[2].Notify(session.EventDone, "Finished")
		app.handleMouse(tcell.NewEventMouse(40, 8, tcell.Button1, 0))
		app.handleMouse(tcell.NewEventMouse(40, 8, tcell.ButtonNone, 0))
		if app.active != p[2] {
			t.Fatal("click did not focus c")
		}
		if st := p[2].Status(); st.State != session.AgentNone {
			t.Fatalf("c after click: %+v", st)
		}
	})
	t.Run("typing", func(t *testing.T) {
		app, p, ptys := threePanes(t, nil)
		p[0].Notify(session.EventNeedsYou, "")
		app.handleKey(runeEv('x', 0))
		if st := p[0].Status(); st.State != session.AgentNone {
			t.Fatalf("a after typing: %+v", st)
		}
		waitWritten(t, p[0].Session)
		if got, err := os.ReadFile(ptys[0]); err != nil || string(got) != "x" {
			t.Fatalf("pane input %q, %v; want x", got, err)
		}
	})
	t.Run("quiet states stay", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		p[0].Notify(session.EventWorking, "")
		p[1].Notify(session.EventFailed, "API Error")
		app.handleKey(runeEv('x', 0))
		app.handleKey(keyEv(tcell.KeyRight, tcell.ModAlt))
		if st := p[0].Status(); st.State != session.AgentWorking || !st.Reported {
			t.Fatalf("typing dropped a working report: %+v", st)
		}
		if st := p[1].Status(); st.State != session.AgentFailed || st.Notice != "API Error" {
			t.Fatalf("focus dropped a failed report: %+v", st)
		}
	})
	t.Run("removed pane's neighbour", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		app.active = p[2]
		p[1].Notify(session.EventNeedsYou, "")
		app.removePane(p[2])
		if app.active != p[1] {
			t.Fatalf("focus went to pane %d", app.active.ID)
		}
		if st := p[1].Status(); st.State != session.AgentNone {
			t.Fatalf("b after refocus: %+v", st)
		}
	})
}

// TestNextNeedsYou: Alt+A visits panes needing you, after the active one in
// layout order and wrapping, before panes whose agent is done. With none
// left it flashes a message; it leaves zoom; it skips dead panes.
func TestNextNeedsYou(t *testing.T) {
	jump := runeEv('a', tcell.ModAlt)
	t.Run("needs you before done", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		p[1].Notify(session.EventDone, "")
		p[2].Notify(session.EventNeedsYou, "")
		for i, want := range []*Pane{p[2], p[1]} {
			app.handleKey(jump)
			if app.active != want {
				t.Fatalf("jump %d focused pane %p, want %p", i, app.active, want)
			}
			if st := want.Status(); st.State != session.AgentNone {
				t.Fatalf("jump %d did not acknowledge: %+v", i, st)
			}
		}
		app.handleKey(jump)
		if app.active != p[1] {
			t.Fatal("jump without a target moved focus")
		}
		p[1].Lock()
		msg, until := p[1].statusMsg, p[1].statusMsgEnd
		p[1].Unlock()
		if msg != noAgentNeedsYou || !until.After(time.Now()) {
			t.Fatalf("flash %q until %v", msg, until)
		}
	})
	t.Run("layout order wraps", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		app.active = p[1]
		p[0].Notify(session.EventNeedsYou, "")
		p[2].Notify(session.EventNeedsYou, "")
		for i, want := range []*Pane{p[2], p[0]} {
			app.handleKey(jump)
			if app.active != want {
				t.Fatalf("jump %d: wrong pane", i)
			}
		}
	})
	t.Run("active pane last", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		p[0].Notify(session.EventNeedsYou, "")
		app.handleKey(jump)
		if app.active != p[0] || p[0].Status().State != session.AgentNone {
			t.Fatalf("active pane's own request: active %p, %+v", app.active, p[0].Status())
		}
	})
	t.Run("leaves zoom", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		app.zoomIn()
		p[2].Notify(session.EventNeedsYou, "")
		app.handleKey(jump)
		if app.active != p[2] || app.zoomedPane != nil {
			t.Fatalf("active %p, zoomed %p", app.active, app.zoomedPane)
		}
		if p[0].x != 0 || p[0].w != 30 {
			t.Fatalf("a not restored after zoom: x %d w %d", p[0].x, p[0].w)
		}
	})
	t.Run("skips dead panes", func(t *testing.T) {
		app, p, _ := threePanes(t, nil)
		p[1].Notify(session.EventNeedsYou, "")
		p[1].Dead = true
		p[2].Notify(session.EventDone, "")
		app.handleKey(jump)
		if app.active != p[2] {
			t.Fatal("jump did not skip the dead pane")
		}
	})
}

// TestNextNeedsYouNone: next_needs_you = "none" disables the jump, and
// Alt+A reaches the pane as ESC a.
func TestNextNeedsYouNone(t *testing.T) {
	app, p, ptys := threePanes(t, map[string]string{"next_needs_you": "none"})
	p[1].Notify(session.EventNeedsYou, "")
	app.handleKey(runeEv('a', tcell.ModAlt))
	if app.active != p[0] || p[1].Status().State != session.AgentNeedsYou {
		t.Fatal("disabled key still jumped")
	}
	waitWritten(t, p[0].Session)
	if got, err := os.ReadFile(ptys[0]); err != nil || string(got) != "\x1ba" {
		t.Fatalf("pane input %q, %v; want ESC a", got, err)
	}
	if !strings.Contains(keybindingsHelpText(&app.keys), "none") {
		t.Fatal("help does not show the disabled key")
	}

	// The same from a config file, and a remapped key.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, tc := range []struct {
		value string
		ev    *tcell.EventKey
		match bool
	}{
		{"none", runeEv('a', tcell.ModAlt), false},
		{"f9", keyEv(tcell.KeyF9, 0), true},
		{"", runeEv('a', tcell.ModAlt), true}, // empty keeps the default
	} {
		if err := os.WriteFile(path, []byte("[keys]\nnext_needs_you = \""+tc.value+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Keybindings.NextNeedsYou.Matches(tc.ev); got != tc.match {
			t.Fatalf("next_needs_you = %q: matches %v, want %v", tc.value, got, tc.match)
		}
	}
	// "none" matches nothing, not even the zero key (Ctrl+Space).
	none, err := parseKey("None")
	if err != nil || none.Matches(keyEv(tcell.KeyNUL, 0)) || none.Matches(runeEv('a', tcell.ModAlt)) {
		t.Fatalf("none: %+v, %v", none, err)
	}
}

// TestNotifySocketTUI: bunk's TUI serves the notify socket. A pane gets its
// own socket and token, even when bunk itself runs inside a bunk-gui tab
// whose values it inherited; "bunk notify" in the pane reaches the pane and
// never the GUI. A removed pane's token stops routing.
func TestNotifySocketTUI(t *testing.T) {
	gui := listenNotify(t)
	guiTarget := make(reportTarget, 8)
	gui.Register("gui-token", guiTarget)
	t.Setenv(session.EnvNotifySocket, gui.Path())
	t.Setenv(session.EnvNotifySession, "gui-token")

	srv := listenNotify(t)
	if srv.Path() == gui.Path() || !strings.Contains(srv.Path(), string(filepath.Separator)+"bunk"+string(filepath.Separator)) {
		t.Fatalf("socket %s (GUI %s)", srv.Path(), gui.Path())
	}
	bin := bunkOnPath(t)
	app, scr := newStatusApp(t, 80, 21)
	app.notify = srv
	script := "PATH='" + bin + "':$PATH; read go; bunk notify needs-you \"Allow npm install?\"; echo \"rc=$?\"; " +
		"env | grep '^BUNK_NOTIFY_' | sort; sleep 30"
	agent := spawn(t, app, 0, 0, 80, 10, "/bin/sh", "-c", script)
	other := spawn(t, app, 0, 11, 80, 10, "/bin/sh", "-c", "sleep 30")
	app.root = newLeaf(agent, 0, 0, 80, 21)
	app.root.split(other, splitHorizontal)
	app.active = other
	if err := agent.WriteInput([]byte("go\r")); err != nil {
		t.Fatal(err)
	}

	st := waitStatus(t, scr, agent, "bunk notify needs-you", func(st session.Status) bool {
		return st.State == session.AgentNeedsYou
	})
	if st.Notice != "Allow npm install?" || !st.Reported || st.Agent != "agent" {
		t.Fatalf("report: %+v", st)
	}
	wantEnv := []string{"rc=0", session.EnvNotifySession + "=" + agent.token, session.EnvNotifySocket + "=" + srv.Path()}
	waitStatus(t, scr, agent, "child environment", func(session.Status) bool {
		agent.Lock()
		defer agent.Unlock()
		var rows []string
		for y := range 10 {
			rows = append(rows, captureRowText(agent, y))
		}
		all := strings.Join(rows, "\n")
		for _, want := range wantEnv {
			if !strings.Contains(all, want) {
				return false
			}
		}
		return !strings.Contains(all, "gui-token")
	})
	// The GUI got nothing: the next report it sees is this sentinel.
	if err := notify.Send(gui.Path(), notify.Message{Session: "gui-token", Event: session.EventClear, Text: "sentinel"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-guiTarget:
		if got != (report{session.EventClear, "sentinel"}) {
			t.Fatalf("the GUI received %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sentinel")
	}

	// After removal the token routes nothing. The server reads one
	// connection at a time, so the other pane's report arrives after the
	// dropped one.
	token := agent.token
	app.removePane(agent)
	for _, m := range []notify.Message{{Session: token, Event: session.EventClear}, {Session: other.token, Event: session.EventWorking}} {
		if err := notify.Send(srv.Path(), m); err != nil {
			t.Fatal(err)
		}
	}
	waitStatus(t, scr, other, "sentinel report", func(st session.Status) bool { return st.Reported })
	if st := agent.Status(); st.State != session.AgentNeedsYou {
		t.Fatalf("a removed pane's token still routes: %+v", st)
	}
}

// TestBunkNotifyCLI runs "bunk notify" as agent hooks do: through cobra,
// with exit codes 0 or 1 (never 2) and nothing on stdout except help.
func TestBunkNotifyCLI(t *testing.T) {
	srv := listenNotify(t)
	target := make(reportTarget, 8)
	srv.Register("tok", target)
	bunk := filepath.Join(bunkOnPath(t), "bunk")
	inside := []string{session.EnvNotifySocket + "=" + srv.Path(), session.EnvNotifySession + "=tok"}
	gone := []string{session.EnvNotifySocket + "=" + filepath.Join(t.TempDir(), "gone.sock"), session.EnvNotifySession + "=tok"}
	const claudeNotification = `{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`
	for _, tc := range []struct {
		name   string
		args   []string
		env    []string
		stdin  string
		code   int
		stdout string // substring; "" means none
		stderr string // substring; "" means none
		want   *report
	}{
		{name: "outside bunk", args: []string{"notify", "done", "hi"}},
		{name: "report", args: []string{"notify", "needs-you", "Allow", "npm", "install?"}, env: inside,
			want: &report{session.EventNeedsYou, "Allow npm install?"}},
		{name: "hook input on stdin", args: []string{"notify", "needs-you", "-"}, env: inside, stdin: claudeNotification,
			want: &report{session.EventNeedsYou, "Claude needs your permission to use Bash"}},
		{name: "words that look like flags", args: []string{"notify", "failed", "--config", "-x"}, env: inside,
			want: &report{session.EventFailed, "--config -x"}},
		{name: "front end gone", args: []string{"notify", "done"}, env: gone},
		{name: "unknown event", args: []string{"notify", "waiting"}, env: inside, code: 1, stderr: `bunk notify: unknown event "waiting"`},
		{name: "no event", args: []string{"notify"}, code: 1, stderr: "usage: bunk notify"},
		{name: "help", args: []string{"notify", "--help"}, stdout: "usage: bunk notify"},
		{name: "hook snippet", args: []string{"notify", "hooks", "claude"}, stdout: `notify needs-you -`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bunk, tc.args...)
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, tc.env...)
			cmd.Stdin = strings.NewReader(tc.stdin)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := cmd.ProcessState.ExitCode()
			if code != tc.code || (err != nil && code == 0) {
				t.Fatalf("exit %d (%v), want %d; stderr %q", code, err, tc.code, stderr.String())
			}
			for _, out := range []struct{ name, got, want string }{
				{"stdout", stdout.String(), tc.stdout}, {"stderr", stderr.String(), tc.stderr},
			} {
				if (out.want == "") != (out.got == "") || !strings.Contains(out.got, out.want) {
					t.Fatalf("%s = %q, want %q", out.name, out.got, out.want)
				}
			}
			if tc.want == nil {
				return
			}
			select {
			case got := <-target:
				if got != *tc.want {
					t.Fatalf("report %+v, want %+v", got, *tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no report")
			}
		})
	}
	if len(target) != 0 {
		t.Fatalf("unexpected report %+v", <-target)
	}
}

// TestAgentBadge draws the agent badge in each state and width: the notice
// shows when there is room, cut with an ellipsis, up to two thirds of the
// row; narrow panes drop the name, then the label; PASS keeps the edge.
func TestAgentBadge(t *testing.T) {
	long := "Claude needs your permission to use Bash for npm install"
	for _, tc := range []struct {
		name   string
		width  int // pane content width (the PTY is one column narrower)
		agent  paneAgent
		pass   bool
		want   string // the row's right end
		fg, bg tcell.Color
	}{
		{"working", 60, paneAgent{"claude", session.AgentWorking, ""}, false, " ● claude working ", colorAgentWorking, colorAgentBadge},
		{"idle", 60, paneAgent{"codex", session.AgentIdle, ""}, false, " ○ codex idle ", colorAgentIdle, colorAgentBadge},
		{"failed with notice", 60, paneAgent{"claude", session.AgentFailed, "API Error: 429"}, false,
			" ✕ claude failed · API Error: 429 ", colorAgentFailed, colorAgentBadge},
		{"needs you", 90, paneAgent{"claude", session.AgentNeedsYou, long}, false,
			" ◐ claude needs you · Claude needs your permission to use… ", colorAgentInk, colorAgentNeeds},
		{"done", 60, paneAgent{"agent", session.AgentDone, "完了しました"}, false, " ✓ agent done · 完了しました ", colorAgentInk, colorAgentDone},
		{"no room for the notice", 30, paneAgent{"claude", session.AgentNeedsYou, long}, false, " ◐ claude needs you ", colorAgentInk, colorAgentNeeds},
		{"without the name", 16, paneAgent{"claude", session.AgentNeedsYou, long}, false, " ◐ needs you ", colorAgentInk, colorAgentNeeds},
		{"dot only", 4, paneAgent{"claude", session.AgentDone, ""}, false, " ✓ ", colorAgentInk, colorAgentDone},
		{"after PASS", 30, paneAgent{"claude", session.AgentWorking, ""}, true, " ● claude working  PASS ", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scr := tcell.NewSimulationScreen("UTF-8")
			if err := scr.Init(); err != nil {
				t.Fatal(err)
			}
			defer scr.Fini()
			scr.SetSize(100, 2)
			p := regressionPane(tc.width-1, 2)
			p.agent, p.passthrough = tc.agent, tc.pass
			drawPaneStatus(scr, p, false, testTheme(), false)
			row := strings.TrimRight(cellsText(scr, 0, 0, tc.width), " ")
			if !strings.HasSuffix(row+" ", tc.want) {
				t.Fatalf("row %q, want it to end in %q", row, tc.want)
			}
			if uniseg.StringWidth(row) > tc.width {
				t.Fatalf("badges overflow the pane: %q", row)
			}
			if tc.fg != 0 {
				expectBadge(t, scr, 0, tc.width, tc.want, tc.fg, tc.bg)
			}
		})
	}
	// No agent: no agent badge.
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(40, 2)
	drawPaneStatus(scr, regressionPane(39, 2), false, testTheme(), false)
	if row := strings.TrimSpace(cellsText(scr, 0, 0, 40)); row != "" {
		t.Fatalf("badge without an agent: %q", row)
	}
}

// TestStatusChangedWakesRender: the StatusChanged hook asks for a frame and
// never blocks, even with a frame already pending.
func TestStatusChangedWakesRender(t *testing.T) {
	p := regressionPane(10, 2)
	redraw := make(chan struct{}, 1)
	hooks := p.hooks(redraw, make(chan *Pane, 1), make(chan struct{}), newOSCBuffer())
	finished := make(chan struct{})
	go func() {
		hooks.StatusChanged()
		hooks.StatusChanged()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("StatusChanged blocked")
	}
	if len(redraw) != 1 {
		t.Fatal("StatusChanged did not ask for a frame")
	}
}

// TestAgentBorders: in a | (b / c), the separators that touch a needs-you
// or done pane take its colour, only along that pane; needs-you wins over
// done and over the focused pane's colour.
func TestAgentBorders(t *testing.T) {
	app, p, _ := threePanes(t, nil)
	scr := app.screen.(tcell.SimulationScreen)
	rt := app.theme
	colour := func(x, y int) tcell.Color {
		fg, _, _ := styleAt(scr, x, y).Decompose()
		return fg
	}
	// Column 30 separates a from b (rows 0-4), from nothing but the b/c
	// separator (row 5) and from c (rows 6-9); row 5 separates b from c
	// over columns 31-59.
	check := func(step string, top, mid, bottom, horiz tcell.Color) {
		t.Helper()
		app.render()
		for y := range 10 {
			want := top
			switch {
			case y == 5:
				want = mid
			case y > 5:
				want = bottom
			}
			if got := colour(30, y); got != want {
				t.Fatalf("%s: separator (30,%d) %v, want %v", step, y, got, want)
			}
		}
		for x := 31; x < 60; x++ {
			if got := colour(x, 5); got != horiz {
				t.Fatalf("%s: separator (%d,5) %v, want %v", step, x, got, horiz)
			}
		}
	}
	active := rt.activeBorder
	check("a active, no agents", active, active, active, rt.inactiveBorder)
	p[2].Notify(session.EventDone, "")
	check("c done", active, active, colorAgentDone, colorAgentDone)
	p[1].Notify(session.EventNeedsYou, "")
	check("b needs you", colorAgentNeeds, active, colorAgentDone, colorAgentNeeds)
	p[0].Notify(session.EventNeedsYou, "")
	check("a needs you too", colorAgentNeeds, colorAgentNeeds, colorAgentNeeds, colorAgentNeeds)
}

// TestNextNeedsYouKeyFreeInReadline: Alt+A, the default next_needs_you key,
// is unbound in readline's default emacs keymap, so bunk takes nothing from
// bash. Alt+R (revert-line) proves that the keymap was read.
func TestNextNeedsYouKeyFreeInReadline(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	if def := keybindingDefaultOf(t, "next_needs_you"); def != "alt+a" {
		t.Fatalf("default next_needs_you %q; check the new key against readline", def)
	}
	cmd := exec.Command(bash, "--norc", "--noprofile", "-c", "bind -p")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "INPUTRC=/dev/null", "HOME=" + t.TempDir()}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	bound := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if key, fn, ok := strings.Cut(line, ": "); ok {
			bound[key] = fn
		}
	}
	if bound[`"\er"`] != "revert-line" {
		t.Fatalf("readline keymap not read: %d bindings", len(bound))
	}
	if fn, ok := bound[`"\ea"`]; ok {
		t.Fatalf("readline binds Alt+A to %s", fn)
	}
	if fn := bound[`"\eA"`]; fn != "" && fn != "do-lowercase-version" {
		t.Fatalf("readline binds Alt+Shift+A to %s", fn)
	}
}

// keybindingDefaultOf returns the built-in key of action.
func keybindingDefaultOf(t *testing.T, action string) string {
	t.Helper()
	for _, e := range keybindingDefaults {
		if e.action == action {
			return e.def
		}
	}
	t.Fatalf("no action %q", action)
	return ""
}
