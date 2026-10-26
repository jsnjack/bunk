// app.go - App struct, event loop, key handling, and pane management.
package main

import (
	"bunk/internal/alert"
	"bunk/internal/badges"
	"bunk/internal/desktop"
	"bunk/internal/notify"
	"bunk/internal/session"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// App holds every piece of global state and mediates between the event loop,
// the render loop, and the layout tree.
type App struct {
	screen tcell.Screen
	theme  resolvedTheme // active colour theme, set at startup
	keys   Keybindings   // resolved hotkey configuration

	scrollback int // max scrollback lines per pane (from config)

	// cellAspect is the pixel height-to-width ratio of a single terminal cell
	// (cellH / cellW).  Typical fonts give ~1.8-2.2.  Used by splitActive to
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

	// oscBuf accumulates OSC sequences from pane readPTY goroutines (and the
	// clipboard helper) for delivery to the host terminal. The render loop
	// flushes it to os.Stdout before tcell.Show() so the host terminal
	// receives OSC 7/8/52/133 (CWD, hyperlinks, clipboard, prompt markers).
	oscBuf *oscBuffer

	// hostOut receives what bunk writes to the host terminal outside tcell
	// (see host). nil means os.Stdout; tests capture it.
	hostOut io.Writer

	// lastEmittedTitle is the most recent title we wrote to the host terminal
	// via OSC 0.  Compared each frame to avoid redundant writes.
	lastEmittedTitle string

	// lastCursorStyle is the DECSCUSR value (0-6) last written to the host
	// terminal.  Compared each frame to avoid redundant writes.
	lastCursorStyle int
	lastCursorColor tcell.Color

	// hostOSCColors caches the outer terminal's probed default fg/bg/cursor
	// colours for theme="terminal" mode so new panes can answer OSC 10/11/12
	// queries consistently without re-probing after tcell owns stdin.
	hostOSCColors hostOSCColors

	// prevMouseBtn remembers the last pressed mouse button so mouseToBytes can
	// generate release events (only used in the event loop goroutine).
	prevMouseBtn tcell.ButtonMask

	// Double-click detection (event loop goroutine only - no lock needed).
	lastClickTime  time.Time
	lastClickPos   session.SelPos
	lastClickPane  *Pane
	dblClickActive bool // release should not overwrite word selection

	// A paste from the host terminal being collected into one input event
	// (handlePaste): its bytes so far (nil when none is open), its pane, the
	// end marker to add, the timer that ends it without an end marker, and
	// its number. Protected by mu.
	paste      []byte
	pastePane  *Pane
	pasteEnd   string
	pasteTimer *time.Timer
	pasteGen   uint64

	// dragPane is the pane where the current Button1 drag started.
	// All drag-select and release events are clamped to this pane so that
	// moving the cursor outside the originating pane doesn't extend the
	// selection into a different pane.  Cleared on Button1 release.
	dragPane *Pane

	// dragEdgeStop stops the edge-auto-scroll goroutine when a drag moves
	// away from the edge or the button is released.  Nil when no goroutine
	// is running.  Access only from the event-loop goroutine.
	dragEdgeStop chan struct{}

	// sbDragPane is non-nil while the user is dragging the scrollbar.
	// sbDragLastY is the Y cell from the previous drag event (for relative deltas).
	// sbDragAccum is the accumulated fractional sbOff so sub-line deltas don't vanish.
	// All three are accessed only from the event-loop goroutine.
	sbDragPane  *Pane
	sbDragLastY int
	sbDragAccum float64

	// Search state.  All fields are protected by mu (read by the render loop,
	// written by the event loop via enterSearch/exitSearch/updateSearch).
	searchMode    bool
	searchQuery   string
	searchPane    *Pane
	searchMatches []session.Match
	searchIdx     int
	searchGen     int // incremented on every search state change; guards async goroutines
	searchWake    chan struct{}
	searchOnce    sync.Once

	// Zoom state.  Protected by mu.
	// When zoomedPane is non-nil, only that pane is drawn (fullscreen).
	zoomedPane *Pane
	zoomGeom   [4]int // saved {x, y, w, h} to restore on unzoom

	// Resize state is protected by mu. Emulator reflow and PTY notification
	// are coalesced together so redraws use the notified dimensions.
	resizeTimer *time.Timer
	resizeGen   uint64

	// notify receives agent hook reports ("bunk notify") and routes them to
	// the panes' sessions. nil when the socket could not be created.
	notify *notify.Server

	// relay reports the most urgent pane to the host terminal when that is
	// a bunk front end or the far end of ssh; nil otherwise. Used under mu.
	relay *agentRelay

	// timing sets the intervals of new panes' session timers. bunk leaves
	// it zero, the defaults; tests shorten it.
	timing session.Timing

	// alerts decides when a pane's agent state reaches the host terminal
	// as a notification (alerts.go). hostBlurred is set while the host
	// terminal reports that it lost the focus, and alertWake is when a
	// held alert is due. Protected by mu.
	alerts      alert.Tracker
	hostBlurred bool
	alertWake   time.Time
	// desktop shows the alerts as desktop notifications through D-Bus
	// when bunk runs on the user's desktop; nil sends them to the host
	// terminal (alerts.go).
	desktop *desktop.Notifier
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
		// Reset cursor style to default so the host terminal isn't left
		// with a non-default shape after bunk exits.
		app.screen.SetCursorStyle(tcell.CursorStyleDefault, tcell.ColorReset)
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
		p.Close()
	}
}

