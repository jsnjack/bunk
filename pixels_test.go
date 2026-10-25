package main

import (
	"testing"

	"github.com/creack/pty"
)

// TestTUIVirtualPixels: bunk's TUI keeps virtual pixels, 8 wide and as high
// as the cell aspect gives, in the PTY's window size and the emulator.
// (bunk-gui passes real cell sizes; see session.Options.CellPixels.)
func TestTUIVirtualPixels(t *testing.T) {
	done := make(chan struct{})
	p, err := NewPane(0, 0, 0, 41, 5, 100, "", []string{"/bin/sh", "-c", "sleep 5"}, hostOSCColors{},
		make(chan struct{}, 1), make(chan *Pane, 1), done, newOSCBuffer(), 2.0)
	if err != nil {
		t.Fatal(err)
	}
	defer close(done)
	defer p.Close()
	p.Lock()
	cw, ch := p.Term.CellPixels()
	p.Unlock()
	if cw != 8 || ch != 16 {
		t.Errorf("emulator cell %dx%d px, want the virtual 8x16", cw, ch)
	}
	ws, err := pty.GetsizeFull(p.PTY)
	if err != nil {
		t.Fatal(err)
	}
	// The PTY is one column narrower than the pane (the scrollbar).
	if ws.Cols != 40 || ws.Rows != 5 || ws.X != 40*8 || ws.Y != 5*16 {
		t.Errorf("PTY window size %+v, want 40x5 cells of 8x16 virtual pixels", *ws)
	}
}
