package main

import (
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/cmd/bunk-gui/gl"
	"bunk/internal/input"
	"bunk/internal/session"
)

// viewSlot is one render target: a device-sized RGBA8 texture and its FBO.
// GTK may read a slot's texture until the GdkTexture wrapping it is released.
type viewSlot struct {
	tex, fbo uint32
	w, h     int
	sync     uintptr
}

// Pane is one terminal: a session and the TermView that shows it. A tab
// holds one or more panes.
type Pane struct {
	id     int // index in App.panes; the C view reports it
	app    *App
	tab    *Tab
	s      *session.Session
	widget *gtk.Widget  // the TermView
	frame  *gtk.Overlay // widget plus the PASS badge; what splits hold
	token  string       // notify session token; "" without the socket

	passthrough bool // every key but the passthrough binding goes to the terminal
	passBadge   *gtk.Label
	view        cview
	im          *gtk.IMMulticontext
	imPtr       uintptr

	// Agent status, refreshed on the main thread when the session reports
	// a change (Hooks.StatusChanged). statusOverride replaces it in tests
	// (the "status" script action).
	st             session.Status
	statusOverride *session.Status
	statusQueued   atomic.Bool

	r     *Renderer // nil until the view is realized
	atlas *Atlas
	tv    *termView
	slots [viewSlots]viewSlot

	cols, rows   int          // size last requested from the session
	want         atomic.Int64 // cols<<32 | rows for the resize worker
	resizing     atomic.Bool  // a resize worker is reflowing
	imX, imY     int          // cursor cell last reported to the input method
	renderQueued atomic.Bool
	// Latency stamps in monoNs, written by the PTY reader and key paths.
	outPending atomic.Int64 // oldest PTY output not yet rendered, 0 if none
	outLast    atomic.Int64 // newest PTY output
	keyPending atomic.Int64 // oldest key write not yet rendered, 0 if none
	refusing   atomic.Bool  // an "input not sent" toast is up for this pane
	events     atomic.Int64 // PTY output chunks plus key writes, for the alloc probe

	titleSeen string // last Term.Title(), read by the PTY reader (Applied)
	title     string // shown on the tab

	// Mouse state (main thread).
	mouseBtn   input.Button // button held while reporting to the application
	mouseCell  [2]int       // last cell reported for motion
	lastX      float64      // last pointer position, widget coordinates
	lastY      float64
	selecting  bool   // left button held for a selection
	pointerIn  bool   // the pointer is over the view
	cursorName string // pointer cursor shown over the view: "text" or "pointer" (a link)
	exited     bool
	closed     bool
	removeOnce bool
}

func (p *Pane) wake() {
	if p.renderQueued.CompareAndSwap(false, true) {
		viewWakeC(p.view)
	}
}

// queueRender redraws on the next frame (main thread, e.g. after scrolling).
func (p *Pane) queueRender() {
	p.renderQueued.Store(true)
	viewQueueRenderC(p.view)
}

// onOutput is Hooks.Output: called by the PTY reader after each chunk.
func (p *Pane) onOutput(_ []byte, _, _ bool) {
	now := monoNs()
	p.outPending.CompareAndSwap(0, now)
	p.outLast.Store(now)
	p.events.Add(1)
	p.wake()
}

// onApplied is Hooks.Applied: it runs on the PTY reader with the session
// locked, so it can watch the title (OSC 0/2) without another lock.
func (p *Pane) onApplied(_ []byte, _, _ bool) {
	if t := p.s.Term.Title(); t != p.titleSeen {
		p.titleSeen = t
		coreglib.IdleAdd(func() { p.app.setTitle(p, t) })
	}
}

// onStatusChanged is Hooks.StatusChanged; it runs on session goroutines
// without the lock. Reports that arrive before the main thread has read the
// last one are merged.
func (p *Pane) onStatusChanged() {
	if p.statusQueued.CompareAndSwap(false, true) {
		coreglib.IdleAdd(p.refreshStatus)
	}
}

// refreshStatus reads the session's status on the main thread and updates
// the tab chrome and the needs-you ring when something shown changed.
func (p *Pane) refreshStatus() {
	p.statusQueued.Store(false)
	if p.removeOnce || p.s == nil {
		return
	}
	p.setStatus(p.s.Status())
}

func (p *Pane) setStatus(st session.Status) {
	if st == p.st {
		return
	}
	ring := p.st.State == session.AgentNeedsYou
	p.st = st
	if p.statusOverride == nil {
		if ring != (st.State == session.AgentNeedsYou) {
			p.queueRender()
		}
		p.app.refreshChrome()
	}
}

