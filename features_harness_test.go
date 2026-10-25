package main

// Harness for the end-to-end tests of scripts/terminal_features (see
// terminal_features_test.go). It runs one group script the way a person
// does: bash on a real PTY in a bunk pane, wired through bunk's own session
// hooks, key handling and render path. The test plays the person at the
// keyboard and reads the cells bunk paints on a tcell simulation screen.

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"bunk/internal/session"
	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// featureTimeout bounds every wait on a script. A healthy run needs a small
// fraction of it.
const featureTimeout = 10 * time.Second

// cursorScreen records the cursor style and colour bunk asks tcell for.
type cursorScreen struct {
	tcell.SimulationScreen
	mu    sync.Mutex
	style tcell.CursorStyle
	color tcell.Color
}

func (s *cursorScreen) SetCursorStyle(style tcell.CursorStyle, colors ...tcell.Color) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.style = style
	if len(colors) > 0 {
		s.color = colors[0]
	}
}

func (s *cursorScreen) cursorStyle() (tcell.CursorStyle, tcell.Color) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.style, s.color
}

// scriptPane is one feature script running in a bunk pane at (0, 0).
type scriptPane struct {
	t      *testing.T
	app    *App
	pane   *Pane
	scr    *cursorScreen
	host   bytes.Buffer  // what render wrote to the host terminal
	output chan struct{} // signalled after each chunk of output
	closed chan struct{} // closed once all output is applied
	exited chan *Pane    // receives the pane when bash exits
	done   chan struct{}
}

