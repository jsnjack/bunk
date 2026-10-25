package session

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"bunk/internal/vt10x"
)

// TestCellPixelsReplies: CSI 14 t and 16 t report the cell size in pixels,
// and follow SetCellPixels.
func TestCellPixelsReplies(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	s := &Session{
		Term:            vt10x.New(vt10x.WithSize(40, 10), vt10x.WithCellPixels(12, 27)),
		PTY:             pw,
		ScrollbackLines: 100,
		Scrollback:      Ring{MaxLines: 100},
	}
	query := []byte("\x1b[14t\x1b[16t\x1b[18t")
	s.Lock()
	s.CaptureAndWrite(query)
	s.Unlock()
	s.SetCellPixels(13, 30) // a pipe has no window size: only the replies change
	s.Lock()
	s.CaptureAndWrite(query)
	s.Unlock()
	closeWriter(t, s, pw)
	got, _ := io.ReadAll(pr)
	want := "\x1b[4;270;480t\x1b[6;27;12t\x1b[8;10;40t" + "\x1b[4;300;520t\x1b[6;30;13t\x1b[8;10;40t"
	if string(got) != want {
		t.Errorf("replies %q, want %q", got, want)
	}
}

// TestWinsizeHelper is the child of TestCellPixelsWinsize: it prints its
// terminal's size, again after every line it reads.
func TestWinsizeHelper(t *testing.T) {
	if os.Getenv("BUNK_WINSIZE_HELPER") == "" {
		t.Skip("child process of TestCellPixelsWinsize")
	}
	in := bufio.NewReader(os.Stdin)
	for {
		ws, err := pty.GetsizeFull(os.Stdin)
		if err != nil {
			fmt.Println("winsize error:", err)
			os.Exit(1)
		}
		fmt.Printf("winsize %dx%d pixels %dx%d\n", ws.Cols, ws.Rows, ws.X, ws.Y)
		if _, err := in.ReadString('\n'); err != nil {
			os.Exit(0)
		}
	}
}

// TestCellPixelsWinsize: a child sees Options.CellPixels in its window
// size, the new size after SetCellPixels, and virtual pixels (8 wide,
// height from CellAspect) when no cell size is given, as bunk's TUI does.
func TestCellPixelsWinsize(t *testing.T) {
	start := func(o Options) *Session {
		t.Helper()
		o.Cols, o.Rows, o.Scrollback = 40, 5, 100
		o.Args = []string{os.Args[0], "-test.run=^TestWinsizeHelper$"}
		o.Env = []string{"BUNK_WINSIZE_HELPER=1"}
		s, err := Start(o)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		t.Cleanup(func() {
			close(done)
			s.Close()
		})
		s.Run(Hooks{}, done)
		return s
	}
	s := start(Options{CellPixels: [2]int{12, 27}})
	waitScreenText(t, s, "winsize 40x5 pixels 480x135")
	s.SetCellPixels(13, 30)
	if err := s.WriteInput([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	waitScreenText(t, s, "winsize 40x5 pixels 520x150")

	v := start(Options{CellAspect: []float64{2.0}})
	waitScreenText(t, v, "winsize 40x5 pixels 320x80")
}

// waitScreenText waits up to 10 s for text on the session's screen.
func waitScreenText(t *testing.T, s *Session, text string) {
	t.Helper()
	var screen string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		s.Lock()
		cols, rows := s.Term.Size()
		var b strings.Builder
		for y := 0; y < rows; y++ {
			for _, g := range CaptureRow(s.Term, y, cols) {
				if g.Char != 0 {
					b.WriteRune(g.Char)
				}
			}
			b.WriteByte('\n')
		}
		s.Unlock()
		if screen = b.String(); strings.Contains(screen, text) {
			return
		}
	}
	t.Fatalf("%q not on the screen:\n%s", text, screen)
}
