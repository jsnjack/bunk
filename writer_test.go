package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"bunk/internal/ptychild"
	"bunk/internal/session"

	"github.com/gdamore/tcell/v2"
)

// closeWriter waits until s has written everything it queued for its PTY,
// then closes w, the write end of the pipe that stands in for the PTY, so
// the test can read the pipe to its end.
func closeWriter(t *testing.T, s *session.Session, w *os.File) {
	t.Helper()
	if !s.WaitWritten(5 * time.Second) {
		t.Fatal("the session did not write its queued bytes")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// waitWritten waits until s has written everything it queued for its PTY.
func waitWritten(t *testing.T, s *session.Session) {
	t.Helper()
	if !s.WaitWritten(5 * time.Second) {
		t.Fatal("the session did not write its queued bytes")
	}
}

// within runs f on another goroutine and returns how long it took. It
// fails the test when f has not returned after limit: f is stuck, and it
// stays stuck until the test's cleanup closes the panes.
func within(t *testing.T, limit time.Duration, what string, f func()) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("%s did not return within %v", what, limit)
		return 0
	}
}

// regionHas reports whether columns x0 to x1 of some row show text.
func regionHas(scr tcell.SimulationScreen, x0, x1 int, text string) bool {
	_, h := scr.Size()
	for y := range h {
		if strings.Contains(cellsText(scr, y, x0, x1), text) {
			return true
		}
	}
	return false
}

// tuiBound is how long a key, a paste event or a frame may take while a
// pane's child floods queries and reads nothing.
const tuiBound = 100 * time.Millisecond

// TestTUINeverReadingPane: the left pane runs a raw-mode child that floods
// queries and never reads its input; the right pane runs a shell. The user
// types and pastes into the left pane, then types into the right one. Keys,
// paste events and frames return at once throughout; the right pane's echo
// reaches the screen; a paste past the left pane's input cap is refused
// whole, and the pane flashes inputRefused.
func TestTUINeverReadingPane(t *testing.T) {
	const w, h = 100, 20
	const stuckCap = 16 << 10
	app, scr := newStatusApp(t, w, h)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"env"}, ptychild.Environ(30000, "", "")...), exe)
	stuck, err := startPane(paneSpec{
		id: 1, w: w / 2, h: h, scrollback: 100, args: args, redraw: app.redraw, paneDead: app.paneDead,
		done: app.done, oscBuf: app.oscBuf, cellAspect: []float64{2}, inputBytes: stuckCap,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stuck.Close)
	shell := spawn(t, app, w/2+1, 0, w-w/2-1, h, "/bin/sh")
	app.root = newLeaf(stuck, 0, 0, w, h)
	app.root.split(shell, splitVertical)
	app.active = stuck

	var worst time.Duration
	step := func(what string, f func()) {
		t.Helper()
		worst = max(worst, within(t, 5*time.Second, what, f))
	}
	key := func(what string, ev *tcell.EventKey) {
		t.Helper()
		step(what, func() {
			if !app.handleKey(ev) {
				t.Errorf("%s: the key quit bunk", what)
			}
		})
	}

	// The flood: frames keep coming, and the stuck pane's output is applied
	// to its end although nothing reads the replies.
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		step("a frame during the flood", app.render)
		if regionHas(scr, 0, w/2, ptychild.FloodDone) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never reached the screen (writer %+v)", ptychild.FloodDone, stuck.WriteStats())
		}
	}
	if ws := stuck.WriteStats(); !ws.Writing || ws.DroppedReplies == 0 {
		t.Fatalf("writer %+v: want replies dropped and a write waiting for the child", ws)
	}

	for range 2000 {
		key("a key into the stuck pane", runeEv('k', 0))
	}
	step("a frame after typing", app.render)

	// A paste one byte past the cap is refused whole.
	step("paste start", func() { app.handlePaste(tcell.NewEventPaste(true)) })
	for range stuckCap + 1 {
		key("a pasted key", runeEv('p', 0))
	}
	step("paste end", func() { app.handlePaste(tcell.NewEventPaste(false)) })
	ws := stuck.WriteStats()
	if ws.RefusedInput != 1 || ws.PendingInput > stuckCap || ws.PendingInput < 2000 {
		t.Errorf("after the paste: %+v; want it refused and the typed keys queued", ws)
	}
	step("a frame after the paste", app.render)
	if !regionHas(scr, 0, w/2, inputRefused) {
		t.Errorf("the stuck pane does not flash %q", inputRefused)
	}

	// The shell beside it still echoes what the user types.
	key("Alt+Right", keyEv(tcell.KeyRight, tcell.ModAlt))
	if app.active != shell {
		t.Fatal("Alt+Right did not focus the shell")
	}
	for _, r := range "echo tui-ok" {
		key("a key into the shell", runeEv(r, 0))
	}
	key("Enter", keyEv(tcell.KeyEnter, 0))
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		step("a frame while the shell echoes", app.render)
		if strings.Count(screenText(scr, w/2+1, w), "tui-ok") >= 2 { // the command line and its output
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the shell's echo never reached the screen:\n%s", screenText(scr, w/2+1, w))
		}
	}
	t.Logf("slowest key, paste event or frame: %v; stuck pane %+v", worst, stuck.WriteStats())
	if worst > tuiBound {
		t.Errorf("a key, paste event or frame took %v, want at most %v", worst, tuiBound)
	}
}

// screenText returns columns x0 to x1 of every row, one line each.
func screenText(scr tcell.SimulationScreen, x0, x1 int) string {
	_, h := scr.Size()
	rows := make([]string, h)
	for y := range h {
		rows[y] = strings.TrimRight(cellsText(scr, y, x0, x1), " ")
	}
	return strings.Join(rows, "\n")
}

// TestTUIHostPasteIsOneEvent: a paste from the host terminal reaches the
// pane as one input event, so a reply to a query the pane answered during
// the paste comes before it, not inside the bracketed text. A paste whose
// end marker never comes is sent after pasteTimeout.
func TestTUIHostPasteIsOneEvent(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() }) //nolint:errcheck // test pipe cleanup
	p := regressionPane(20, 3)
	p.PTY = pw
	p.CaptureAndWrite([]byte("\x1b[?2004h"))
	app := &App{root: newLeaf(p, 0, 0, 21, 3), active: p, keys: resolveKeybindings(nil), redraw: make(chan struct{}, 1)}

	app.handlePaste(tcell.NewEventPaste(true))
	app.handleKey(runeEv('a', 0))
	app.handleKey(runeEv('b', 0))
	p.CaptureAndWrite([]byte("\x1b[5n")) // the application asks during the paste
	app.handleKey(runeEv('c', 0))
	app.handlePaste(tcell.NewEventPaste(false))

	// No end marker: the paste goes out after pasteTimeout, then keys
	// go one by one again.
	app.handlePaste(tcell.NewEventPaste(true))
	app.handleKey(runeEv('d', 0))
	time.Sleep(pasteTimeout + 200*time.Millisecond)
	app.handleKey(runeEv('e', 0))

	closeWriter(t, p.Session, pw)
	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1b[0n" + "\x1b[200~abc\x1b[201~" + "\x1b[200~d\x1b[201~" + "e"
	if string(got) != want {
		t.Errorf("the pane got %q, want %q", got, want)
	}
}
