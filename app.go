// app.go – App struct, event loop, key handling, and pane management.
package main

import (
	"math"
	"os"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// oscChanSize caps how many forwarded OSC sequences can queue between the
// pane reader goroutines and the render loop drain.
const oscChanSize = 64

// App holds every piece of global state and mediates between the event loop,
// the render loop, and the layout tree.
type App struct {
	screen tcell.Screen
	theme  resolvedTheme // active colour theme, set at startup
	keys   Keybindings  // resolved hotkey configuration

	// cellAspect is the pixel height-to-width ratio of a single terminal cell
	// (cellH / cellW).  Typical fonts give ~1.8–2.2.  Used by splitActive to
	// decide whether a pane is wider or taller in actual pixels.
	// Set at startup from TIOCGWINSZ ws_xpixel/ws_ypixel; falls back to 2.0.
	cellAspect float64

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

	// Zoom state.  Protected by mu.
	// When zoomedPane is non-nil, only that pane is drawn (fullscreen).
	zoomedPane *Pane
	zoomGeom   [4]int // saved {x, y, w, h} to restore on unzoom
}

// shutdown is safe to call multiple times.  It closes every pane (sending
// SIGHUP to each shell), finalises the tcell screen (restoring the host
// terminal), and closes done so background goroutines exit cleanly.
//
// NOTE: post-Fini terminal cleanup (clearing the screen, stty sane, etc.) is
// intentionally done in run() AFTER app.eventLoop() returns, not here.
func (app *App) shutdown() {
	app.shutOnce.Do(func() {
		L.Info("shutdown: closing all panes")
		app.closeAllPanes()
		close(app.done)

		waitCh := make(chan struct{})
		go func() { app.renderWg.Wait(); close(waitCh) }()
		select {
		case <-waitCh:
		case <-time.After(500 * time.Millisecond):
		}

		app.screen.DisableMouse()
		app.screen.Fini()
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
// Event loop
// ---------------------------------------------------------------------------

// eventLoop processes tcell events until the screen is finalised (PollEvent
// returns nil) or Ctrl+Q is pressed.
func (app *App) eventLoop() {
	for {
		ev := app.screen.PollEvent()
		if ev == nil {
			L.Debug("eventLoop: PollEvent returned nil, exiting")
			return // screen.Fini() was called
		}
		switch ev := ev.(type) {
		case *tcell.EventResize:
			L.Debug("event: resize")
			app.handleResize()
		case *tcell.EventKey:
			L.Debug("event: key", "key", ev.Name(), "mod", ev.Modifiers(), "rune", ev.Rune())
			if !app.handleKey(ev) {
				L.Info("eventLoop: shutdown requested via key")
				app.shutdown()
				return
			}
		case *tcell.EventMouse:
			x, y := ev.Position()
			L.Debug("event: mouse", "btn", ev.Buttons(), "x", x, "y", y, "mod", ev.Modifiers())
			app.handleMouse(ev)
		case *tcell.EventPaste:
			L.Debug("event: paste", "start", ev.Start())
			app.handlePaste(ev)
		}
	}
}

// handleResize is called when the host terminal changes size.
func (app *App) handleResize() {
	app.screen.Sync()
	w, h := app.screen.Size()
	L.Debug("handleResize", "w", w, "h", h)
	app.mu.Lock()
	if app.root != nil {
		app.root.resize(0, 0, w, h)
	}
	// If zoomed, the tree recalculated the zoomed pane's BSP position —
	// save it as the new restore geometry, then re-apply fullscreen.
	if p := app.zoomedPane; p != nil {
		app.zoomGeom = [4]int{p.x, p.y, p.w, p.h}
		p.resize(0, 0, w, h)
	}
	app.mu.Unlock()
	app.triggerRedraw()
}

// handleKey routes a key event.  Returns false to initiate a clean shutdown.
func (app *App) handleKey(ev *tcell.EventKey) bool {
	if app.searchMode {
		return app.handleSearchKey(ev)
	}

	kb := &app.keys

	// Copy key: copy selection if active, otherwise forward raw bytes to PTY.
	if kb.Copy.Matches(ev) {
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
				active.SetStatus("COPIED", 3*time.Second)
				app.triggerRedraw()
				// Schedule a redraw after the badge expires so it clears
				// even if the terminal is idle.
				go func() {
					time.Sleep(3 * time.Second)
					app.triggerRedraw()
				}()
				return true
			}
		}
		// No selection – fall through so the key is forwarded to the PTY.
	}

	switch {
	case kb.Split.Matches(ev):
		L.Debug("handleKey: split", "key", kb.Split)
		app.zoomOut() // unzoom before splitting
		app.splitActive()
		return true
	case kb.Quit.Matches(ev):
		L.Debug("handleKey: quit", "key", kb.Quit)
		return false
	case kb.Search.Matches(ev):
		L.Debug("handleKey: enter search", "key", kb.Search)
		app.enterSearch()
		return true
	case kb.Zoom.Matches(ev):
		L.Debug("handleKey: zoom toggle", "key", kb.Zoom)
		app.zoomToggle()
		return true
	case kb.Paste.Matches(ev):
		L.Debug("handleKey: paste", "key", kb.Paste)
		app.pasteFromClipboard()
		return true
	case kb.NavUp.Matches(ev):
		L.Debug("handleKey: navigate up")
		app.zoomOut()
		app.navigate(dirUp)
		return true
	case kb.NavDown.Matches(ev):
		L.Debug("handleKey: navigate down")
		app.zoomOut()
		app.navigate(dirDown)
		return true
	case kb.NavLeft.Matches(ev):
		L.Debug("handleKey: navigate left")
		app.zoomOut()
		app.navigate(dirLeft)
		return true
	case kb.NavRight.Matches(ev):
		L.Debug("handleKey: navigate right")
		app.zoomOut()
		app.navigate(dirRight)
		return true
	}

	// Scrollback keys.
	app.mu.Lock()
	active := app.active
	h := 0
	if active != nil {
		h = active.h
	}
	app.mu.Unlock()
	switch {
	case kb.ScrollUp.Matches(ev):
		if active != nil {
			L.Debug("handleKey: scrollback up", "pane", active.id, "amount", max(1, h/2))
			active.scrollUp(max(1, h/2))
			app.triggerRedraw()
		}
		return true
	case kb.ScrollDown.Matches(ev):
		if active != nil {
			L.Debug("handleKey: scrollback down", "pane", active.id, "amount", max(1, h/2))
			active.scrollDown(max(1, h/2))
			app.triggerRedraw()
		}
		return true
	}

	// Forward everything else to the focused pane's PTY.
	app.mu.Lock()
	active = app.active
	app.mu.Unlock()
	if active != nil && !active.isDead() {
		needRedraw := false
		if active.inScrollback() {
			L.Debug("handleKey: leaving scrollback on keypress", "pane", active.id)
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
			L.Debug("handleKey: forwarding to PTY", "pane", active.id, "bytes", len(data))
			active.writeInput(data)
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Pane management
// ---------------------------------------------------------------------------

func (app *App) splitActive() {
	app.mu.Lock()
	defer app.mu.Unlock()

	if app.active == nil || app.root == nil {
		L.Debug("splitActive: no active pane or root, skipping")
		return
	}
	node := app.root.findPane(app.active)
	if node == nil {
		L.Debug("splitActive: active pane not found in tree")
		return
	}

	var d splitDir
	// Compare pane dimensions in pixels using the measured cell aspect ratio
	// so the split direction matches what the user actually sees.
	// cellAspect = cellPixelH / cellPixelW; pane is wider-in-pixels when
	// node.w * cellW > node.h * cellH  →  node.w > node.h * cellAspect.
	pixelW := float64(node.w)
	pixelH := float64(node.h) * app.cellAspect
	if pixelW >= pixelH+5 { // if almost square, prefer horizontal split to avoid very narrow panes
		d = splitVertical
	} else {
		d = splitHorizontal
	}
	L.Debug("splitActive: direction decision",
		"pane", app.active.id,
		"node_cols", node.w, "node_rows", node.h,
		"cell_aspect", app.cellAspect,
		"pixel_w", int(pixelW), "pixel_h", int(pixelH),
		"wider_in_pixels", pixelW >= pixelH,
		"dir", map[splitDir]string{splitVertical: "vertical (side-by-side)", splitHorizontal: "horizontal (top-bottom)"}[d],
	)

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
		L.Debug("splitActive: panes too small to split", "lw", lw, "lh", lh, "nw", nw, "nh", nh)
		return
	}
	nx, ny := rx, ry

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
		L.Error("splitActive: NewPane", "err", err)
		return
	}
	L.Debug("splitActive: new pane created", "new_pane", newPane.id, "x", nx, "y", ny, "w", nw, "h", nh)
	app.nextID++

	node.split(newPane, d)
	app.active = newPane
	app.triggerRedraw()
}

// ---------------------------------------------------------------------------
// Zoom (fullscreen toggle)
// ---------------------------------------------------------------------------

// zoomToggle toggles fullscreen zoom on the active pane.
func (app *App) zoomToggle() {
	app.mu.Lock()
	if app.zoomedPane != nil {
		app.mu.Unlock()
		app.zoomOut()
	} else {
		app.mu.Unlock()
		app.zoomIn()
	}
}

// zoomIn maximises the active pane to fill the entire screen.
// Must NOT hold app.mu on entry (acquires it internally).
func (app *App) zoomIn() {
	app.mu.Lock()
	defer app.mu.Unlock()

	p := app.active
	if p == nil || app.root == nil {
		return
	}
	// Only one pane → nothing to zoom.
	if app.root.isLeaf() {
		return
	}

	app.zoomedPane = p
	app.zoomGeom = [4]int{p.x, p.y, p.w, p.h}

	w, h := app.screen.Size()
	L.Debug("zoomIn", "pane", p.id, "screen", w, "x", h)
	p.resize(0, 0, w, h)
	app.triggerRedraw()
}

// zoomOut restores the zoomed pane to its original geometry.
// Safe to call when not zoomed (no-op).
// Must NOT hold app.mu on entry (acquires it internally).
func (app *App) zoomOut() {
	app.mu.Lock()
	defer app.mu.Unlock()

	p := app.zoomedPane
	if p == nil {
		return
	}
	app.zoomedPane = nil

	g := app.zoomGeom
	L.Debug("zoomOut", "pane", p.id, "restore", g)
	p.resize(g[0], g[1], g[2], g[3])
	app.triggerRedraw()
}

// navigate moves focus to the nearest pane in direction d.
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
		L.Debug("navigate: focus change", "dir", d, "from_pane", prev.id, "to_pane", best.id)
		app.active = best
		app.triggerRedraw()
	} else {
		L.Debug("navigate: no target found", "dir", d)
	}
	app.mu.Unlock()

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
func (app *App) removePane(p *Pane) {
	L.Debug("removePane: removing pane", "pane", p.id)
	app.mu.Lock()

	if app.root == nil {
		app.mu.Unlock()
		return
	}

	// If the zoomed pane died, unzoom (no geometry restore needed — it's gone).
	if app.zoomedPane == p {
		app.zoomedPane = nil
	}

	var newActive *Pane
	if app.active == p {
		newActive = bestFocusAfterRemove(app.root, p)
		L.Debug("removePane: refocusing", "from_pane", p.id, "to_pane", func() int {
			if newActive != nil {
				return newActive.id
			}
			return -1
		}())
		app.active = newActive
	}

	app.root = removeFromTree(app.root, p)
	shutdown := app.root == nil
	app.mu.Unlock()

	if shutdown {
		L.Info("removePane: last pane removed, shutting down")
		go app.shutdown()
		return
	}

	if newActive != nil {
		sendFocusIn(newActive)
	}

	app.triggerRedraw()
}

func bestFocusAfterRemove(root *Node, dying *Pane) *Pane {
	node := root.findPane(dying)
	if node == nil {
		return nil
	}
	cx := float64(dying.x) + float64(dying.w)/2
	cy := float64(dying.y) + float64(dying.h)/2

	if node.parent != nil {
		sibling := node.parent.right
		if node.parent.right == node {
			sibling = node.parent.left
		}
		if best := closestLivePane(sibling, cx, cy, dying); best != nil {
			return best
		}
	}
	return closestLivePane(root, cx, cy, dying)
}

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

func keyToBytes(ev *tcell.EventKey) []byte {
	mod := ev.Modifiers()

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if mod&tcell.ModAlt != 0 {
			return append([]byte{'\x1b'}, []byte(string(r))...)
		}
		return []byte(string(r))
	}

	k := ev.Key()
	if k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ {
		return []byte{byte(k-tcell.KeyCtrlA) + 1}
	}

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
			return nil
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
