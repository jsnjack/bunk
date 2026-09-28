package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"bunk/internal/vt10x"

	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

func TestResizeRedrawUsesNotifiedSize(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		t.Run(fmt.Sprintf("alternate=%t", alternate), func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := master.Close(); err != nil {
					t.Error(err)
				}
				if err := slave.Close(); err != nil {
					t.Error(err)
				}
			}()
			screen := tcell.NewSimulationScreen("UTF-8")
			if err := screen.Init(); err != nil {
				t.Fatal(err)
			}
			defer screen.Fini()
			p := regressionPane(160, 24)
			p.ptmx = master
			p.resize(0, 0, 161, 24)
			p.captureAndWrite([]byte("existing output\r\n"))
			if alternate {
				p.captureAndWrite([]byte("\x1b[?1049h"))
			}
			app := &App{screen: screen, root: &Node{pane: p}, active: p}
			defer func() {
				app.mu.Lock()
				defer app.mu.Unlock()
				if app.resizeTimer != nil {
					app.resizeTimer.Stop()
				}
			}()
			for _, cols := range []int{80, 40, 100} {
				screen.SetSize(cols+1, 24)
				app.handleResize()
				deadline := time.Now().Add(2 * time.Second)
				for {
					rows, notifiedCols, err := pty.Getsize(slave)
					if err != nil {
						t.Fatal(err)
					}
					if notifiedCols == cols {
						p.mu.Lock()
						actualCols, actualRows := p.term.Size()
						if actualCols != cols || actualRows != rows {
							p.mu.Unlock()
							t.Fatalf("application notified of %dx%d while emulator is %dx%d", cols, rows, actualCols, actualRows)
						}
						// A redraw positions at the new margin and relies on autowrap.
						output := fmt.Sprintf("\x1b[2J\x1b[H%s!", strings.Repeat("x", cols))
						p.captureAndWrite([]byte(output))
						if p.term.Cell(0, 1).Char != '!' {
							p.mu.Unlock()
							t.Fatal("redraw did not wrap at the notified width")
						}
						p.mu.Unlock()
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("PTY did not receive new size")
					}
					time.Sleep(time.Millisecond)
				}
				// Let any deferred replay run before checking the completed redraw.
				time.Sleep(80 * time.Millisecond)
				p.mu.Lock()
				got := p.term.Cell(0, 1).Char
				mode := p.term.Mode()
				p.mu.Unlock()
				if got != '!' {
					t.Fatalf("deferred resize damaged redraw: got %q", got)
				}
				if (mode&vt10x.ModeAltScreen != 0) != alternate {
					t.Fatal("resize changed screen mode")
				}
			}
		})
	}
}
