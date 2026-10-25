package main

// Leak and soak tests for bunk's TUI: bunk's own App, panes on real PTYs,
// its render loop and death watcher, driven on a simulation screen through
// the splits, zooms, resizes, output and pane exits of a day of use. After
// every cycle the open descriptors, goroutines, child processes and live
// heap must be back at the baseline (internal/leakcheck). SOAK=1 (make
// soak) runs the long version.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"bunk/internal/leakcheck"

	"github.com/gdamore/tcell/v2"
)

// useClipboardWriter replaces the desktop clipboard writer for one test.
func useClipboardWriter(t *testing.T, write func(string) bool) {
	t.Helper()
	waitClipboardIdle(t)
	nativeClipboard.Lock()
	old := writeNativeClipboard
	writeNativeClipboard = write
	nativeClipboard.Unlock()
	t.Cleanup(func() {
		waitClipboardIdle(t)
		nativeClipboard.Lock()
		writeNativeClipboard = old
		nativeClipboard.Unlock()
	})
}

// waitClipboardIdle waits until no clipboard write is queued or running.
func waitClipboardIdle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; {
		nativeClipboard.Lock()
		running := nativeClipboard.running
		nativeClipboard.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the clipboard writer never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

func osc52(text string) []byte {
	return []byte("\x1b]52;c;" + b64(text) + "\x07")
}

// TestOSC52MirrorOneWriterAtATime: a pane flooding OSC 52 runs one
// clipboard writer at a time and the newest text wins, instead of starting
// a goroutine and a clipboard process for every sequence.
func TestOSC52MirrorOneWriterAtATime(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	calls, active, maxActive, last := 0, 0, 0, ""
	useClipboardWriter(t, func(text string) bool {
		mu.Lock()
		calls++
		active++
		maxActive = max(maxActive, active)
		mu.Unlock()
		<-release
		mu.Lock()
		active--
		last = text
		mu.Unlock()
		return true
	})
	base := runtime.NumGoroutine()
	const n = 2000
	for i := range n {
		mirrorOSC52ToNativeClipboard(osc52(fmt.Sprintf("copy %d", i)))
	}
	if extra := runtime.NumGoroutine() - base; extra > 1 {
		t.Errorf("%d goroutines for %d OSC 52 writes, want at most 1", extra, n)
	}
	close(release)
	waitClipboardIdle(t)
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 || calls > 2 {
		t.Errorf("%d clipboard writes, up to %d at once; want at most 2, one at a time", calls, maxActive)
	}
	if want := fmt.Sprintf("copy %d", n-1); last != want {
		t.Errorf("clipboard holds %q, want the newest text %q", last, want)
	}
}

// tuiSoak is a running bunk App on a simulation screen.
type tuiSoak struct {
	t    *testing.T
	app  *App
	sim  tcell.SimulationScreen
	home *Pane // the pane that stays while others come and go
	// holders are the process groups of processes that keep the PTYs of
	// removed panes open.
	holders []int
}

const soakW, soakH = 160, 48

// startTUISoak starts bunk the way run() does, with /bin/sh panes.
func startTUISoak(t *testing.T) *tuiSoak {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("ENV", "")
	t.Setenv("HISTFILE", "/dev/null")
	t.Setenv("PS1", "$ ")
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(soakW, soakH)
	app := &App{
		screen: sim, theme: testTheme(), keys: resolveKeybindings(nil), cellAspect: 2, scrollback: 500,
		redraw: make(chan struct{}, 1), paneDead: make(chan *Pane, 8), done: make(chan struct{}),
		oscBuf: newOSCBuffer(), hostOut: io.Discard, lastCursorStyle: -1,
	}
	p, err := NewPane(app.nextID, 0, 0, soakW, soakH, app.scrollback, "", nil, app.paneOSCColors(),
		app.redraw, app.paneDead, app.done, app.oscBuf, app.cellAspect)
	if err != nil {
		t.Fatal(err)
	}
	app.nextID++
	app.root = newLeaf(p, 0, 0, soakW, soakH)
	app.active = p
	go app.deathWatcher()
	app.renderWg.Add(1)
	go app.renderLoop()
	t.Cleanup(app.shutdown) // after a failure; a no-op once bunk shut down
	return &tuiSoak{t: t, app: app, sim: sim, home: p}
}

// panes returns the panes in the layout.
func (s *tuiSoak) panes() []*Pane {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	var out []*Pane
	if s.app.root != nil {
		for _, leaf := range s.app.root.leaves() {
			out = append(out, leaf.pane)
		}
	}
	return out
}

// waitPanes waits until the layout holds n panes.
func (s *tuiSoak) waitPanes(n int) {
	s.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); len(s.panes()) != n; {
		if time.Now().After(deadline) {
			s.t.Fatalf("%d panes in the layout, want %d", len(s.panes()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// soakOutput is a shell loop that prints lines of colours, wide and
// combining characters, emoji, OSC 8 links and OSC 52 clipboard writes.
func soakOutput(cycle, pane, lines int) string {
	return fmt.Sprintf(`i=0; while [ $i -lt %d ]; do `+
		`printf '\033[1;3%%dm%%05d \342\224\200 \346\227\245\346\234\254 e\314\201 \360\237\221\215\033[0m `+
		`\033]8;;https://example.com/%d/%d/%%d\033\\link\033]8;;\033\\ \033]52;c;%s\007\033[38;2;1;2;%%dm.\033[0m\n' `+
		`$((i %% 8)) $i $i $((i %% 256)); i=$((i+1)); done`, lines, cycle, pane, b64("bunk soak"))
}

// cycle splits the home pane into four, zooms, navigates and resizes,
// then lets the three new panes print and exit. The first leaves a
// process behind that keeps its PTY open: removing the pane must close the
// PTY anyway. That process lives on until killHolders.
func (s *tuiSoak) cycle(i int) {
	s.t.Helper()
	app := s.app
	app.mu.Lock()
	app.active = s.home
	app.mu.Unlock()
	for range 3 {
		app.splitActive(false)
	}
	s.waitPanes(4)
	app.zoomToggle()
	app.render()
	app.zoomToggle()
	for _, d := range []dir{dirLeft, dirUp, dirRight, dirDown} {
		app.navigate(d)
	}
	w, h := soakW-10*(i%3), soakH-6*(i%2)
	s.sim.SetSize(w, h)
	app.handleResize()

	holder := true
	for j, p := range s.panes() {
		if p == s.home {
			continue
		}
		cmd := soakOutput(i, j, 600)
		if holder {
			// Without job control the background sleep stays in the
			// shell's process group, which killHolders kills.
			cmd = "set +m; trap '' HUP; sleep 120 & " + cmd
			s.holders = append(s.holders, p.Cmd.Process.Pid)
			holder = false
		}
		p.WriteInput([]byte(cmd + "; exit\r"))
	}
	s.waitPanes(1)
	s.sim.SetSize(soakW, soakH)
	app.handleResize()
}

// killHolders ends the processes that cycles left holding PTYs.
func (s *tuiSoak) killHolders() {
	for _, pgid := range s.holders {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			s.t.Errorf("kill the process holding a PTY: %v", err)
		}
	}
	s.holders = nil
}

// TestTUISoak runs split/zoom/resize/output/exit cycles (5, or 200 with
// SOAK=1) and checks after each that bunk holds no more descriptors,
// goroutines, children or heap than after the first. Then the last pane
// exits, bunk shuts down, and everything it started must be gone.
func TestTUISoak(t *testing.T) {
	if !leakcheck.Supported() {
		t.Skip("needs /proc")
	}
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	cycles := 5
	if leakcheck.Soak() {
		cycles = 200
	}
	var copies atomic.Int64
	useClipboardWriter(t, func(string) bool { copies.Add(1); return true })
	// Removing a pane must free its PTY descriptor and goroutines at once,
	// so after the warm-up the counts have no slack.
	limits := leakcheck.Limits{Heap: 4 << 20, Settle: 5 * time.Second}
	before := leakcheck.Take()

	s := startTUISoak(t)
	t.Cleanup(s.killHolders)
	s.cycle(0) // warm-up: render caches, buffers and the poller exist now
	s.killHolders()
	base := leakcheck.Check(t, "warm-up", leakcheck.Take(), limits)
	first := base
	start := time.Now()
	for i := 1; i <= cycles; i++ {
		s.cycle(i)
		// The processes left by the cycles still hold their PTYs.
		snap := leakcheck.Check(t, fmt.Sprintf("cycle %d", i), base, limits)
		if t.Failed() {
			t.FailNow()
		}
		if i == 1 {
			first = snap
		} else if snap.Heap > first.Heap+2<<20 {
			t.Errorf("cycle %d: live heap %s, %s after cycle 1: it grows with every cycle",
				i, leakcheck.MiB(snap.Heap), leakcheck.MiB(first.Heap))
		}
		if i == 1 || i == cycles || i%50 == 0 {
			t.Logf("cycle %d: %d fds, %d goroutines, heap %s (baseline %d, %d, %s), %d clipboard writes, %v",
				i, len(snap.FDs), snap.Goroutines, leakcheck.MiB(snap.Heap), len(base.FDs), base.Goroutines,
				leakcheck.MiB(base.Heap), copies.Load(), time.Since(start).Round(time.Millisecond))
		}
	}
	if n := copies.Load(); n == 0 || n > int64(cycles+1)*3*600 {
		t.Errorf("%d clipboard writes for %d OSC 52 sequences", n, (cycles+1)*3*600)
	}

	s.killHolders()

	// The last pane exits: bunk shuts down and stops its goroutines.
	s.home.WriteInput([]byte("exit\r"))
	select {
	case <-s.app.done:
	case <-time.After(10 * time.Second):
		t.Fatal("bunk did not shut down after its last pane exited")
	}
	waitClipboardIdle(t)
	after := leakcheck.Check(t, "after shutdown", before, limits)
	t.Logf("after shutdown: %d fds, %d goroutines, heap %s (before start %d, %d, %s)",
		len(after.FDs), after.Goroutines, leakcheck.MiB(after.Heap), len(before.FDs), before.Goroutines, leakcheck.MiB(before.Heap))
}

// b64 is base64 of s, for OSC 52 payloads.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
