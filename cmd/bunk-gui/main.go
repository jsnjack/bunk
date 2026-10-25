// Command bunk-gui is a GTK4 terminal window around bunk's session core.
//
// One AdwApplicationWindow without a title bar holds tabs; each tab is a
// TermView (view.go): a widget that renders one session with OpenGL into a
// texture at the exact device-pixel size of the widget, so text stays on the
// pixel grid at fractional scales. The emulator, PTY and scrollback come
// from bunk/internal/session; keys and mouse reports are encoded by
// bunk/internal/input.
//
//	bunk-gui                     # run $SHELL
//	bunk-gui -e 'htop'           # run sh -c CMD; quit when it exits
//	bunk-gui -e 'cat big.txt; sleep 1' -stats -timeout 10s
//
// Keys: Ctrl+Shift+C/V copy/paste, Ctrl+Shift+T/W new/close tab,
// Ctrl+PgUp/PgDn and Ctrl+(Shift+)Tab switch tabs, Shift+PgUp/PgDn scroll.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
)

const appID = "io.github.jsnjack.BunkGui"

func init() { runtime.LockOSThread() }

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ", ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type config struct {
	cmd        string
	tabs       listFlag
	font       string
	size       string
	dump       string
	dumpSize   string
	stats      bool
	allocProbe bool
	verify     bool
	timeout    time.Duration
	typeEvery  time.Duration
	maximize   time.Duration
	script     string
	gsk        string
}

type pendingFrame struct {
	counter int64
	keyNs   int64 // monoNs of the key write drawn in that frame
}

type App struct {
	cfg  config
	app  *adw.Application
	win  *adw.ApplicationWindow
	tabs *adw.TabView
	done chan struct{}

	panes              []*Pane // by id; nil once closed
	atlases            map[float64]*Atlas
	monScale           float64
	initCols, initRows int
	clock              uintptr

	st        *runStats
	beforePnt int64
	rendered  bool // a frame was published in the current frame-clock cycle
	pending   [256]pendingFrame
	pendHead  int
	pendTail  int
	probeMS   [2]runtime.MemStats
	mem0      runtime.MemStats

	started            time.Time
	childExit          atomic.Int64 // UnixNano when a child exited
	mainCPU0, procCPU0 int64
	mark               struct {
		at                  time.Time
		mainCPU, procCPU    int64
		frames, clockFrames int
		mallocs             uint64
	}
	exitArmed, finished bool
	pendingClip         string // OSC 52 text waiting for the window to be active
}

var theApp *App

var keyX = []byte{'x'}

func main() {
	var cfg config
	flag.StringVar(&cfg.cmd, "e", "", "run `CMD` with sh -c instead of $SHELL in the first tab")
	flag.Var(&cfg.tabs, "tab", "open another tab running `CMD` (repeatable); the first tab stays selected")
	flag.StringVar(&cfg.font, "font", "Noto Sans Mono 11", "Pango font description")
	flag.StringVar(&cfg.size, "size", "120x36", "initial size in cells, COLSxROWS")
	flag.StringVar(&cfg.dump, "dump", "", "on exit, write a PNG crop of the last frame to `FILE`")
	flag.StringVar(&cfg.dumpSize, "dump-size", "1280x720", "size of the -dump crop in device pixels")
	flag.BoolVar(&cfg.stats, "stats", false, "print frame statistics on exit")
	flag.BoolVar(&cfg.allocProbe, "alloc-probe", false, "count heap allocations inside the frame path (implies -stats)")
	flag.BoolVar(&cfg.verify, "verify", false, "on exit, check texel:pixel mapping through GSK")
	flag.DurationVar(&cfg.timeout, "timeout", 0, "quit after this long (automated runs)")
	flag.DurationVar(&cfg.typeEvery, "type-every", 0, "write 'x' to the selected tab's PTY at this interval (latency runs)")
	flag.DurationVar(&cfg.maximize, "maximize-after", 0, "maximize the window after this long (resize checks)")
	flag.StringVar(&cfg.script, "do", "", "run test `ACTIONS` on the main thread, e.g. '1s scroll-up 10; 2s dump x.png' (see script.go)")
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

	a := &App{cfg: cfg, done: make(chan struct{}), st: &runStats{}, atlases: map[float64]*Atlas{}}
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
	for _, p := range a.panes {
		if p != nil && p.s != nil {
			p.s.Close()
		}
	}
	os.Exit(code)
}

