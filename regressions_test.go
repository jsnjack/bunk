package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"bunk/internal/session"
	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

func regressionPane(cols, rows int) *Pane {
	p := &Pane{Session: &session.Session{Cmd: &exec.Cmd{}, ScrollbackLines: 1000, Scrollback: session.Ring{MaxLines: 1000}}, w: cols + 1, h: rows}
	p.Term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.OnScrollRow), vt10x.WithScrollbackClearCallback(p.OnScrollbackClear))
	return p
}

func TestRegressionResizeRetainsModes(t *testing.T) {
	for _, size := range [][2]int{{39, 3}, {40, 4}, {40, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := regressionPane(40, 3)
			p.CaptureAndWrite([]byte("hello\x1b[?2004h\x1b[?1004h\x1b[?25l\x1b[5 q"))
			before := p.Term.Mode()
			p.resize(0, 0, size[0]+1, size[1])
			if p.Term.Mode() != before || p.Term.Cursor().Shape != 5 {
				t.Fatalf("resize lost terminal state: modes %v -> %v; shape 5 -> %d", before, p.Term.Mode(), p.Term.Cursor().Shape)
			}
		})
	}
}

func TestRegressionClearAfterResize(t *testing.T) {
	for _, size := range [][2]int{{39, 3}, {40, 4}, {40, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := regressionPane(40, 3)
			p.CaptureAndWrite([]byte(strings.Repeat("history\r\n", 20)))
			p.resize(0, 0, size[0]+1, size[1])
			p.CaptureAndWrite([]byte("fresh\r\nmore\r\nlines\r\n"))
			if p.Scrollback.Count == 0 {
				t.Fatal("setup produced no scrollback")
			}
			p.CaptureAndWrite([]byte("\x1b[3J"))
			if p.Scrollback.Count != 0 {
				t.Fatalf("ED3 failed after resize: %d saved rows remain", p.Scrollback.Count)
			}
		})
	}
}

func TestRegressionShortLineHistory(t *testing.T) {
	p := regressionPane(80, 3)
	for i := 0; i < 100; i++ {
		p.CaptureAndWrite([]byte(fmt.Sprintf("%03d\r\n", i)))
	}
	before := p.Scrollback.Count
	p.resize(0, 0, 80, 3)
	if p.Scrollback.Count != before {
		t.Fatalf("width 80 -> 79 discarded short-line history: %d -> %d rows", before, p.Scrollback.Count)
	}
}

func TestRegressionWideCharacterWrap(t *testing.T) {
	p := regressionPane(5, 3)
	p.CaptureAndWrite([]byte("界界AB"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(6, 3)
	renderPane(scr, p, testTheme())
	got, _, _, _ := scr.GetContent(0, 1) //nolint:staticcheck // inspect the rendered rune
	if got != 'B' {
		t.Fatalf("B should wrap after five display columns; row 2 starts with %q", got)
	}
}

// TestRegressionKittySurvivesFirstPoll: the first foreground-process poll
// keeps the kitty keyboard flags of the child that negotiated them and
// still runs.
func TestRegressionKittySurvivesFirstPoll(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	p, err := startPane(paneSpec{
		w: 40, h: 3, scrollback: 100, args: []string{"/bin/sh", "-c", "printf '\033[>1u'; sleep 5"},
		redraw: make(chan struct{}, 1), paneDead: make(chan *Pane, 1), done: done, oscBuf: newOSCBuffer(),
		timing: fastTiming,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer close(done)
	defer p.Close()
	deadline := time.Now().Add(10 * time.Second)
	negotiated := false
	for time.Now().Before(deadline) {
		p.Lock()
		negotiated = len(p.KittyStack) > 0
		p.Unlock()
		if negotiated {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !negotiated {
		t.Fatal("child failed to negotiate kitty mode")
	}
	// Polls run every fastTiming.Poll and the first names the foreground
	// process. Ten more come after the negotiation.
	time.Sleep(10 * fastTiming.Poll)
	for deadline := time.Now().Add(10 * time.Second); p.Status().Process == ""; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no foreground-process poll")
		}
	}
	p.Lock()
	defer p.Unlock()
	if len(p.KittyStack) == 0 {
		t.Fatal("first foreground poll erased currently running child's kitty negotiation")
	}
}

func TestRegressionRepeatedTransientClear(t *testing.T) {
	t.Parallel()
	p := regressionPane(10, 3)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.PTY = r
	readDone := make(chan struct{})
	redraw := make(chan struct{}, 1)
	go func() { p.ReadLoop(p.hooks(redraw, nil, nil, newOSCBuffer())); close(readDone) }()
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		<-readDone
	}()
	for n := 0; n < 2; n++ {
		if _, err := w.Write([]byte("\r   \r")); err != nil {
			t.Fatal(err)
		}
		// A clear-only chunk asks for a repaint when its timer expired.
		select {
		case <-redraw:
		case <-time.After(5 * time.Second):
			t.Fatalf("clear %d: no repaint after its timer", n+1)
		}
		p.Lock()
		held := p.transientLineClear
		p.Unlock()
		if held {
			t.Fatalf("clear %d remains render-suppressed after timer expiry", n+1)
		}
	}
}

func TestRegressionSyncDoesNotFreezeOtherPane(t *testing.T) {
	p, other := regressionPane(10, 3), regressionPane(10, 3)
	other.x = 12
	p.CaptureAndWrite([]byte("\x1b[?2026h"))
	other.CaptureAndWrite([]byte("updated"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(23, 3)
	app := &App{screen: scr, root: &Node{left: &Node{pane: p}, right: &Node{pane: other}}, active: p, oscBuf: newOSCBuffer(), theme: testTheme()}
	app.render()
	got, _, _, _ := scr.GetContent(12, 0) //nolint:staticcheck // inspect the rendered rune
	if got != 'u' {
		t.Fatalf("other pane's output suppressed by active pane's sync mode: got %q", got)
	}
}

func TestRegressionMouseReleaseForwarding(t *testing.T) {
	for _, mode := range []int{1000, 1002, 1003} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			p := regressionPane(10, 3)
			f, err := os.CreateTemp(t.TempDir(), "input")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			}()
			p.PTY = f
			p.CaptureAndWrite([]byte(fmt.Sprintf("\x1b[?%dh\x1b[?1006h", mode)))
			app := &App{root: newLeaf(p, 0, 0, 11, 3), active: p}
			app.handleMouse(tcell.NewEventMouse(1, 1, tcell.Button1, tcell.ModNone))
			app.handleMouse(tcell.NewEventMouse(1, 1, tcell.ButtonNone, tcell.ModNone))
			waitWritten(t, p.Session)
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasSuffix(got, []byte("\x1b[<0;2;2m")) {
				t.Fatalf("mouse release not forwarded; child received %q", got)
			}
		})
	}
}

func TestRegressionWideSelectionAndSearch(t *testing.T) {
	for _, tc := range []struct {
		text string
		cols int
	}{
		{"界界AB", 5}, {"abcd界B", 5}, {"界X", 10},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := regressionPane(tc.cols, 3)
			p.CaptureAndWrite([]byte(tc.text))
			p.SelActive = true
			p.SelAnchor = session.SelPos{Row: 0, Col: 0}
			cur := p.Term.Cursor()
			p.SelCursor = session.SelPos{Row: cur.Y, Col: cur.X - 1}
			if got := p.SelText(); got != tc.text {
				t.Fatalf("copied %q, want %q", got, tc.text)
			}
		})
	}
	t.Run("search display columns", func(t *testing.T) {
		p := regressionPane(10, 3)
		p.CaptureAndWrite([]byte("a界界b"))
		app := &App{searchPane: p, searchQuery: "界界b"}
		app.runSearchScan()
		if len(app.searchMatches) != 1 || app.searchMatches[0].Col != 1 || app.searchMatches[0].Length != 5 {
			t.Fatalf("matches = %+v", app.searchMatches)
		}
		selectWord(p, session.SelPos{Row: 0, Col: 2})
		if got := p.SelText(); got != "a界界b" {
			t.Fatalf("word on wide continuation = %q", got)
		}
	})
}

