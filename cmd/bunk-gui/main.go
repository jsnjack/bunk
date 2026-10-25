// Command bunk-gui is a GTK4 terminal window around bunk's session core.
//
// One AdwApplicationWindow without a title bar holds a TermView (view.go): a
// widget that renders the terminal with OpenGL into a texture at the exact
// device-pixel size of the widget, so text stays on the pixel grid at
// fractional scales. The emulator, PTY and scrollback come from
// bunk/internal/session; keys are encoded by bunk/internal/input.
//
//	bunk-gui                     # run $SHELL
//	bunk-gui -e 'htop'           # run sh -c CMD; quit when it exits
//	bunk-gui -e 'cat big.txt; sleep 1' -stats -timeout 10s
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/cmd/bunk-gui/gl"
	"bunk/internal/input"
	"bunk/internal/session"
)

const appID = "io.github.jsnjack.BunkGui"

func init() { runtime.LockOSThread() }

type config struct {
	cmd        string
	font       string
	size       string
	dump       string
	dumpSize   string
	stats      bool
	allocProbe bool
	verify     bool
	verifyDir  string
	timeout    time.Duration
	typeEvery  time.Duration
	maximize   time.Duration
	gsk        string
}

// viewSlot is one render target: a device-sized RGBA8 texture and its FBO.
// GTK may read a slot's texture until the GdkTexture wrapping it is released.
type viewSlot struct {
	tex, fbo uint32
	w, h     int
	sync     uintptr
}

type pendingFrame struct {
	counter int64
	keyNs   int64 // monoNs of the key write drawn in that frame
}

type App struct {
	cfg  config
	app  *adw.Application
	win  *adw.ApplicationWindow
	view *gtk.Widget
	s    *session.Session
	done chan struct{}

	atlas      *Atlas
	r          *Renderer
	tv         *termView
	cols, rows int // size last requested from the session
	slots      [viewSlots]viewSlot
	clock      uintptr
	monScale   float64

	renderQueued atomic.Bool
	// Latency stamps in monoNs, written by the PTY reader and key paths.
	outPending atomic.Int64 // oldest PTY output not yet rendered, 0 if none
	outLast    atomic.Int64 // newest PTY output
	keyPending atomic.Int64 // oldest key write not yet rendered, 0 if none
	events     atomic.Int64 // PTY output chunks plus key writes, for the alloc probe

	st        *runStats
	beforePnt int64
	rendered  bool // a frame was published in the current frame-clock cycle
	pending   [256]pendingFrame
	pendHead  int
	pendTail  int
	probeMS   [2]runtime.MemStats
	mem0      runtime.MemStats

	started time.Time
	mark    struct {
		at                  time.Time
		mainCPU, procCPU    int64
		frames, clockFrames int
		mallocs             uint64
	}
	childExit           atomic.Int64 // UnixNano when the child exited
	mainCPU0, procCPU0  int64
	exitArmed, finished bool
}

var theApp *App

var keyX = []byte{'x'}

func main() {
	var cfg config
	flag.StringVar(&cfg.cmd, "e", "", "run `CMD` with sh -c instead of $SHELL")
	flag.StringVar(&cfg.font, "font", "Noto Sans Mono 11", "Pango font description")
	flag.StringVar(&cfg.size, "size", "120x36", "initial size in cells, COLSxROWS")
	flag.StringVar(&cfg.dump, "dump", "", "on exit, write a PNG crop of the last frame to `FILE`")
	flag.StringVar(&cfg.dumpSize, "dump-size", "1280x720", "size of the -dump crop in device pixels")
	flag.BoolVar(&cfg.stats, "stats", false, "print frame statistics on exit")
	flag.BoolVar(&cfg.allocProbe, "alloc-probe", false, "count heap allocations inside the frame path (implies -stats)")
	flag.BoolVar(&cfg.verify, "verify", false, "on exit, check texel:pixel mapping through GSK")
	flag.StringVar(&cfg.verifyDir, "verify-dir", os.TempDir(), "directory for -verify PNG crops")
	flag.DurationVar(&cfg.timeout, "timeout", 0, "quit after this long (automated runs)")
	flag.DurationVar(&cfg.typeEvery, "type-every", 0, "write 'x' to the PTY at this interval (latency runs)")
	flag.DurationVar(&cfg.maximize, "maximize-after", 0, "maximize the window after this long (resize checks)")
	flag.StringVar(&cfg.gsk, "gsk", "gl", "GSK renderer to request when GSK_RENDERER is unset: gl, vulkan, or default")
	flag.Parse()
	if cfg.allocProbe {
		cfg.stats = true
	}

	// GTK 4.22 picks the Vulkan GSK renderer by default. With it, every GL
	// texture is exported as a dmabuf with an implicit modifier that RADV
	// rejects, and GTK copies each frame through the CPU. The GL renderer
	// samples our texture directly.
	if os.Getenv("GSK_RENDERER") == "" && cfg.gsk != "default" {
		os.Setenv("GSK_RENDERER", cfg.gsk)
	}

	a := &App{cfg: cfg, tv: newTermView(), done: make(chan struct{}), st: &runStats{}}
	if cfg.stats {
		a.st = newRunStats()
	}
	theApp = a
	monoBase = glibMonotonicUs() * 1000
	monoStart = time.Now()

	a.app = adw.NewApplication(appID, gio.ApplicationNonUnique)
	a.app.ConnectActivate(a.activate)
	code := a.app.Run(os.Args[:1])
	close(a.done)
	if a.s != nil {
		a.s.Close()
	}
	os.Exit(code)
}