// triggerRedraw sends a non-blocking redraw signal.
func (app *App) triggerRedraw() {
	select {
	case app.redraw <- struct{}{}:
	default:
	}
}

// reinitHost recovers from host-terminal corruption (typically caused by a
// pane that printed binary data containing escape sequences) by emitting a
// targeted reset and forcing tcell to repaint every cell.
//
// We deliberately do NOT call screen.Suspend()/Resume() here.  Suspend()
// goes through tcell's disengage() which calls cells.Resize(0,0) — wiping
// the cell buffer.  After Resume() the buffer is empty and Sync() paints
// nothing, leaving a blank screen until the shell happens to redraw on the
// next keystroke.  We hit exactly that bug while developing this command.
//
// We also do NOT emit RIS (ESC c).  RIS exits alt-screen mode, but tcell
// has no way to know it needs to re-enter — its internal state still says
// "alt-screen on", so subsequent paints would land on the user's primary
// scrollback.
//
// What we DO emit, written directly to stdout under app.mu:
//
//	\x1b[m       SGR reset — clears bold/colours/etc. left over from the
//	              binary stream.
//	\x1b(B       Set G0 charset to ASCII — fixes line-drawing-mode glyph
//	              lockup, the most common after-cat-binary symptom.
//	\x1b]8;;\x1b\\  Close any active OSC 8 hyperlink.
//
// Then we invalidate bunk's per-frame emit caches (so title and cursor
// style get re-sent) and call screen.Sync().  Sync()'s clearScreen() emits
// its own AttrOff + exitUrl + default fg/bg + clear before invalidating
// every cell and repainting from tcell's still-intact cell buffer.
//
// Net effect: the host terminal is reset enough to render correctly, tcell
// re-emits all visual state, and the user's panes redraw immediately
// without a keystroke.
func (app *App) reinitHost() {
	app.mu.Lock()
	defer app.mu.Unlock()

	if _, err := app.host().Write([]byte("\x1b[m\x1b(B\x1b]8;;\x1b\\")); err != nil {
		L.Warn("reinitHost: reset write failed", "err", err)
	}

	// Invalidate emit caches so the next frame re-sends title and cursor
	// style — the host may have lost them, and the cache would otherwise
	// suppress the re-emit.
	app.lastEmittedTitle = ""
	app.lastCursorStyle = -1

	app.screen.Sync()
	L.Info("reinitHost: host terminal re-initialised")
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
		case *tcell.EventFocus:
			app.handleFocus(ev.Focused)
		}
	}
}

// handleResize coalesces emulator reflow and PTY notification. Sending
// SIGWINCH before reflow lets applications redraw into the old-sized grid.
func (app *App) handleResize() {
	app.screen.Sync()
	w, h := app.screen.Size()
	L.Debug("handleResize", "w", w, "h", h)

	app.mu.Lock()
	defer app.mu.Unlock()
	if app.resizeTimer != nil {
		app.resizeTimer.Stop()
	}
	app.resizeGen++
	gen := app.resizeGen
	app.resizeTimer = time.AfterFunc(50*time.Millisecond, func() {
		app.mu.Lock()
		defer app.mu.Unlock()
		// Stop cannot cancel a callback that is already waiting for mu.
		if gen != app.resizeGen {
			return
		}
		L.Debug("handleResize: deferred reflow", "w", w, "h", h)
		if app.root != nil {
			app.root.resize(0, 0, w, h)
		}
		if p := app.zoomedPane; p != nil {
			app.zoomGeom = [4]int{p.x, p.y, p.w, p.h}
			p.resize(0, 0, w, h)
		}
		app.triggerRedraw()
	})
}