// status is the pane's agent status as shown.
func (p *Pane) status() session.Status {
	if p.statusOverride != nil {
		return *p.statusOverride
	}
	return p.st
}

// acknowledge clears a needs-you request: the user focused or typed into
// the pane.
func (p *Pane) acknowledge() {
	if o := p.statusOverride; o != nil && (o.State == session.AgentNeedsYou || o.State == session.AgentDone) {
		o.State, o.Notice = session.AgentIdle, ""
		p.queueRender()
		p.app.refreshChrome()
	}
	if (p.st.State != session.AgentNeedsYou && p.st.State != session.AgentDone) || p.s == nil {
		return
	}
	p.s.Acknowledge()
	p.setStatus(p.s.Status())
}

// onClosed is Hooks.Closed: the PTY is closed and all output is applied.
func (p *Pane) onClosed() {
	coreglib.IdleAdd(func() {
		p.closed = true
		p.app.paneDone(p)
	})
}

// onExited is Hooks.Exited. Closed normally follows; if a process the child
// left behind still holds the PTY, the tab closes anyway after a moment.
func (p *Pane) onExited() {
	p.app.childExit.Store(time.Now().UnixNano())
	coreglib.IdleAdd(func() {
		p.exited = true
		coreglib.TimeoutAdd(300, func() bool {
			if !p.closed {
				p.app.paneDone(p)
			}
			return false
		})
	})
}

// realized runs inside the view's realize vfunc with its GL context current.
func (p *Pane) realized() {
	a := p.app
	if msg := viewErrorC(p.view); msg != "" || !viewHasCtxC(p.view) {
		a.fatal("GL context: %s", msg)
		return
	}
	if err := gl.Init(); err != nil {
		a.fatal("%v", err)
		return
	}
	r, err := NewRenderer(max(1, p.cols), max(1, p.rows), p.atlas)
	if err != nil {
		a.fatal("renderer: %v", err)
		return
	}
	p.r = r
	p.r.img.budget = a.cfg.imageGPUBytes
	p.tv.invalidate()
	if a.clock == 0 {
		major, minor := viewGLVersionC(p.view)
		native := p.widget.Native()
		a.glVersion = fmt.Sprintf("%d.%d", major, minor)
		a.glRenderer = gl.GetString(gl.RENDERER)
		a.gskRenderer = coreglib.BaseObject(native.Renderer()).TypeFromInstance().Name()
		a.note("- GL %s: %s; GSK renderer %s", a.glVersion, a.glRenderer, a.gskRenderer)
		clock := p.widget.FrameClock()
		connectClockC(clock)
		a.clock = nativePtr(clock)
	}
}

// unrealize frees GL objects before the view's context goes away.
func (p *Pane) unrealize() {
	p.freeSlots(true)
	if p.r != nil {
		p.r.Delete()
		p.r = nil
	}
}

// unmapped runs when the view is hidden (another tab selected): free the
// render textures GTK no longer holds.
func (p *Pane) unmapped() {
	p.freeSlots(false)
	if p.r != nil {
		p.r.fbW, p.r.fbH = 0, 0 // force a full frame when shown again
		p.r.img.release()       // uploaded again when drawn
	}
	p.tv.invalidate()
}

func (p *Pane) freeSlots(all bool) {
	for i := range p.slots {
		s := &p.slots[i]
		if s.tex == 0 || (!all && viewSlotBusyC(p.view, i)) {
			continue
		}
		if s.sync != 0 {
			gl.DeleteSync(s.sync)
		}
		gl.DeleteFramebuffer(s.fbo)
		gl.DeleteTexture(s.tex)
		*s = viewSlot{}
	}
}

// requestResize asks for a new grid size. Reflowing a full scrollback takes
// tens of milliseconds, so it runs on a worker goroutine: the main thread
// keeps showing the previous frame meanwhile. Sizes requested during a
// reflow (a window drag) are coalesced: the worker applies only the latest
// one when it finishes, so a drag reflows at most once per finished reflow
// and once more for the size it settles at.
func (p *Pane) requestResize(cols, rows int) {
	p.cols, p.rows = cols, rows
	p.want.Store(int64(cols)<<32 | int64(rows))
	if p.resizing.CompareAndSwap(false, true) {
		go p.resizeWorker()
	} else {
		p.app.st.resizeDeferred++
	}
}