func (a *App) activate() {
	mons := gdk.DisplayGetDefault().Monitors()
	a.monScale = 1
	if mons.NItems() > 0 {
		a.monScale = mons.Item(0).Cast().(*gdk.Monitor).Scale()
	}
	atlas, err := BuildAtlas(a.cfg.font, a.monScale)
	if err != nil {
		a.fatal("%v", err)
		return
	}
	a.atlas = atlas
	cols, rows := 120, 36
	if _, err := fmt.Sscanf(a.cfg.size, "%dx%d", &cols, &rows); err != nil || cols < 2 || rows < 1 {
		a.fatal("bad -size %q", a.cfg.size)
		return
	}

	// The view must exist before the session starts: output wakes it.
	a.view = newViewWidget()
	keys := gtk.NewEventControllerKey()
	keys.ConnectKeyPressed(a.onKey)
	a.view.AddController(keys)

	var args []string
	if a.cfg.cmd != "" {
		args = []string{"/bin/sh", "-c", a.cfg.cmd}
	}
	hex := func(rgb uint32) string { return session.XParseColor(fmt.Sprintf("%06x", rgb)) }
	a.started = time.Now()
	s, err := session.Start(session.Options{
		Cols: cols, Rows: rows, Scrollback: session.DefaultScrollbackLines, Args: args,
		ThemeFG: hex(theme.fg), ThemeBG: hex(theme.bg), ThemeCursor: hex(theme.fg),
		CellAspect: []float64{float64(atlas.CellH) / float64(atlas.CellW)},
	})
	if err != nil {
		a.fatal("start session: %v", err)
		return
	}
	a.s, a.cols, a.rows = s, cols, rows
	s.Run(session.Hooks{Output: a.onOutput, Redraw: a.wake, Exited: a.onExited}, a.done)

	a.win = adw.NewApplicationWindow(&a.app.Application)
	a.win.SetTitle("bunk-gui")
	a.win.SetContent(a.view)
	// Logical size that holds the grid in device pixels, plus one pixel for
	// a fractional window origin.
	w := int(math.Ceil(float64(cols*atlas.CellW)/a.monScale)) + 1
	h := int(math.Ceil(float64(rows*atlas.CellH)/a.monScale)) + 1
	a.win.SetDefaultSize(w, h)
	a.win.Present()
	a.view.GrabFocus()

	a.mainCPU0, a.procCPU0 = threadCPUNs(), procCPUNs()
	runtime.ReadMemStats(&a.mem0)
	if a.cfg.stats {
		// Steady-state mark: everything after the first second (startup,
		// first frames and atlas build excluded).
		coreglib.TimeoutAdd(1000, func() bool {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			a.mark.at, a.mark.mainCPU, a.mark.procCPU = time.Now(), threadCPUNs(), procCPUNs()
			a.mark.frames, a.mark.clockFrames, a.mark.mallocs = a.st.frames, a.st.clockFrames, ms.Mallocs
			return false
		})
	}
	if a.cfg.timeout > 0 {
		coreglib.TimeoutAdd(uint(a.cfg.timeout.Milliseconds()), func() bool {
			a.finish("timeout")
			return false
		})
	}
	if a.cfg.typeEvery > 0 {
		go a.typeDriver(a.cfg.typeEvery)
	}
	if a.cfg.maximize > 0 {
		coreglib.TimeoutAdd(uint(a.cfg.maximize.Milliseconds()), func() bool {
			a.win.Maximize()
			return false
		})
	}
}