func (a *App) activate() {
	mons := gdk.DisplayGetDefault().Monitors()
	a.monScale = 1
	if mons.NItems() > 0 {
		a.monScale = mons.Item(0).Cast().(*gdk.Monitor).Scale()
	}
	atlas, err := a.atlasFor(a.monScale)
	if err != nil {
		a.fatal("%v", err)
		return
	}
	if _, err := fmt.Sscanf(a.cfg.size, "%dx%d", &a.initCols, &a.initRows); err != nil || a.initCols < 2 || a.initRows < 1 {
		a.fatal("bad -size %q", a.cfg.size)
		return
	}

	a.win = adw.NewApplicationWindow(&a.app.Application)
	a.win.SetTitle(defaultTitle)
	a.win.SetContent(a.setupTabs())
	// Logical size that holds the grid in device pixels, plus one pixel for
	// a fractional window origin.
	w := int(math.Ceil(float64(a.initCols*atlas.CellW)/a.monScale)) + 1
	h := int(math.Ceil(float64(a.initRows*atlas.CellH)/a.monScale)) + 1
	a.win.SetDefaultSize(w, h)
	a.win.NotifyProperty("is-active", a.onActive)

	a.started = time.Now()
	first := a.newTab(a.cfg.cmd, "")
	if first == nil {
		return
	}
	for _, cmd := range a.cfg.tabs {
		a.newTab(cmd, "")
	}
	a.tabs.SetSelectedPage(first.page)
	a.win.Present()
	first.widget.GrabFocus()

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
	if a.cfg.script != "" {
		a.runScript(a.cfg.script)
	}
}

// atlasFor returns the shared glyph atlas for a device scale.
func (a *App) atlasFor(scale float64) (*Atlas, error) {
	for s, at := range a.atlases {
		if math.Abs(s-scale) < 1e-6 {
			return at, nil
		}
	}
	at, err := BuildAtlas(a.cfg.font, scale)
	if err != nil {
		return nil, err
	}
	a.atlases[scale] = at
	return at, nil
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

// typeDriver stands in for a typist: it writes 'x' to the selected tab's PTY
// at a fixed interval, so echo latency can be measured without synthesizing
// GDK events.
func (a *App) typeDriver(every time.Duration) {
	time.Sleep(time.Second) // let the shell start
	t := time.NewTicker(every)
	defer t.Stop()
	var target atomic.Pointer[Pane]
	coreglib.IdleAdd(func() { target.Store(a.activePane()) })
	for {
		select {
		case <-a.done:
			return
		case <-t.C:
		}
		if p := target.Load(); p != nil {
			p.write(keyX)
		}
	}
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
	p := a.activePane()
	if p == nil {
		for _, q := range a.panes {
			if q != nil {
				p = q
			}
		}
	}
	if a.cfg.dump != "" && p != nil {
		a.writeDump(p, a.cfg.dump)
	}
	if a.cfg.verify && p != nil {
		a.verify(p)
	}
	if a.cfg.stats {
		a.report(p, reason)
	}
	a.app.Quit()
}

func (a *App) report(p *Pane, reason string) {
	wall := time.Since(a.started)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("\n## bunk-gui run (%s)\n\n", reason)
	open := 0
	for _, q := range a.panes {
		if q != nil && !q.removeOnce {
			open++
		}
	}
	if p != nil {
		g := viewGeomC(p.view)
		fmt.Printf("- tabs open: %d; selected tab: window %dx%d logical at scale %.4f -> texture %dx%d device px; grid %dx%d cells of %dx%d px\n",
			open, g.lw, g.lh, g.scale, g.dw, g.dh, p.cols, p.rows, p.atlas.CellW, p.atlas.CellH)
		fmt.Printf("- selected view: snapshots %d, renders %d, reused %d, wake-ups %d, scale changes %d; keyboard focus: %v\n",
			g.snapshots, g.renders, g.reused, g.wakes, g.scaleChg, p.widget.HasFocus())
		fmt.Printf("- atlas: %d glyphs rasterized (%d as U+FFFD), %dx%d px, %d lookups found no slot\n",
			p.atlas.Rasterized, p.atlas.Replaced, p.atlas.W, p.atlas.H, p.atlas.Full)
	}
	for _, q := range a.panes {
		if q != nil && q != p && !q.removeOnce {
			g := viewGeomC(q.view)
			fmt.Printf("- hidden tab %d: snapshots %d, renders %d, wake-ups %d\n", q.id, g.snapshots, g.renders, g.wakes)
		}
	}
	if t := a.childExit.Load(); t != 0 {
		fmt.Printf("- child ran %.3fs (start to exit)\n", float64(t-a.started.UnixNano())/1e9)
	}
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

func (a *App) writeDump(p *Pane, path string) {
	pix, w, h := downloadLastC(p.view)
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
	if err := writePNG(path, rgba, cw, ch); err != nil {
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