// handleKey routes a key event.  Returns false to initiate a clean shutdown.
func (app *App) handleKey(ev *tcell.EventKey) bool {
	app.mu.Lock()
	pane := app.active
	passthrough := false
	if pane != nil {
		pane.Lock()
		if app.keys.Passthrough.Matches(ev) {
			pane.passthrough = !pane.passthrough
		}
		passthrough = pane.passthrough
		pane.Unlock()
	}
	app.mu.Unlock()
	if app.keys.Passthrough.Matches(ev) {
		if app.searchMode {
			app.exitSearch()
		}
		L.Debug("handleKey: passthrough", "enabled", passthrough)
		app.triggerRedraw()
		return true
	}
	if passthrough {
		app.forwardKey(ev)
		return true
	}
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
			active.Lock()
			text := active.SelText()
			if text != "" {
				active.SelActive = false
			}
			active.Unlock()
			if text != "" {
				app.copyToClipboard(text)
				active.SetStatus(badges.Copied, badges.FlashFor)
				app.triggerRedraw()
				// Schedule a redraw after the badge expires so it clears
				// even if the terminal is idle.
				go func() {
					time.Sleep(badges.FlashFor)
					app.triggerRedraw()
				}()
				return true
			}
		}
		// No selection - fall through so the key is forwarded to the PTY.
	}

	switch {
	case kb.Split.Matches(ev):
		L.Debug("handleKey: split", "key", kb.Split)
		app.zoomOut() // unzoom before splitting
		app.splitActive(false)
		return true
	case kb.SplitContext.Matches(ev):
		L.Debug("handleKey: split into context", "key", kb.SplitContext)
		app.zoomOut()
		app.splitActive(true)
		return true
	case kb.Quit.Matches(ev):
		L.Debug("handleKey: quit", "key", kb.Quit)
		return false
	case kb.ReinitHost.Matches(ev):
		L.Debug("handleKey: re-initialise host terminal", "key", kb.ReinitHost)
		app.reinitHost()
		return true
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
		if app.pasteFromClipboard() {
			return true
		}
		// Empty clipboard — fall through so the key reaches the child app.
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
	case kb.NextNeedsYou.Matches(ev):
		L.Debug("handleKey: next pane needing you", "key", kb.NextNeedsYou)
		app.jumpNeedingYou()
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
			L.Debug("handleKey: scrollback up", "pane", active.ID, "amount", max(1, h/2))
			active.ScrollUp(max(1, h/2))
			app.triggerRedraw()
		}
		return true
	case kb.ScrollDown.Matches(ev):
		if active != nil {
			L.Debug("handleKey: scrollback down", "pane", active.ID, "amount", max(1, h/2))
			active.ScrollDown(max(1, h/2))
			app.triggerRedraw()
		}
		return true
	}

	app.forwardKey(ev)
	return true
}

func (app *App) forwardKey(ev *tcell.EventKey) {
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active != nil && !active.IsDead() {
		// Typing into the pane answers its agent's request.
		needRedraw := acknowledge(active)
		if active.InScrollback() {
			L.Debug("handleKey: leaving scrollback on keypress", "pane", active.ID)
			active.ScrollReset()
			needRedraw = true
		}
		active.Lock()
		if active.SelActive {
			active.SelActive = false
			needRedraw = true
		}
		active.Unlock()
		if needRedraw {
			app.triggerRedraw()
		}
		kittyFlags := 0
		active.Lock()
		if len(active.KittyStack) > 0 {
			kittyFlags = active.KittyStack[len(active.KittyStack)-1]
		}
		mode := active.Term.Mode()
		active.Unlock()
		if data := keyToBytesMode(ev, kittyFlags, mode); len(data) > 0 && !app.collectPaste(active, data) {
			L.Debug("handleKey: forwarding to PTY", "pane", active.ID, "bytes", len(data))
			app.sendInput(active, data)
		}
	}
}