func newViewWidget() *gtk.Widget {
	obj := coreglib.Take(viewNewC())
	w := obj.WalkCast(func(o coreglib.Objector) bool {
		_, ok := o.(gtk.Widgetter)
		return ok
	})
	return gtk.BaseWidget(w.(gtk.Widgetter))
}

func (a *App) note(format string, args ...any) {
	if a.cfg.stats {
		fmt.Printf(format+"\n", args...)
	}
}

func (a *App) fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bunk-gui: "+format+"\n", args...)
	a.app.Quit()
}

// viewRealized runs inside TermView's realize vfunc with its GL context
// current.
func (a *App) viewRealized() {
	if msg := viewErrorC(); msg != "" {
		a.fatal("GL context: %s", msg)
		return
	}
	if err := gl.Init(); err != nil {
		a.fatal("%v", err)
		return
	}
	r, err := NewRenderer(a.cols, a.rows, a.atlas)
	if err != nil {
		a.fatal("renderer: %v", err)
		return
	}
	a.r = r
	major, minor := viewGLVersionC()
	native := a.view.Native()
	a.note("- GL %d.%d: %s; GSK renderer %s", major, minor, gl.GetString(gl.RENDERER),
		coreglib.InternObject(native.Renderer()).TypeFromInstance().Name())
	clock := a.view.FrameClock()
	connectClockC(clock)
	a.clock = nativePtr(clock)
}

// wake queues a frame. It runs on the PTY reader goroutine (Output, Redraw)
// and at most once per rendered frame reaches the main loop.
func (a *App) wake() {
	if a.renderQueued.CompareAndSwap(false, true) {
		viewWakeC()
	}
}

// onOutput is Hooks.Output: called by the PTY reader after each chunk.
func (a *App) onOutput(_ []byte, _, _ bool) {
	now := monoNs()
	a.outPending.CompareAndSwap(0, now)
	a.outLast.Store(now)
	a.events.Add(1)
	a.wake()
}

func (a *App) onExited() {
	a.childExit.Store(time.Now().UnixNano())
	coreglib.IdleAdd(func() {
		// The reader may still hold the child's last output; give it a
		// moment, render once more, and quit after that frame.
		coreglib.TimeoutAdd(50, func() bool {
			a.exitArmed = true
			viewWakeC()
			coreglib.TimeoutAdd(1000, func() bool { a.finish("child exited (no frame)"); return false })
			return false
		})
	})
}

// onKey maps a key press to terminal input. kittyFlags and the modes are read
// under the session lock.
func (a *App) onKey(keyval, _ uint, state gdk.ModifierType) bool {
	ev, ok := keyEvent(keyval, state)
	if !ok || a.s == nil {
		return false
	}
	a.s.Lock()
	kitty := 0
	if n := len(a.s.KittyStack); n > 0 {
		kitty = a.s.KittyStack[n-1]
	}
	modes := session.InputModes(kitty, a.s.Term.Mode())
	a.s.Unlock()
	var data []byte
	if b, ok := ctrlByte(ev); ok && kitty&1 == 0 {
		data = []byte{b}
	} else {
		data = input.EncodeKey(ev, modes)
	}
	if len(data) == 0 {
		return false
	}
	a.keyPending.CompareAndSwap(0, monoNs())
	a.events.Add(1)
	a.s.WriteInput(data)
	return true
}

// typeDriver stands in for a typist: it writes 'x' to the PTY at a fixed
// interval, so echo latency can be measured without synthesizing GDK events.
func (a *App) typeDriver(every time.Duration) {
	time.Sleep(time.Second) // let the shell start
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-a.done:
			return
		case <-t.C:
		}
		a.keyPending.CompareAndSwap(0, monoNs())
		a.events.Add(1)
		a.s.WriteInput(keyX)
	}
}