func (p *Pane) resizeWorker() {
	for {
		w := p.want.Load()
		t0 := monoNs()
		p.s.Resize(int(w>>32), int(w&0xffffffff))
		p.app.st.noteResize(monoNs() - t0)
		if p.want.Load() != w {
			continue // the size changed again during the reflow
		}
		p.resizing.Store(false)
		// A request that saw resizing==true just before the Store started
		// no worker; pick it up here.
		if p.want.Load() == w || !p.resizing.CompareAndSwap(false, true) {
			break
		}
	}
	p.wake()
}

// render runs inside the view's snapshot vfunc, in the frame clock's paint
// phase, with the view's GL context current. It renders into a free slot at
// exactly dw x dh device pixels and publishes it as a GdkGLTexture. It
// returns false when nothing changed, so the previous texture is reused.
func (p *Pane) render(dw, dh int, scale float64) bool {
	a := p.app
	if p.r == nil || p.s == nil {
		return false
	}
	st := a.st
	t0 := monoNs()
	p.renderQueued.Store(false)
	outOldest, outNewest := p.outPending.Swap(0), p.outLast.Load()

	if math.Abs(p.atlas.Scale-scale) > 1e-6 || p.atlas.Font != a.fontDesc {
		// First frame, a scale change or a text size change: rasterize
		// glyphs at device scale.
		if atlas, err := a.atlasFor(scale); err == nil {
			if atlas.CellW != p.atlas.CellW || atlas.CellH != p.atlas.CellH {
				// Before any resize below, so the PTY gets both together.
				p.s.SetCellPixels(atlas.CellW, atlas.CellH)
			}
			p.atlas = atlas
			p.r.SetAtlas(atlas)
			p.tv.invalidate()
		}
	}
	cols, rows := max(2, dw/p.atlas.CellW), max(1, dh/p.atlas.CellH)
	if cols != p.cols || rows != p.rows {
		p.requestResize(cols, rows)
	}
	if p.resizing.Load() {
		// Never wait for a reflow: keep the previous frame; the worker
		// wakes the view when it is done.
		if !p.s.TryLock() {
			st.skipped++
			return false
		}
	} else {
		p.s.Lock()
	}
	changed := p.tv.pull(p.s)
	if p.r.img.sync(p.s.Images(), p.tv) {
		p.tv.invalidate() // a placement on screen lost its bitmap
		changed = true
	}
	p.s.Unlock()
	p.r.img.collect()
	t1 := monoNs()

	if p.r.cols != p.tv.cols || p.r.rows != p.tv.rows {
		p.r.Resize(p.tv.cols, p.tv.rows)
		p.tv.invalidate()
		changed = true
	}
	if p.r.fbW != dw || p.r.fbH != dh {
		p.r.fbW, p.r.fbH = dw, dh
		changed = true
	}
	var dim, ring float32
	if p.tab != nil && p.tab.dimmed(p) {
		dim = 0.28
	}
	if p.status().State == session.AgentNeedsYou {
		ring = float32(math.Round(2 * scale))
	}
	if dim != p.r.dim || ring != p.r.ringPx {
		p.r.dim, p.r.ringPx = dim, ring
		changed = true
	}
	if !changed {
		st.skipped++
		return false
	}

	rowsBuilt := p.tv.convert(p.r)
	bg := p.tv.defBG
	p.r.clearRGB = [3]float32{float32(bg&0xff) / 255, float32(bg>>8&0xff) / 255, float32(bg>>16&0xff) / 255}
	viewSetBGC(p.view, bg&0xff<<16|bg&0xff00|bg>>16&0xff)
	t2 := monoNs()

	slot := -1
	for i := range p.slots {
		if viewSlotBusyC(p.view, i) {
			continue
		}
		if s := &p.slots[i]; s.sync != 0 {
			gl.DeleteSync(s.sync)
			s.sync = 0
		}
		if slot < 0 {
			slot = i
		}
	}
	if slot < 0 {
		// GTK still holds every texture: try again next frame.
		p.tv.invalidate()
		st.skipped++
		return false
	}
	s := &p.slots[slot]
	if s.w != dw || s.h != dh {
		allocSlot(s, dw, dh)
	}
	gl.BindFramebuffer(gl.FRAMEBUFFER, s.fbo)
	timing := p.r.BeginTimer()
	bytes := p.r.Upload()
	t3 := monoNs()
	p.r.Draw()
	if p.r.img.redo {
		p.wake() // an image could not be uploaded: draw its half blocks
	}
	p.r.DrawScrollbar(p.tv.sbOff, p.tv.sbCount)
	p.r.DrawRing()
	if timing {
		p.r.EndTimer()
	}
	s.sync = gl.FenceSync()
	gl.Flush() // the fence must reach the GPU before GSK waits on it from its context
	viewPublishC(p.view, slot, s.tex, s.sync, dw, dh)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	t4 := monoNs()
	p.r.PollTimers(&st.gpu)
	p.updateIMCursor(scale)
	end := monoNs()

	a.rendered = true
	st.frames++
	st.rows += rowsBuilt
	st.bytes += bytes
	st.uploadCalls += p.r.uploadCalls
	st.pull.Add(float64(t1-t0) / 1e3)
	st.convert.Add(float64(t2-t1) / 1e3)
	st.upload.Add(float64(t3-t2) / 1e3)
	st.draw.Add(float64(t4-t3) / 1e3)
	st.total.Add(float64(end-t0) / 1e3)
	if outOldest != 0 {
		st.outOldest.Add(float64(end-outOldest) / 1e3)
		st.outNewest.Add(float64(end-outNewest) / 1e3)
	}
	// A key counts as drawn once output that arrived after it is on screen.
	if k := p.keyPending.Load(); k != 0 && outNewest >= k {
		p.keyPending.CompareAndSwap(k, 0)
		st.keyRender.Add(float64(end-k) / 1e3)
		if a.pendHead-a.pendTail < len(a.pending) {
			a.pending[a.pendHead%len(a.pending)] = pendingFrame{counter: clockCounter(a.clock), keyNs: k}
			a.pendHead++
		}
	}
	return true
}