func TestRegressionResizePreservesLinkIdentities(t *testing.T) {
	p := regressionPane(20, 3)
	p.CaptureAndWrite([]byte("\x1b]8;;https://old.example\x07old\x1b]8;;\x07\r\n"))
	p.CaptureAndWrite([]byte("\x1b]8;;https://new.example\x07new\x1b]8;;\x07\r\none\r\ntwo\r\nthree\r\n"))
	p.resize(0, 0, 20, 3)
	if p.Scrollback.Count == 0 {
		t.Fatal("missing rebuilt scrollback")
	}
	for i, want := range []string{"https://old.example", "https://new.example"} {
		if got := p.Term.Link(p.Scrollback.Get(i)[0].Link); got != want {
			t.Fatalf("scrollback link %d = %q, want %q", i, got, want)
		}
	}
}

// TestRegressionLowTruecolorRendersRGB: 38;2;0;0;255 painted palette 255
// (grey) and 48;2;0;0;1 the theme's red, because vt10x stored the RGB value
// as a palette index.
func TestRegressionLowTruecolorRendersRGB(t *testing.T) {
	p := regressionPane(4, 1)
	p.CaptureAndWrite([]byte("\x1b[38;2;0;0;255mB\x1b[0;48;2;0;0;1mK"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(5, 1)
	renderPane(scr, p, testTheme())
	_, style, _ := scr.Get(0, 0)
	if fg, _, _ := style.Decompose(); fg != tcell.NewRGBColor(0, 0, 255) {
		t.Errorf("38;2;0;0;255 painted %v", fg)
	}
	_, style, _ = scr.Get(1, 0)
	if _, bg, _ := style.Decompose(); bg != tcell.NewRGBColor(0, 0, 1) {
		t.Errorf("48;2;0;0;1 painted %v", bg)
	}
}

// TestRegressionLegacyWidthKeepsColumns: with DECRST 2027 a heart with VS16
// takes one column. bunk painted the whole cluster, which tcell measures as
// two columns, so the next cell disappeared on the host.
func TestRegressionLegacyWidthKeepsColumns(t *testing.T) {
	p := regressionPane(4, 1)
	p.CaptureAndWrite([]byte("\x1b[?2027l\u2764\ufe0f|"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(5, 1)
	renderPane(scr, p, testTheme())
	for x, want := range []string{"\u2764", "|"} {
		if got, _, width := scr.Get(x, 0); got != want || width != 1 {
			t.Errorf("column %d = %+q width %d, want %+q width 1", x, got, width, want)
		}
	}
	if got := p.Term.Cell(0, 0).Text(); got != "\u2764\ufe0f" {
		t.Errorf("cell text for copy = %+q, want the heart with VS16", got)
	}
}