// viewRender runs inside TermView's snapshot vfunc, in the frame clock's
// paint phase, with the GL context current. It renders into a free slot at
// exactly dw x dh device pixels and publishes it as a GdkGLTexture. It
// returns false when nothing changed, so the previous texture is reused.
func (a *App) viewRender(dw, dh int, scale float64) bool {
	if a.r == nil || a.s == nil {
		return false
	}
	st := a.st
	probe := a.cfg.allocProbe
	var probeEvents int64
	var probeAtlas *Atlas
	var probeGlyphs, probeCells, probeFB int
	if probe {
		probeEvents, probeAtlas, probeGlyphs = a.events.Load(), a.atlas, a.atlas.Rasterized
		probeCells, probeFB = a.r.cols*a.r.rows, a.r.fbW*a.r.fbH
		runtime.ReadMemStats(&a.probeMS[0])
	}
	t0 := monoNs()
	a.renderQueued.Store(false)
	outOldest, outNewest := a.outPending.Swap(0), a.outLast.Load()

	if math.Abs(a.atlas.Scale-scale) > 1e-6 {
		// First frame or a scale change: rasterize glyphs at device scale.
		if atlas, err := BuildAtlas(a.cfg.font, scale); err == nil {
			a.atlas = atlas
			a.r.SetAtlas(atlas)
			a.tv.invalidate()
		}
	}
	cols, rows := max(2, dw/a.atlas.CellW), max(1, dh/a.atlas.CellH)
	if cols != a.cols || rows != a.rows {
		a.cols, a.rows = cols, rows
		a.s.Resize(cols, rows)
	}

	a.s.Lock()
	changed := a.tv.pull(a.s.Term)
	a.s.Unlock()
	t1 := monoNs()

	if a.r.cols != a.tv.cols || a.r.rows != a.tv.rows {
		a.r.Resize(a.tv.cols, a.tv.rows)
		a.tv.invalidate()
		changed = true
	}
	if a.r.fbW != dw || a.r.fbH != dh {
		a.r.fbW, a.r.fbH = dw, dh
		changed = true
	}
	if !changed {
		st.skipped++
		return false
	}

	rowsBuilt := a.tv.convert(a.r)
	bg := a.tv.defBG
	a.r.clearRGB = [3]float32{float32(bg&0xff) / 255, float32(bg>>8&0xff) / 255, float32(bg>>16&0xff) / 255}
	viewSetBGC(bg&0xff<<16 | bg&0xff00 | bg>>16&0xff)
	t2 := monoNs()

	slot := -1
	for i := range a.slots {
		if viewSlotBusyC(i) {
			continue
		}
		if s := &a.slots[i]; s.sync != 0 {
			gl.DeleteSync(s.sync)
			s.sync = 0
		}
		if slot < 0 {
			slot = i
		}
	}
	if slot < 0 {
		// GTK still holds every texture: try again next frame.
		a.tv.invalidate()
		st.skipped++
		return false
	}
	s := &a.slots[slot]
	if s.w != dw || s.h != dh {
		allocSlot(s, dw, dh)
	}
	gl.BindFramebuffer(gl.FRAMEBUFFER, s.fbo)
	timing := a.r.BeginTimer()
	bytes := a.r.Upload()
	t3 := monoNs()
	a.r.Draw()
	if timing {
		a.r.EndTimer()
	}
	s.sync = gl.FenceSync()
	gl.Flush() // the fence must reach the GPU before GSK waits on it from its context
	viewPublishC(slot, s.tex, s.sync, dw, dh)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	t4 := monoNs()
	a.r.PollTimers(&st.gpu)
	end := monoNs()

	a.rendered = true
	st.frames++
	st.rows += rowsBuilt
	st.bytes += bytes
	st.uploadCalls += a.r.uploadCalls
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
	if k := a.keyPending.Load(); k != 0 && outNewest >= k {
		a.keyPending.CompareAndSwap(k, 0)
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
		if a.events.Load() == probeEvents && a.atlas == probeAtlas && a.atlas.Rasterized == probeGlyphs &&
			a.r.cols*a.r.rows == probeCells && a.r.fbW*a.r.fbH == probeFB {
			st.probeMallocs += a.probeMS[1].Mallocs - a.probeMS[0].Mallocs
			st.probeFrames++
		} else {
			st.probeSkipped++
		}
	}
	return true
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

func (a *App) beforePaint() { a.beforePnt = monoNs() }

func (a *App) afterPaint() {
	now := monoNs()
	a.st.clockFrames++
	if a.rendered {
		a.st.gtkFrame.Add(float64(now-a.beforePnt) / 1e3)
	}
	a.rendered = false
	a.pollPresentation()
	if a.exitArmed {
		a.finish("child exited")
	}
}

// pollPresentation reads GDK frame timings (wp_presentation feedback) for
// frames that drew a key echo.
func (a *App) pollPresentation() {
	var ft frameTimings
	for a.pendTail < a.pendHead {
		pf := &a.pending[a.pendTail%len(a.pending)]
		rc := clockTimings(a.clock, pf.counter, &ft)
		if rc == 0 {
			return
		}
		a.pendTail++
		if rc > 0 && ft.present != 0 {
			a.st.keyPresent.Add(float64(ft.present*1000-pf.keyNs) / 1e3)
		}
	}
}

// finish writes the requested dump, checks and statistics, then quits.
func (a *App) finish(reason string) {
	if a.finished {
		return
	}
	a.finished = true
	if a.cfg.dump != "" {
		a.writeDump()
	}
	if a.cfg.verify {
		a.verify()
	}
	if a.cfg.stats {
		a.report(reason)
	}
	a.app.Quit()
}

func (a *App) report(reason string) {
	wall := time.Since(a.started)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	g := viewGeomC()
	fmt.Printf("\n## bunk-gui run (%s)\n\n", reason)
	fmt.Printf("- window %dx%d logical at scale %.4f -> texture %dx%d device px; grid %dx%d cells of %dx%d px\n",
		g.lw, g.lh, g.scale, g.dw, g.dh, a.cols, a.rows, a.atlas.CellW, a.atlas.CellH)
	if t := a.childExit.Load(); t != 0 {
		fmt.Printf("- child ran %.3fs (start to exit)\n", float64(t-a.started.UnixNano())/1e9)
	}
	fmt.Printf("- atlas: %d glyphs rasterized (%d as U+FFFD), %dx%d px, %d lookups found no slot\n",
		a.atlas.Rasterized, a.atlas.Replaced, a.atlas.W, a.atlas.H, a.atlas.Full)
	fmt.Printf("- snapshots %d, renders %d, reused %d, wake-ups %d, scale changes %d; view has keyboard focus: %v\n",
		g.snapshots, g.renders, g.reused, g.wakes, g.scaleChg, a.view.HasFocus())
	fmt.Printf("- CPU: GTK main thread %.3fs, whole process %.3fs over %.3fs wall\n",
		float64(threadCPUNs()-a.mainCPU0)/1e9, float64(procCPUNs()-a.procCPU0)/1e9, wall.Seconds())
	fmt.Printf("- heap: %d mallocs, %d bytes since the window opened (whole process)\n",
		ms.Mallocs-a.mem0.Mallocs, ms.TotalAlloc-a.mem0.TotalAlloc)
	if !a.mark.at.IsZero() {
		span := time.Since(a.mark.at).Seconds()
		frames := a.st.frames - a.mark.frames
		perFrame := 0.0
		if frames > 0 {
			perFrame = float64(ms.Mallocs-a.mark.mallocs) / float64(frames)
		}
		fmt.Printf("- after the 1 s mark (%.2fs): %d frames, %d frame-clock frames, main thread %.1f%% of a core, process %.1f%%, %d mallocs (%.2f per frame, whole process)\n",
			span, frames, a.st.clockFrames-a.mark.clockFrames,
			100*float64(threadCPUNs()-a.mark.mainCPU)/1e9/span, 100*float64(procCPUNs()-a.mark.procCPU)/1e9/span,
			ms.Mallocs-a.mark.mallocs, perFrame)
	}
	fmt.Println()
	a.st.report(os.Stdout, wall)
}

func (a *App) writeDump() {
	pix, w, h := downloadLastC()
	if pix == nil {
		fmt.Fprintln(os.Stderr, "bunk-gui: -dump: no frame")
		return
	}
	cw, ch := 1280, 720
	fmt.Sscanf(a.cfg.dumpSize, "%dx%d", &cw, &ch) //nolint:errcheck // keeps the defaults on error
	cw, ch = min(cw, w), min(ch, h)
	rgba := make([]byte, cw*ch*4)
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			si, di := (y*w+x)*4, (y*cw+x)*4
			rgba[di], rgba[di+1], rgba[di+2] = pix[si+2], pix[si+1], pix[si]
		}
	}
	if err := writePNG(a.cfg.dump, rgba, cw, ch); err != nil {
		fmt.Fprintf(os.Stderr, "bunk-gui: -dump: %v\n", err)
	}
}

func threadCPUNs() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(1 /* RUSAGE_THREAD */, &ru)
	return ru.Utime.Nano() + ru.Stime.Nano()
}

func procCPUNs() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return ru.Utime.Nano() + ru.Stime.Nano()
}

var (
	monoBase  int64 // g_get_monotonic_time() in ns at monoStart
	monoStart = time.Now()
)

// monoNs returns CLOCK_MONOTONIC in ns on the same base as
// g_get_monotonic_time, so it compares with GDK presentation times. It does
// not call into C and does not allocate.
func monoNs() int64 { return monoBase + int64(time.Since(monoStart)) }