// inputRefused is the flash message of a pane whose program stopped
// reading its input while the input queue filled up.
const inputRefused = "input not sent"

// sendInput queues one input event (a key, a paste, a mouse report) for
// p's program. When the program has stopped reading and its input queue
// is full, the event is dropped and the pane flashes inputRefused. It must
// not be called with p's session locked.
func (app *App) sendInput(p *Pane, data []byte) {
	err := p.WriteInput(data)
	if err == nil {
		return
	}
	L.Debug("input refused", "pane", p.ID, "bytes", len(data), "err", err)
	const flash = 2 * time.Second
	p.SetStatus(inputRefused, flash)
	app.triggerRedraw()
	time.AfterFunc(flash, app.triggerRedraw)
}

// ---------------------------------------------------------------------------
// Pane management
// ---------------------------------------------------------------------------

func (app *App) splitActive(inheritContext bool) {
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
		"pane", app.active.ID,
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

	// Inherit the working directory from the active pane's foreground process.
	dir := app.active.Cwd()

	app.active.Lock()
	cid := app.active.ContainerID
	ct := app.active.ContainerType
	fgProc := app.active.FgProcess
	shellPid := 0
	if app.active.Cmd.Process != nil {
		shellPid = app.active.Cmd.Process.Pid
	}
	app.active.Unlock()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	var spawnArgs []string
	if inheritContext {
		L.Debug("splitActive: context inherit requested",
			"pane", app.active.ID, "fgProc", fgProc, "shellPid", shellPid,
			"containerType", ct, "containerID", cid, "dir", dir)
		switch {
		case (ct == "lxc" || ct == "incus") && cid != "" && shellPid > 0:
			// `lxc exec` is the foreground process — reuse its exact cmdline
			// so that `lxc exec xx -- su --login jsn` is preserved verbatim.
			if args := session.FgProcessCmdline(shellPid); len(args) > 0 {
				spawnArgs = args
				L.Debug("splitActive: cloning lxc/incus exec cmdline", "spawnArgs", spawnArgs)
			} else if session.IsRunningContainer(cid, ct) {
				spawnArgs = session.ContainerSpawnArgs(cid, ct, shell)
				L.Debug("splitActive: lxc/incus fallback to containerSpawnArgs", "spawnArgs", spawnArgs)
			}
		case ct == "lxd" && shellPid > 0:
			// "lxd" means we're inside an LXC container as seen from the host.
			// Walk the process tree up from the foreground to find the
			// `lxc exec` ancestor and reuse its full cmdline — this preserves
			// any `su --login <user>` or other entry command.
			fgPid := session.TermFgPGID(shellPid)
			if fgPid <= 0 {
				fgPid = shellPid
			}
			if args := session.FindLXCAncestorCmdline(fgPid); len(args) > 0 {
				spawnArgs = args
				L.Debug("splitActive: cloning lxc exec ancestor", "spawnArgs", spawnArgs)
			} else {
				L.Debug("splitActive: lxd container but no lxc ancestor found (bunk is inside container), using host shell")
			}
		case cid != "" && ct != "" && session.IsRunningContainer(cid, ct):
			spawnArgs = session.ContainerSpawnArgs(cid, ct, shell)
			L.Debug("splitActive: using container context", "spawnArgs", spawnArgs)
		case (ct == "podman" || ct == "docker") && shellPid > 0:
			// "podman run <image>" — cid is an image name, not a running
			// container ID.  Try two strategies:
			// 1. Walk child process tree looking for libpod cgroup entry.
			// 2. Ask podman/docker to list containers with the ancestor image.
			fgPid := session.TermFgPGID(shellPid)
			if fgPid <= 0 {
				fgPid = shellPid
			}
			containerID := session.FindRunningContainerFromPID(fgPid)
			L.Debug("splitActive: process-tree walk result", "containerID", containerID, "fgPid", fgPid)
			if containerID == "" && cid != "" {
				containerID = session.FindContainerByAncestor(cid, ct)
				L.Debug("splitActive: ancestor filter result", "containerID", containerID, "image", cid)
			}
			if containerID != "" {
				spawnArgs = []string{ct, "exec", "-it", containerID, shell}
				L.Debug("splitActive: resolved podman/docker run container", "containerID", containerID, "spawnArgs", spawnArgs)
			} else {
				L.Debug("splitActive: podman/docker run but could not resolve container ID", "fgPid", fgPid, "image", cid)
			}
		case (fgProc == "ssh" || fgProc == "mosh") && shellPid > 0:
			if args := session.FgProcessCmdline(shellPid); len(args) > 0 {
				spawnArgs = args
				L.Debug("splitActive: cloning ssh/mosh session", "spawnArgs", spawnArgs)
			} else {
				L.Debug("splitActive: ssh/mosh detected but cmdline empty", "shellPid", shellPid)
			}
		case fgProc == "sudo" && shellPid > 0:
			if args := session.FgProcessCmdline(shellPid); len(args) > 0 {
				spawnArgs = args
				L.Debug("splitActive: cloning sudo session", "spawnArgs", spawnArgs)
			} else {
				L.Debug("splitActive: sudo detected but cmdline empty", "shellPid", shellPid)
			}
		default:
			L.Debug("splitActive: no context to inherit, using host shell")
		}
	}

	newPane, err := app.newPane(nx, ny, nw, nh, dir, spawnArgs)
	if err != nil {
		L.Error("splitActive: NewPane", "err", err, "dir", dir, "spawnArgs", spawnArgs, "container", ct, "containerID", cid)
		return
	}
	L.Debug("splitActive: new pane created", "new_pane", newPane.ID, "x", nx, "y", ny, "w", nw, "h", nh)

	node.split(newPane, d)
	app.active = newPane
	app.triggerRedraw()
}