// runFeatureScript starts scripts/terminal_features/<script> in a pane of
// cols x rows cells, plus bunk's scrollbar column. Pauses wait for Enter.
func runFeatureScript(t *testing.T, script string, cols, rows int) *scriptPane {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(cols+1, rows)
	theme := testTheme()
	fg, bg, cursor := defaultOSCColors(theme, hostOSCColors{})
	sp := &scriptPane{
		t:      t,
		scr:    &cursorScreen{SimulationScreen: sim},
		output: make(chan struct{}, 1),
		closed: make(chan struct{}),
		exited: make(chan *Pane, 1),
		done:   make(chan struct{}),
	}
	// NewPane's options, plus a fixed locale and interactive pauses.
	s, err := session.Start(session.Options{
		ID: 1, Cols: cols, Rows: rows, Scrollback: 1000,
		Args:    []string{"bash", filepath.Join("scripts", "terminal_features", script)},
		Env:     []string{"BUNK=1", "LC_ALL=C.UTF-8", "TERMINAL_FEATURES_AUTO=0"},
		ThemeFG: fg, ThemeBG: bg, ThemeCursor: cursor,
		CellAspect: []float64{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pane{Session: s, w: cols + 1, h: rows}
	sp.pane = p
	redraw := make(chan struct{}, 1)
	oscBuf := newOSCBuffer()
	sp.app = &App{
		screen: sp.scr, theme: theme, keys: resolveKeybindings(nil), cellAspect: 2,
		root: newLeaf(p, 0, 0, cols+1, rows), active: p,
		redraw: redraw, oscBuf: oscBuf, hostOut: &sp.host, lastCursorStyle: -1,
	}
	hooks := p.hooks(redraw, sp.exited, sp.done, oscBuf)
	output := hooks.Output
	hooks.Output = func(chunk []byte, scrolled, syncing bool) {
		output(chunk, scrolled, syncing)
		select {
		case sp.output <- struct{}{}:
		default:
		}
	}
	hooks.Closed = func() { close(sp.closed) }
	s.Run(hooks, sp.done)
	t.Cleanup(func() {
		close(sp.done)
		p.Close()
		select {
		case <-sp.closed:
		case <-time.After(featureTimeout):
			t.Error("pane output never closed")
		}
		sim.Fini()
	})
	return sp
}

// waitUntil waits for cond, checked with the session locked after each
// chunk of output.
func (sp *scriptPane) waitUntil(what string, cond func() bool) {
	sp.t.Helper()
	check := func() bool {
		sp.pane.Lock()
		defer sp.pane.Unlock()
		return cond()
	}
	deadline := time.After(featureTimeout)
	for !check() {
		select {
		case <-sp.output:
		case <-sp.closed:
			if !check() {
				sp.render()
				sp.t.Fatalf("script ended before %s:\n%s", what, sp.dump())
			}
			return
		case <-deadline:
			sp.render()
			sp.t.Fatalf("timed out waiting for %s:\n%s", what, sp.dump())
		}
	}
}

// beforeCursor returns the text left of the cursor, including the rows
// that soft-wrap into the cursor row, so that a prompt wrapped at the pane
// edge still matches. The session must be locked.
func (sp *scriptPane) beforeCursor() string {
	term := sp.pane.Term
	cur := term.Cursor()
	cols, _ := term.Size()
	wrapped := func(y int) bool {
		return term.Cell(cols-1, y).Mode&vt10x.AttrWrap != 0 ||
			cols > 1 && term.Cell(cols-2, y).Mode&vt10x.AttrWrap != 0
	}
	top := cur.Y
	for top > 0 && wrapped(top-1) {
		top--
	}
	var b strings.Builder
	for y := top; y <= cur.Y; y++ {
		end := cols
		if y == cur.Y {
			end = cur.X
		}
		for x := 0; x < end; x++ {
			b.WriteString(term.Cell(x, y).Text())
		}
	}
	return b.String()
}

// waitPrompt waits until the script has printed prompt and waits for input.
func (sp *scriptPane) waitPrompt(prompt string) {
	sp.t.Helper()
	want := strings.TrimRight(prompt, " ")
	sp.waitUntil("prompt "+prompt, func() bool {
		return strings.HasSuffix(strings.TrimRight(sp.beforeCursor(), " "), want)
	})
}

// press sends a key through bunk's key handling, as the host reports it.
func (sp *scriptPane) press(key tcell.Key, r rune, mod tcell.ModMask) {
	sp.t.Helper()
	if !sp.app.handleKey(tcell.NewEventKey(key, r, mod)) {
		sp.t.Fatalf("key %v %q quit bunk", key, r)
	}
}

// pressEnter answers a pause and waits for the terminal to echo it.
func (sp *scriptPane) pressEnter() {
	sp.t.Helper()
	sp.pane.Lock()
	before, saved := sp.pane.Term.Cursor(), sp.pane.Scrollback.Count
	sp.pane.Unlock()
	sp.press(tcell.KeyEnter, 0, tcell.ModNone)
	sp.waitUntil("the echo of Enter", func() bool {
		cur := sp.pane.Term.Cursor()
		return cur.X != before.X || cur.Y != before.Y || sp.pane.Scrollback.Count != saved
	})
}

// paste delivers text the way a host terminal with bracketed paste does:
// paste start, the text as key events, paste end.
func (sp *scriptPane) paste(text string) {
	sp.t.Helper()
	sp.app.handlePaste(tcell.NewEventPaste(true))
	for _, r := range text {
		sp.press(tcell.KeyRune, r, tcell.ModNone)
	}
	sp.app.handlePaste(tcell.NewEventPaste(false))
}

// finish waits for bash to exit with status 0 and for all of its output.
func (sp *scriptPane) finish() {
	sp.t.Helper()
	timeout := time.After(featureTimeout)
	select {
	case <-sp.closed:
	case <-timeout:
		sp.render()
		sp.t.Fatalf("script output did not end:\n%s", sp.dump())
	}
	select {
	case <-sp.exited:
	case <-timeout:
		sp.t.Fatal("script did not exit")
	}
	if code := sp.pane.Cmd.ProcessState.ExitCode(); code != 0 {
		sp.render()
		sp.t.Fatalf("script exited with status %d:\n%s", code, sp.dump())
	}
}

// render paints the panes through bunk's render path.
func (sp *scriptPane) render() { sp.app.render() }

// resizeHost resizes the host screen and waits for bunk's coalesced resize.
func (sp *scriptPane) resizeHost(w, h int) {
	sp.t.Helper()
	sp.scr.SetSize(w, h)
	sp.app.handleResize()
	deadline := time.Now().Add(featureTimeout)
	for {
		sp.app.mu.Lock()
		sp.pane.Lock()
		ok := sp.pane.w == w && sp.pane.h == h
		sp.pane.Unlock()
		sp.app.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			sp.t.Fatalf("resize to %dx%d never applied", w, h)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// screenCell is one painted cell of the simulation screen.
type screenCell struct {
	x     int
	text  string
	width int
	style tcell.Style
}

// row returns the painted cells of screen row y. Like a host terminal, it
// skips the right half of a two-column cell.
func (sp *scriptPane) row(y int) []screenCell {
	w, _ := sp.scr.Size()
	var cells []screenCell
	for x := 0; x < w; x++ {
		text, style, width := sp.scr.Get(x, y)
		cells = append(cells, screenCell{x: x, text: text, width: width, style: style})
		if width == 2 {
			x++
		}
	}
	return cells
}

// rowText returns the painted text of screen row y without trailing blanks.
func (sp *scriptPane) rowText(y int) string {
	var b strings.Builder
	for _, c := range sp.row(y) {
		b.WriteString(c.text)
	}
	return strings.TrimRight(b.String(), " ")
}

// rows returns the painted text of every screen row.
func (sp *scriptPane) rows() []string {
	_, h := sp.scr.Size()
	rows := make([]string, h)
	for y := range rows {
		rows[y] = sp.rowText(y)
	}
	return rows
}

// dump returns the painted screen for failure messages.
func (sp *scriptPane) dump() string {
	return strings.Join(sp.rows(), "\n")
}

// findIn returns the cell index in row of the first painted occurrence of
// text at or after cell index from, or -1.
func findIn(row []screenCell, from int, text string) int {
	for i := from; i < len(row); i++ {
		var b strings.Builder
		for j := i; j < len(row) && b.Len() < len(text); j++ {
			b.WriteString(row[j].text)
		}
		if strings.HasPrefix(b.String(), text) {
			return i
		}
	}
	return -1
}

// find returns the screen row and its cells for the first row that shows
// text, and the cell index where text starts.
func (sp *scriptPane) find(text string) (y int, row []screenCell, i int) {
	sp.t.Helper()
	_, h := sp.scr.Size()
	for y := 0; y < h; y++ {
		row := sp.row(y)
		if i := findIn(row, 0, text); i >= 0 {
			return y, row, i
		}
	}
	sp.t.Fatalf("%q is not on screen:\n%s", text, sp.dump())
	return 0, nil, 0
}

// spanAfter returns the screen row and painted cells of text on the first
// row that shows label followed by text.
func (sp *scriptPane) spanAfter(label, text string) (int, []screenCell) {
	sp.t.Helper()
	_, h := sp.scr.Size()
	for y := 0; y < h; y++ {
		row := sp.row(y)
		i := findIn(row, 0, label)
		if i < 0 {
			continue
		}
		j := findIn(row, i+1, text)
		if j < 0 {
			continue
		}
		k, n := j, 0
		for ; k < len(row) && n < len(text); k++ {
			n += len(row[k].text)
		}
		return y, row[j:k]
	}
	sp.t.Fatalf("no row shows %q then %q:\n%s", label, text, sp.dump())
	return 0, nil
}

// around returns the pane cells of screen row y left and right of span.
func (sp *scriptPane) around(y int, span []screenCell) (left, right []screenCell) {
	for _, c := range sp.content(sp.row(y)) {
		switch {
		case c.x < span[0].x:
			left = append(left, c)
		case c.x > span[len(span)-1].x:
			right = append(right, c)
		}
	}
	return left, right
}

// content returns the cells of row inside the pane, without the
// scrollbar column.
func (sp *scriptPane) content(row []screenCell) []screenCell {
	sp.app.mu.Lock()
	w := sp.pane.w
	sp.app.mu.Unlock()
	end := slices.IndexFunc(row, func(c screenCell) bool { return c.x >= w-1 })
	if end < 0 {
		return row
	}
	return row[:end]
}
