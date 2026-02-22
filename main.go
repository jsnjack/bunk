// bunk – a lightweight terminal multiplexer.
//
// Key bindings:
//
//	F1          Auto-split the focused pane (vertical if wide, horizontal if tall)
//	Alt+Arrow   Move focus to the nearest pane in that direction
//	Ctrl+Q      Quit bunk (all child shells receive SIGHUP)
package main

import (
	"log"
	"math"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// oscChanSize caps how many forwarded OSC sequences can queue between the
// pane reader goroutines and the render loop drain.
const oscChanSize = 64

// ---------------------------------------------------------------------------
// App – top-level application state
// ---------------------------------------------------------------------------

// App holds every piece of global state and mediates between the event loop,
// the render loop, and the layout tree.
type App struct {
	screen tcell.Screen

	// mu guards root, active, and nextID.  All layout mutations must hold it.
	mu     sync.Mutex
	root   *Node // root of the BSP layout tree
	active *Pane // the pane that currently receives keystrokes
	nextID int   // monotonically increasing pane ID

	// redraw is a 1-element channel; any goroutine writes a token to ask for a
	// screen repaint.  The render loop drains it.  The buffer of 1 coalesces
	// rapid bursts into a single repaint.
	redraw chan struct{}

	// paneDead receives a *Pane when its shell process exits.
	paneDead chan *Pane

	// done is closed to signal all background goroutines to stop.
	done     chan struct{}
	shutOnce sync.Once
	renderWg sync.WaitGroup // tracks the render loop goroutine

	// oscCh carries OSC sequences from pane readPTY goroutines to the render
	// loop.  The render loop writes them to os.Stdout before tcell.Show() so
	// the host terminal receives OSC 7/8/52 (CWD, hyperlinks, clipboard).
	oscCh chan []byte

	// prevMouseBtn remembers the last pressed mouse button so mouseToBytes can
	// generate release events (only used in the event loop goroutine).
	prevMouseBtn tcell.ButtonMask

	// Double-click detection (event loop goroutine only – no lock needed).
	lastClickTime  time.Time
	lastClickPos   selPos
	lastClickPane  *Pane
	dblClickActive bool // release should not overwrite word selection

	// Search state.  All fields are protected by mu (read by the render loop,
	// written by the event loop via enterSearch/exitSearch/updateSearch).
	searchMode    bool
	searchQuery   string
	searchPane    *Pane
	searchMatches []searchMatch
	searchIdx     int
}

// shutdown is safe to call multiple times.  It closes every pane (sending
// SIGHUP to each shell), finalises the tcell screen (restoring the host
// terminal), and closes done so background goroutines exit cleanly.
//
// NOTE: post-Fini terminal cleanup (clearing the screen, stty sane, etc.) is
// intentionally done in main() AFTER app.eventLoop() returns, not here.  When
// shutdown is triggered by a pane dying (Ctrl+D / exit), this function runs in
// a background goroutine that is killed the moment main() returns – so any
// cleanup placed after screen.Fini() here would be unreliable.
func (app *App) shutdown() {
	app.shutOnce.Do(func() {
		app.closeAllPanes() // kill shells
		close(app.done)     // signal all goroutines

		// Wait for the render loop to stop before calling Fini(), so we don't
		// race between screen.Show() and screen.Fini().  500 ms timeout guards
		// against a stuck render call.
		waitCh := make(chan struct{})
		go func() { app.renderWg.Wait(); close(waitCh) }()
		select {
		case <-waitCh:
		case <-time.After(500 * time.Millisecond):
		}

		app.screen.DisableMouse() // stop mouse bytes flowing to bash stdin
		app.screen.Fini()         // exit alt-screen, restore cooked mode/termios
		// ↑ Fini() causes PollEvent to return nil → eventLoop returns → main()
		//   runs the post-Fini cleanup synchronously before the process exits.
	})
}

// closeAllPanes sends SIGHUP to every live shell process and closes every
// PTY master.  It is safe to call concurrently.
func (app *App) closeAllPanes() {
	app.mu.Lock()
	var panes []*Pane
	if app.root != nil {
		for _, leaf := range app.root.leaves() {
			panes = append(panes, leaf.pane)
		}
	}
	app.mu.Unlock()
	for _, p := range panes {
		p.close()
	}
}

// triggerRedraw sends a non-blocking redraw signal.
func (app *App) triggerRedraw() {
	select {
	case app.redraw <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	// Log to a file so diagnostics don't corrupt the TUI.
	if lf, err := os.OpenFile("/tmp/bunk.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(lf)
		defer lf.Close()
	}

	screen, err := tcell.NewScreen()
	if err != nil {
		log.Fatalf("tcell.NewScreen: %v", err)
	}
	if err := screen.Init(); err != nil {
		log.Fatalf("screen.Init: %v", err)
	}
	screen.SetStyle(tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite))
	screen.HideCursor()
	screen.Clear()

	app := &App{
		screen:   screen,
		redraw:   make(chan struct{}, 1),
		paneDead: make(chan *Pane, 8),
		done:     make(chan struct{}),
		oscCh:    make(chan []byte, oscChanSize),
	}

	// Enable mouse reporting so clicks and scroll events are delivered.
	// MouseMotionEvents also delivers motion events (used for drag-select in panes).
	screen.EnableMouse(tcell.MouseMotionEvents)

	// Enable bracketed paste: tcell will send EventPaste{start:true} / {false}
	// wrapping the paste content so we can forward \x1b[200~…\x1b[201~ to panes
	// that have opted in via DECSET 2004.
	screen.EnablePaste()

	// Create the initial full-screen pane.
	w, h := screen.Size()
	p, err := NewPane(app.nextID, 0, 0, w, h, nil, app.redraw, app.paneDead, app.done, app.oscCh)
	if err != nil {
		screen.Fini()
		log.Fatalf("NewPane: %v", err)
	}
	app.nextID++
	app.root = newLeaf(p, 0, 0, w, h)
	app.active = p

	go app.deathWatcher() // removes panes whose shells have exited
	app.renderWg.Add(1)
	go app.renderLoop() // paints the screen on demand

	app.eventLoop() // blocks until PollEvent returns nil (i.e. Fini() was called)

	// Terminal cleanup runs HERE, synchronously in the main goroutine, AFTER
	// Fini() has already been called.  This is the only safe place: when the
	// user exits via Ctrl+D / `exit` (last pane dies), shutdown() is invoked
	// from a background goroutine whose remaining code is killed as soon as
	// main() returns.  Fini() causes PollEvent to return nil which unblocks
	// eventLoop, so by the time we reach this line Fini() is guaranteed done.
	const vtreset = "\033[?2004l" + // bracketed-paste off
		"\033[?1004l" + // focus-events off
		"\033[?1003l\033[?1002l\033[?1000l" + // all mouse modes off
		"\033[?1006l" + // SGR mouse extension off
		"\033[?1049l" + // exit alternate screen
		"\033[?25h" + // show cursor
		"\033[0m" + // reset all SGR attributes
		"\033[2J\033[H" // clear screen + cursor home
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		tty.WriteString(vtreset) //nolint:errcheck
		tty.Close()
	} else {
		os.Stdout.WriteString(vtreset) //nolint:errcheck
	}
	exec.Command("stty", "sane").Run() //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Event loop
// ---------------------------------------------------------------------------

// eventLoop processes tcell events until the screen is finalised (PollEvent
// returns nil) or Ctrl+Q is pressed.
func (app *App) eventLoop() {
	for {
		ev := app.screen.PollEvent()
		if ev == nil {
			return // screen.Fini() was called
		}
		switch ev := ev.(type) {
		case *tcell.EventResize:
			app.handleResize()
		case *tcell.EventKey:
			if !app.handleKey(ev) {
				app.shutdown()
				return
			}
		case *tcell.EventMouse:
			app.handleMouse(ev)
		case *tcell.EventPaste:
			app.handlePaste(ev)
		}
	}
}

// handleResize is called when the host terminal changes size.
// It recalculates the BSP tree dimensions and propagates ioctl TIOCSWINSZ to
// every PTY so the shells redraw at the new size.
func (app *App) handleResize() {
	app.screen.Sync()
	w, h := app.screen.Size()
	app.mu.Lock()
	if app.root != nil {
		app.root.resize(0, 0, w, h)
	}
	app.mu.Unlock()
	app.triggerRedraw()
}

// handleKey routes a key event.  Returns false to initiate a clean shutdown.
func (app *App) handleKey(ev *tcell.EventKey) bool {
	// ── Search mode captures all input ────────────────────────────────────
	if app.searchMode {
		return app.handleSearchKey(ev)
	}

	// ── Ctrl+C: copy selection if one exists, otherwise send interrupt ──────
	// Many terminals cannot report Ctrl+Shift+C distinctly from Ctrl+C, so we
	// piggyback on the selection state: if text is selected, Ctrl+C copies it
	// (and clears the selection) without sending ^C to the PTY.  This matches
	// the behaviour of most terminal emulators.
	if ev.Key() == tcell.KeyCtrlC {
		app.mu.Lock()
		active := app.active
		app.mu.Unlock()
		if active != nil {
			active.mu.Lock()
			text := active.selText()
			if text != "" {
				active.selActive = false
			}
			active.mu.Unlock()
			if text != "" {
				app.copyToClipboard(text)
				app.triggerRedraw()
				return true // consumed – don't send ^C to PTY
			}
		}
		// No selection: fall through and forward ^C to the PTY normally.
	}

	// ── App-level shortcuts ────────────────────────────────────────────────
	switch ev.Key() {
	case tcell.KeyF1:
		app.splitActive()
		return true
	case tcell.KeyCtrlQ:
		return false
	case tcell.KeyCtrlF:
		app.enterSearch()
		return true
	case tcell.KeyCtrlV:
		// Ctrl+V: paste from system clipboard into the active pane.
		// The terminal emulator intercepts Ctrl+Shift+V itself (before tcell
		// sees it), so we provide Ctrl+V as the in-app paste shortcut.
		app.pasteFromClipboard()
		return true
	}

	// ── Alt+Arrow: pane navigation ────────────────────────────────────────
	if ev.Modifiers()&tcell.ModAlt != 0 {
		switch ev.Key() {
		case tcell.KeyUp:
			app.navigate(dirUp)
			return true
		case tcell.KeyDown:
			app.navigate(dirDown)
			return true
		case tcell.KeyLeft:
			app.navigate(dirLeft)
			return true
		case tcell.KeyRight:
			app.navigate(dirRight)
			return true
		}
	}

	// ── Shift+PgUp / Shift+PgDn: scrollback ──────────────────────────────
	if ev.Modifiers()&tcell.ModShift != 0 {
		app.mu.Lock()
		active := app.active
		h := 0
		if active != nil {
			h = active.h
		}
		app.mu.Unlock()
		switch ev.Key() {
		case tcell.KeyPgUp:
			if active != nil {
				active.scrollUp(max(1, h/2))
				app.triggerRedraw()
			}
			return true
		case tcell.KeyPgDn:
			if active != nil {
				active.scrollDown(max(1, h/2))
				app.triggerRedraw()
			}
			return true
		}
	}

	// ── Forward everything else to the focused pane's PTY ─────────────────
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active != nil && !active.isDead() {
		// Any regular keystroke: snap back to live view and clear selection.
		needRedraw := false
		if active.inScrollback() {
			active.scrollReset()
			needRedraw = true
		}
		active.mu.Lock()
		if active.selActive {
			active.selActive = false
			needRedraw = true
		}
		active.mu.Unlock()
		if needRedraw {
			app.triggerRedraw()
		}
		if data := keyToBytes(ev); len(data) > 0 {
			active.writeInput(data)
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Pane management actions
// ---------------------------------------------------------------------------

// splitActive splits the currently focused pane.
// Direction is chosen by the pane's visual aspect ratio.
// Terminal cells are roughly 2:1 (height:width in pixels), so we compare
// w against 2*h: only if the pane is visually wider do we split vertically.
func (app *App) splitActive() {
	app.mu.Lock()
	defer app.mu.Unlock()

	if app.active == nil || app.root == nil {
		return
	}
	node := app.root.findPane(app.active)
	if node == nil {
		return
	}

	// Terminal cells are ~2× taller than wide (typically 8×16 px).
	// Multiply w by 1 and h by 2 to compare in approximate pixel dimensions:
	//   visually wider  → w*1 ≥ h*2  (more cols than 2× the rows)  → vertical split
	//   visually taller → w*1 <  h*2                                 → horizontal split
	// This prevents repeated vertical splits on a wide-but-not-huge terminal:
	// e.g. 220×50 cells → 220 >= 100 → v; then 109 >= 100 → v; then 54 >= 100? → h.
	var d splitDir
	if node.w >= node.h*2 {
		d = splitVertical
	} else {
		d = splitHorizontal
	}

	// Pre-calculate the two child regions and reject splits that would create
	// panes too small to be useful (minimum 4 cols × 2 rows each).
	var lw, lh, rx, ry, nw, nh int
	if d == splitVertical {
		half := node.w / 2
		lw, lh = half, node.h
		rx, ry = node.x+half+1, node.y
		nw, nh = node.w-half-1, node.h
	} else {
		half := node.h / 2
		lw, lh = node.w, half
		rx, ry = node.x, node.y+half+1
		nw, nh = node.w, node.h-half-1
	}
	if lw < 4 || lh < 2 || nw < 4 || nh < 2 {
		return // either half would be too small
	}
	nx, ny := rx, ry

	// Inherit the container context of the pane being split, so that the new
	// pane opens inside the same Toolbox/Distrobox/Podman container.
	app.active.mu.Lock()
	cid := app.active.containerID
	ct := app.active.containerType
	app.active.mu.Unlock()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	var spawnArgs []string
	if cid != "" && ct != "" {
		spawnArgs = containerSpawnArgs(cid, ct, shell)
	}

	newPane, err := NewPane(app.nextID, nx, ny, nw, nh, spawnArgs, app.redraw, app.paneDead, app.done, app.oscCh)
	if err != nil {
		log.Printf("splitActive: NewPane: %v", err)
		return
	}
	app.nextID++

	node.split(newPane, d) // mutates the BSP tree in-place
	app.active = newPane
	app.triggerRedraw()
}

// navigate moves focus to the nearest pane in direction d, using the centre
// point of each pane as the reference coordinate.
// Focus events (ESC[I / ESC[O) are sent AFTER app.mu is released so that
// writeInput never executes while we hold the layout lock.
func (app *App) navigate(d dir) {
	app.mu.Lock()

	var prev, best *Pane
	if app.root != nil && app.active != nil {
		leaves := app.root.leaves()
		if len(leaves) >= 2 {
			ax := float64(app.active.x) + float64(app.active.w)/2
			ay := float64(app.active.y) + float64(app.active.h)/2
			bestDist := math.MaxFloat64

			for _, leaf := range leaves {
				p := leaf.pane
				if p == app.active || p.isDead() {
					continue
				}
				cx := float64(p.x) + float64(p.w)/2
				cy := float64(p.y) + float64(p.h)/2

				switch d {
				case dirRight:
					if cx <= ax {
						continue
					}
				case dirLeft:
					if cx >= ax {
						continue
					}
				case dirDown:
					if cy <= ay {
						continue
					}
				case dirUp:
					if cy >= ay {
						continue
					}
				}

				if dist := math.Hypot(cx-ax, cy-ay); dist < bestDist {
					bestDist = dist
					best = p
				}
			}
		}
	}

	if best != nil {
		prev = app.active
		app.active = best
		app.triggerRedraw()
	}
	app.mu.Unlock()

	// Send focus events outside the lock so writeInput never runs while
	// app.mu is held (lock order: app.mu → pane.mu only).
	if best != nil {
		sendFocusOut(prev)
		sendFocusIn(best)
	}
}

// deathWatcher waits for dead-pane notifications and cleans them up.
func (app *App) deathWatcher() {
	for {
		select {
		case p := <-app.paneDead:
			app.removePane(p)
		case <-app.done:
			return
		}
	}
}

// removePane removes a dead pane from the layout and re-focuses if needed.
// Focus-in is sent to the newly active pane AFTER releasing app.mu.
func (app *App) removePane(p *Pane) {
	app.mu.Lock()

	if app.root == nil {
		app.mu.Unlock()
		return
	}

	// Determine the best pane to inherit focus.
	// Priority: BSP sibling first (the pane this one was split with, most
	// "previous"), then nearest by screen distance as a fallback.
	var newActive *Pane
	if app.active == p {
		newActive = bestFocusAfterRemove(app.root, p)
		app.active = newActive
	}

	app.root = removeFromTree(app.root, p)
	shutdown := app.root == nil
	app.mu.Unlock()

	if shutdown {
		go app.shutdown()
		return
	}

	if newActive != nil {
		sendFocusIn(newActive)
	}

	app.triggerRedraw()
}

// bestFocusAfterRemove returns the pane that should receive focus after dying
// is removed from the tree.
//
// Strategy:
//  1. Find the dying pane's BSP sibling subtree.  The sibling is the pane (or
//     subtree) that was created at the same split event – it is the natural
//     "previous" pane from the user's perspective.
//  2. Within the sibling subtree, pick the leaf whose centre is geometrically
//     closest to the dying pane's centre.
//  3. If no live pane is found in the sibling subtree (shouldn't happen in
//     normal use) fall back to the nearest live pane anywhere in the tree.
func bestFocusAfterRemove(root *Node, dying *Pane) *Pane {
	node := root.findPane(dying)
	if node == nil {
		return nil
	}

	// Centre of the pane that is about to be removed.
	cx := float64(dying.x) + float64(dying.w)/2
	cy := float64(dying.y) + float64(dying.h)/2

	// Prefer the sibling subtree.
	if node.parent != nil {
		sibling := node.parent.right
		if node.parent.right == node {
			sibling = node.parent.left
		}
		if best := closestLivePane(sibling, cx, cy, dying); best != nil {
			return best
		}
	}

	// Fallback: nearest live pane anywhere.
	return closestLivePane(root, cx, cy, dying)
}

// closestLivePane returns the live pane in subtree n whose centre is closest
// to (cx, cy), excluding the pane `exclude`.
func closestLivePane(n *Node, cx, cy float64, exclude *Pane) *Pane {
	var best *Pane
	bestDist := math.MaxFloat64
	for _, leaf := range n.leaves() {
		p := leaf.pane
		if p == exclude || p.isDead() {
			continue
		}
		px := float64(p.x) + float64(p.w)/2
		py := float64(p.y) + float64(p.h)/2
		if d := math.Hypot(px-cx, py-cy); d < bestDist {
			bestDist = d
			best = p
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Navigation direction type
// ---------------------------------------------------------------------------

type dir int

const (
	dirUp dir = iota
	dirDown
	dirLeft
	dirRight
)

// ---------------------------------------------------------------------------
// Key → PTY byte translation
// ---------------------------------------------------------------------------

// keyToBytes converts a tcell key event into the raw byte sequence expected by
// the PTY (standard VT100/xterm encoding).
func keyToBytes(ev *tcell.EventKey) []byte {
	mod := ev.Modifiers()

	// Printable rune (including space).
	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if mod&tcell.ModAlt != 0 {
			// ESC-prefix for Meta/Alt combos.
			return append([]byte{'\x1b'}, []byte(string(r))...)
		}
		return []byte(string(r))
	}

	// Control characters.
	// IMPORTANT: tcell's Key constants for Ctrl+A…Z are NOT the ASCII byte
	// values (1–26).  They are defined as `iota + 64`, so:
	//   KeyCtrlA = 65, KeyCtrlB = 66, KeyCtrlC = 67 … KeyCtrlZ = 90.
	// The correct ASCII ctrl-byte for Ctrl+X is (X - 'A' + 1), i.e. we must
	// subtract KeyCtrlA and add 1.  Using byte(k) directly would send 'A', 'B',
	// 'C' … to the PTY instead of \x01, \x02, \x03 – which is why Ctrl+C would
	// not deliver SIGINT to the foreground process.
	k := ev.Key()
	if k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ {
		return []byte{byte(k-tcell.KeyCtrlA) + 1}
	}

	// Named keys.
	switch k {
	case tcell.KeyBackspace:
		return []byte{'\x08'}
	case tcell.KeyBackspace2:
		return []byte{'\x7f'}
	case tcell.KeyEnter:
		return []byte{'\r'}
	case tcell.KeyTab:
		return []byte{'\t'}
	case tcell.KeyEsc:
		return []byte{'\x1b'}

	case tcell.KeyUp:
		if mod&tcell.ModAlt != 0 {
			return nil // handled at app level
		}
		return []byte{'\x1b', '[', 'A'}
	case tcell.KeyDown:
		if mod&tcell.ModAlt != 0 {
			return nil
		}
		return []byte{'\x1b', '[', 'B'}
	case tcell.KeyRight:
		if mod&tcell.ModAlt != 0 {
			return nil
		}
		return []byte{'\x1b', '[', 'C'}
	case tcell.KeyLeft:
		if mod&tcell.ModAlt != 0 {
			return nil
		}
		return []byte{'\x1b', '[', 'D'}
	case tcell.KeyHome:
		return []byte{'\x1b', '[', 'H'}
	case tcell.KeyEnd:
		return []byte{'\x1b', '[', 'F'}
	case tcell.KeyPgUp:
		return []byte{'\x1b', '[', '5', '~'}
	case tcell.KeyPgDn:
		return []byte{'\x1b', '[', '6', '~'}
	case tcell.KeyDelete:
		return []byte{'\x1b', '[', '3', '~'}
	case tcell.KeyInsert:
		return []byte{'\x1b', '[', '2', '~'}
	// F-keys (F1 is intercepted at the app level and never reaches here).
	case tcell.KeyF2:
		return []byte{'\x1b', 'O', 'Q'}
	case tcell.KeyF3:
		return []byte{'\x1b', 'O', 'R'}
	case tcell.KeyF4:
		return []byte{'\x1b', 'O', 'S'}
	case tcell.KeyF5:
		return []byte{'\x1b', '[', '1', '5', '~'}
	case tcell.KeyF6:
		return []byte{'\x1b', '[', '1', '7', '~'}
	case tcell.KeyF7:
		return []byte{'\x1b', '[', '1', '8', '~'}
	case tcell.KeyF8:
		return []byte{'\x1b', '[', '1', '9', '~'}
	case tcell.KeyF9:
		return []byte{'\x1b', '[', '2', '0', '~'}
	case tcell.KeyF10:
		return []byte{'\x1b', '[', '2', '1', '~'}
	case tcell.KeyF11:
		return []byte{'\x1b', '[', '2', '3', '~'}
	case tcell.KeyF12:
		return []byte{'\x1b', '[', '2', '4', '~'}
	}
	return nil
}