// updateIMCursor tells the input method where the cursor is when it moved,
// so candidate windows open next to it.
func (p *Pane) updateIMCursor(scale float64) {
	if p.imPtr == 0 || (p.tv.cur.X == p.imX && p.tv.cur.Y == p.imY) {
		return
	}
	p.imX, p.imY = p.tv.cur.X, p.tv.cur.Y
	g := viewGeomC(p.view)
	cw, ch := float64(p.atlas.CellW)/scale, float64(p.atlas.CellH)/scale
	viewSetIMCursorC(p.imPtr, int(g.bx+float64(p.imX)*cw), int(g.by+float64(p.imY)*ch), int(math.Ceil(cw)), int(math.Ceil(ch)))
}

func allocSlot(s *viewSlot, w, h int) {
	if s.tex == 0 {
		s.tex = gl.GenTexture()
		s.fbo = gl.GenFramebuffer()
	}
	gl.BindTexture(gl.TEXTURE_2D, s.tex)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, int32(w), int32(h), gl.RGBA, gl.UNSIGNED_BYTE, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
	gl.BindFramebuffer(gl.FRAMEBUFFER, s.fbo)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, s.tex, 0)
	s.w, s.h = w, h
}

// scrollBy moves the scrollback view n lines (positive = back in history).
func (p *Pane) scrollBy(n int) {
	switch {
	case n > 0:
		p.s.ScrollUp(n)
	case n < 0:
		p.s.ScrollDown(-n)
	}
	p.queueRender()
}

// snapToLive returns to the live view, as any key that sends input does.
func (p *Pane) snapToLive() {
	if p.s.InScrollback() {
		p.s.ScrollReset()
		p.queueRender()
	}
}

// write sends input to the child, stamping it for the latency statistics.
// It never waits for the child: input it has no room for is refused.
func (p *Pane) write(data []byte) {
	if len(data) == 0 {
		return
	}
	now := monoNs()
	stamped := p.keyPending.CompareAndSwap(0, now)
	if err := p.s.WriteInput(data); err != nil {
		if stamped {
			p.keyPending.CompareAndSwap(now, 0)
		}
		p.inputRefused(err)
		return
	}
	p.events.Add(1)
}

// refusedToastFor is how long an "input not sent" toast stays up; while
// it does, more refused input in the same pane shows no new toast.
const refusedToastFor = 3 * time.Second

// inputRefused tells the user that input was not sent: the child stopped
// reading and its input queue is full. It runs on any goroutine.
func (p *Pane) inputRefused(err error) {
	slog.Debug("input refused", "pane", p.id, "err", err)
	if !p.refusing.CompareAndSwap(false, true) {
		return
	}
	coreglib.IdleAdd(func() {
		p.app.toast("Input not sent: " + err.Error())
		coreglib.TimeoutAdd(uint(refusedToastFor.Milliseconds()), func() bool {
			p.refusing.Store(false)
			return false
		})
	})
}

func (p *Pane) String() string { return fmt.Sprintf("pane %d", p.id) }