// newPane starts pane app.nextID at the given place, with the app's
// scrollback, colours and notify socket, and advances nextID. app.mu must
// be held.
func (app *App) newPane(x, y, w, h int, dir string, args []string) (*Pane, error) {
	p, err := startPane(paneSpec{
		id: app.nextID, x: x, y: y, w: w, h: h, scrollback: app.scrollback, dir: dir, args: args,
		colors: app.paneOSCColors(), redraw: app.redraw, paneDead: app.paneDead, done: app.done,
		oscBuf: app.oscBuf, cellAspect: []float64{app.cellAspect}, notify: app.notify, timing: app.timing,
	})
	if err != nil {
		return nil, fmt.Errorf("pane %d: %w", app.nextID, err)
	}
	app.nextID++
	return p, nil
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
	L.Debug("zoomIn", "pane", p.ID, "screen", w, "x", h)
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
	L.Debug("zoomOut", "pane", p.ID, "restore", g)
	p.resize(g[0], g[1], g[2], g[3])

	// While zoomed, the zoomed pane's frame filled the entire tcell cell
	// buffer, so every other pane's screen region now holds stale zoomed
	// content.  Those panes have no vt10x dirty rows (nothing wrote to
	// them), so renderPane would skip them — force one full repaint.
	if app.root != nil {
		for _, leaf := range app.root.leaves() {
			leaf.pane.markFullRepaint()
		}
	}
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
				if p == app.active || p.IsDead() {
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
		L.Debug("navigate: focus change", "dir", d, "from_pane", prev.ID, "to_pane", best.ID)
		app.active = best
		app.triggerRedraw()
	} else {
		L.Debug("navigate: no target found", "dir", d)
	}
	app.mu.Unlock()

	if best != nil {
		sendFocusOut(prev)
		app.focusIn(best)
	}
}

// focusIn tells p it gained focus (DECSET 1004) and acknowledges its
// agent's request, as focusing a tab does in bunk-gui. Call it after
// making p the active pane, without app.mu.
func (app *App) focusIn(p *Pane) {
	sendFocusIn(p)
	if acknowledge(p) {
		app.triggerRedraw()
	}
}

// acknowledge records that the user has seen p when its agent needs the
// user or is done, and reports whether it did. Other states stay, so an
// agent's working or failed report survives a focus change. It takes the
// session lock; callers must not hold it.
func acknowledge(p *Pane) bool {
	if p == nil {
		return false
	}
	switch p.Status().State {
	case session.AgentNeedsYou, session.AgentDone:
		p.Acknowledge()
		L.Debug("acknowledge: agent request seen", "pane", p.ID)
		return true
	}
	return false
}

// noAgentNeedsYou is the flash message of next_needs_you without a target.
const noAgentNeedsYou = "no agent needs you"

