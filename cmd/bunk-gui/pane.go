package main

import (
	"fmt"
	"math"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
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

// Pane is one terminal: a session and the TermView that shows it. In
// milestone 2 every tab holds one pane.
type Pane struct {
	id     int // index in App.panes; the C view reports it
	app    *App
	s      *session.Session
	widget *gtk.Widget
	view   cview
	page   *adw.TabPage
	im     *gtk.IMMulticontext
	imPtr  uintptr

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
	events     atomic.Int64 // PTY output chunks plus key writes, for the alloc probe

	titleSeen string // last Term.Title(), read by the PTY reader (Applied)
	title     string // shown on the tab

	// Mouse state (main thread).
	mouseBtn   input.Button // button held while reporting to the application
	mouseCell  [2]int       // last cell reported for motion
	lastX      float64      // last pointer position, widget coordinates
	lastY      float64
	selecting  bool // left button held for a selection
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
	p.tv.invalidate()
	if a.clock == 0 {
		major, minor := viewGLVersionC(p.view)
		native := p.widget.Native()
		a.note("- GL %d.%d: %s; GSK renderer %s", major, minor, gl.GetString(gl.RENDERER),
			coreglib.InternObject(native.Renderer()).TypeFromInstance().Name())
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
	probe := a.cfg.allocProbe
	var probeEvents int64
	var probeAtlas *Atlas
	var probeGlyphs, probeCells, probeFB int
	if probe {
		probeEvents, probeAtlas, probeGlyphs = p.events.Load(), p.atlas, p.atlas.Rasterized
		probeCells, probeFB = p.r.cols*p.r.rows, p.r.fbW*p.r.fbH
		runtime.ReadMemStats(&a.probeMS[0])
	}
	t0 := monoNs()
	p.renderQueued.Store(false)
	outOldest, outNewest := p.outPending.Swap(0), p.outLast.Load()

	if math.Abs(p.atlas.Scale-scale) > 1e-6 {
		// First frame or a scale change: rasterize glyphs at device scale.
		if atlas, err := a.atlasFor(scale); err == nil {
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
	p.s.Unlock()
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
	p.r.DrawScrollbar(p.tv.sbOff, p.tv.sbCount)
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
	if probe {
		runtime.ReadMemStats(&a.probeMS[1])
		// Count only frames that no other goroutine could have overlapped
		// with output or keys, and that rasterized no new glyph.
		if p.events.Load() == probeEvents && p.atlas == probeAtlas && p.atlas.Rasterized == probeGlyphs &&
			p.r.cols*p.r.rows == probeCells && p.r.fbW*p.r.fbH == probeFB {
			st.probeMallocs += a.probeMS[1].Mallocs - a.probeMS[0].Mallocs
			st.probeFrames++
		} else {
			st.probeSkipped++
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
func (p *Pane) write(data []byte) {
	if len(data) == 0 {
		return
	}
	p.keyPending.CompareAndSwap(0, monoNs())
	p.events.Add(1)
	p.s.WriteInput(data)
}

func (p *Pane) String() string { return fmt.Sprintf("pane %d", p.id) }