// jumpNeedingYou focuses the next pane after the active one, in layout
// order, whose agent needs the user, else the next one whose agent is done
// (next_needs_you), as bunk-gui does with tabs. The active pane comes last,
// so a request that arrived while it had focus is found too. Focusing the
// pane acknowledges it. Without such a pane it flashes a message.
func (app *App) jumpNeedingYou() {
	app.mu.Lock()
	var panes []*Pane
	if app.root != nil {
		for _, leaf := range app.root.leaves() {
			panes = append(panes, leaf.pane)
		}
	}
	active := app.active
	app.mu.Unlock()

	start := len(panes) - 1 // no active pane: begin with the first
	for i, p := range panes {
		if p == active {
			start = i
		}
	}
	var target *Pane
	for _, want := range []session.AgentState{session.AgentNeedsYou, session.AgentDone} {
		for k := 1; k <= len(panes) && target == nil; k++ {
			p := panes[(start+k)%len(panes)]
			if !p.IsDead() && p.Status().State == want {
				target = p
			}
		}
		if target != nil {
			break
		}
	}
	if target == nil {
		L.Debug("jumpNeedingYou: no agent needs you")
		if active != nil {
			const flash = 2 * time.Second
			active.SetStatus(noAgentNeedsYou, flash)
			app.triggerRedraw()
			time.AfterFunc(flash, app.triggerRedraw)
		}
		return
	}
	L.Debug("jumpNeedingYou: focus", "pane", target.ID)
	app.focusPane(target)
}

// focusPane makes p the active pane, leaving zoom when another pane fills
// the screen, and acknowledges p.
func (app *App) focusPane(p *Pane) {
	app.mu.Lock()
	zoomedOther := app.zoomedPane != nil && app.zoomedPane != p
	app.mu.Unlock()
	if zoomedOther {
		app.zoomOut()
	}
	app.mu.Lock()
	if app.root == nil || app.root.findPane(p) == nil {
		app.mu.Unlock()
		return // removed meanwhile
	}
	prev := app.active
	app.active = p
	app.mu.Unlock()
	if prev != p {
		sendFocusOut(prev)
	}
	app.focusIn(p)
	app.triggerRedraw()
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
	L.Debug("removePane: removing pane", "pane", p.ID)
	app.mu.Lock()

	if app.root == nil {
		app.mu.Unlock()
		return
	}

	// If the zoomed pane died, unzoom (no geometry restore needed — it's gone).
	wasZoomed := app.zoomedPane == p
	if wasZoomed {
		app.zoomedPane = nil
	}

	var newActive *Pane
	if app.active == p {
		newActive = bestFocusAfterRemove(app.root, p)
		L.Debug("removePane: refocusing", "from_pane", p.ID, "to_pane", func() int {
			if newActive != nil {
				return newActive.ID
			}
			return -1
		}())
		app.active = newActive
	}

	app.root = removeFromTree(app.root, p)
	if app.alerts.Remove(p.ID) == alert.Withdraw {
		app.withdrawAlert(p.ID)
	}
	if app.notify != nil && p.token != "" {
		app.notify.Unregister(p.token)
	}
	shutdown := app.root == nil
	if wasZoomed && !shutdown {
		// Same staleness as zoomOut: the dead pane's fullscreen frame is
		// still in tcell's cell buffer over every surviving pane.
		for _, leaf := range app.root.leaves() {
			leaf.pane.markFullRepaint()
		}
	}
	app.mu.Unlock()

	// The shell has exited, but a process it left behind (`server &`) may
	// still hold the PTY. Close the master so the pane's reader goroutine,
	// descriptor and emulator do not outlive the pane.
	p.Close()

	if shutdown {
		L.Info("removePane: last pane removed, shutting down")
		go app.shutdown()
		return
	}

	if newActive != nil {
		app.focusIn(newActive)
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
		if p == exclude || p.IsDead() {
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

// tcellColorToXParse converts a tcell.Color to XParseColor format
// "rgb:<rrrr>/<gggg>/<bbbb>" for use in OSC 10/11 terminal colour query
// responses.  Each 8-bit RGB component is doubled to 16-bit (0xd0 → "d0d0").
// Returns "" when the colour is not known as an RGB value (for example the
// `terminal` theme's ColorDefault), so bunk suppresses the reply instead of
// claiming a made-up colour.
func tcellColorToXParse(c tcell.Color) string {
	r, g, b := c.RGB()
	if r < 0 {
		return ""
	}
	return fmt.Sprintf("rgb:%02x%02x/%02x%02x/%02x%02x", r, r, g, g, b, b)
}
