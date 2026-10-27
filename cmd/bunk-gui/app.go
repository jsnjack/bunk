package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/notify"
	"bunk/internal/theme"
)

const appID = "io.github.jsnjack.BunkGui"

type pendingFrame struct {
	counter int64
	keyNs   int64 // monoNs of the key write drawn in that frame
}

type atlasKey struct {
	font  string
	scale float64
}

// App is one bunk-gui process: its window, tabs and panes, the notify
// server, and the state of test runs (--do, --report, --stats).
type App struct {
	cfg      config
	settings Settings
	app      *adw.Application
	win      *adw.ApplicationWindow
	done     chan struct{}

	// Window content: toasts > overlay (switcher) > rootBox = chrome + stack.
	toasts     *adw.ToastOverlay
	overlay    *gtk.Overlay
	rootBox    *gtk.Box
	stack      *gtk.Stack
	chrome     gtk.Widgetter
	tabsBox    *gtk.Box
	optionsBtn *gtk.MenuButton
	menuBox    *gtk.Box
	zoomLabel  *gtk.Label
	switcher   *switcher
	ratio      *splitRatio // the label on a held divider (splitratio.go)
	tabDrag    *tabDrag    // a tab being dragged (tabdrag.go)
	chromeGen  uint64      // tab item rebuilds; paintedGen: those the last frame laid out
	paintedGen uint64
	lastToast  string
	// Chrome widgets the report and the pointer-injection targets name.
	tabItems    []tabItem
	newTabBtn   gtk.Widgetter
	closeWinBtn gtk.Widgetter
	menuButtons map[string]*gtk.Button
	about       *adw.AboutDialog
	passCheck   *gtk.CheckButton // menu: Send all keys to terminal
	syncingMenu bool             // passCheck is being set from the pane

	shortcutsWin *shortcutsWindow
	prefsWin     *prefsWindow
	find         *finder // the find bar, nil when closed
	quitDialog   *adw.AlertDialog
	quitBody     []string      // the quit dialog's list of busy tabs
	rename       *renameDialog // the rename dialog (tabname.go), nil when closed

	notify       *notify.Server // agent hook reports; nil when the socket failed
	cfgMonitor   gio.FileMonitorrer
	fileSettings Settings // as the config file last read had them
	toastLog     []string
	chromeKey    string // what the tab items show, to skip identical rebuilds
	layout       string // "bar" or "rail"
	railOpen     bool
	fontSize     float64          // points; settings.FontSize is 100 %
	fontDesc     string           // Pango description at fontSize; atlases are keyed by it
	theme        theme.Theme      // the theme shown, every colour set (themes.go)
	themeCSS     *gtk.CSSProvider // the colours that follow the theme
	themeErr     error            // what was wrong with --theme, shown at start

	tabs               []*Tab
	active             *Tab
	panes              []*Pane // by id; nil once closed
	atlases            map[atlasKey]*Atlas
	monScale           float64
	initCols, initRows int
	dumpW, dumpH       int // --dump-size
	clock              uintptr

	st        *runStats
	beforePnt int64
	rendered  bool // a frame was published in the current frame-clock cycle
	pending   [256]pendingFrame
	pendHead  int
	pendTail  int
	mem0      runtime.MemStats

	// --report and test hooks.
	rep                                *runReport
	glRenderer, glVersion, gskRenderer string
	ptrX, ptrY                         float64 // last pointer position over the window
	injectAt                           int64   // monoNs of the last injected key, 0 if consumed
	typingStop                         chan struct{}
	pngJobs                            sync.WaitGroup
	shotsPending                       int // shots waiting for a frame
	// --do script (script.go): its steps, the next one, and its start.
	steps    []scriptStep
	stepNext int
	scriptT0 time.Time
	// Frames rendered when the current and the previous step started
	// ("wait frames N" counts from the step before it).
	stepFrames, prevStepFrames int
	inputSent                  int      // injected key and button events (wait input)
	paintWaiters               []func() // onNextPaint callbacks
	pngMu                      sync.Mutex
	pngErrs                    []string

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
	pendingClip         string     // OSC 52 text waiting for the window to be active
	alerts              alertState // desktop notifications (alerts.go)
	configErr           error
	fatalErr            error // why the application quit early (fatal)
}

var theApp *App

var keyX = []byte{'x'}

// font returns the Pango description for the current text size.
func (a *App) font() string { return fmt.Sprintf("%s %g", a.settings.Font, a.fontSize) }

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
	loadCSS()
	a.loadThemeCSS()

	a.win = adw.NewApplicationWindow(&a.app.Application)
	a.win.SetTitle(defaultTitle)
	a.win.AddCSSClass("bunk-window")
	a.stack = gtk.NewStack()
	a.stack.SetHExpand(true)
	a.stack.SetVExpand(true)
	a.stack.AddCSSClass("bunk-content")
	a.rootBox = gtk.NewBox(gtk.OrientationHorizontal, 0)
	a.rootBox.Append(a.stack)
	a.overlay = gtk.NewOverlay()
	a.overlay.SetChild(a.rootBox)
	a.toasts = adw.NewToastOverlay()
	a.toasts.SetChild(a.overlay)
	a.win.SetContent(a.toasts)
	a.buildChrome()
	// Logical size that holds the grid in device pixels, plus one pixel for
	// a fractional window origin, plus the chrome.
	w := int(math.Ceil(float64(a.initCols*atlas.CellW)/a.monScale)) + 1
	h := int(math.Ceil(float64(a.initRows*atlas.CellH)/a.monScale)) + 1
	if a.layout == "bar" {
		h += 44
	} else if a.railOpen {
		w += 268
	} else {
		w += 48
	}
	a.win.SetDefaultSize(w, h)
	a.win.NotifyProperty("is-active", a.onActive)
	a.initAlerts()
	a.win.ConnectCloseRequest(a.onCloseRequest)
	a.installShortcuts()
	a.installSplitRatio()
	a.watchConfig()
	ptr := gtk.NewEventControllerMotion()
	ptr.ConnectMotion(func(x, y float64) { a.ptrX, a.ptrY = x, y })
	ptr.ConnectEnter(func(x, y float64) { a.ptrX, a.ptrY = x, y })
	a.win.AddController(ptr)

	a.started = time.Now()
	first := a.newTab(a.cfg.cmd, "")
	if first == nil {
		return
	}
	for _, cmd := range a.cfg.tabs {
		a.newTab(cmd, "")
	}
	a.selectTab(first)
	a.win.Present()
	first.focus.widget.GrabFocus()
	if a.configErr != nil {
		a.toast("Settings: " + a.configErr.Error())
		fmt.Fprintln(os.Stderr, "bunk-gui:", a.configErr)
	}
	if a.themeErr != nil {
		a.toast(a.themeErr.Error())
		fmt.Fprintln(os.Stderr, "bunk-gui:", a.themeErr)
	}

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
		coreglib.TimeoutAdd(1000, func() bool { // let the shell start
			a.startTyping(a.cfg.typeEvery)
			return false
		})
	}
	if a.cfg.maximize > 0 {
		coreglib.TimeoutAdd(uint(a.cfg.maximize.Milliseconds()), func() bool {
			a.win.Maximize()
			return false
		})
	}
	switch {
	case a.cfg.waitFocus:
		a.waitFocus()
	case a.cfg.script != "":
		a.startScript()
	}
}

// waitFocus readies input on the headless test compositor before --do
// starts. It has no physical keyboard, so the window gets no keyboard focus
// (and stays inactive) until the RemoteDesktop session creates a virtual
// keyboard: tap Shift (and nudge the pointer, for a virtual pointer), wait
// for the window to become active, then start the script. Script times
// count from there.
func (a *App) waitFocus() {
	err := injectOpen()
	if err == nil {
		err = a.addDevices()
	}
	if err != nil {
		a.reportError("--wait-focus: %v", err)
		a.startScript()
		return
	}
	tries := 0
	coreglib.TimeoutAdd(50, func() bool {
		switch {
		case a.win.IsActive():
		case tries == 100:
			a.reportError("--wait-focus: the window did not become active within 5 s")
		default:
			tries++
			return true
		}
		a.startScript()
		return false
	})
}

func (a *App) startScript() {
	a.rep.ActiveMs = time.Since(a.started).Milliseconds()
	if a.cfg.script != "" {
		a.runScript(a.cfg.script)
	}
}

// atlasFor returns the shared glyph atlas for the current font at a device
// scale.
func (a *App) atlasFor(scale float64) (*Atlas, error) {
	font := a.fontDesc
	for k, at := range a.atlases {
		if k.font == font && math.Abs(k.scale-scale) < 1e-6 {
			return at, nil
		}
	}
	at, err := BuildAtlas(font, scale)
	if err != nil {
		return nil, err
	}
	a.atlases[atlasKey{font, scale}] = at
	return at, nil
}

func (a *App) note(format string, args ...any) {
	if a.cfg.stats {
		fmt.Printf(format+"\n", args...)
	}
}

// fatal quits the application; runApp returns the error, so bunk-gui
// prints it and exits 1.
func (a *App) fatal(format string, args ...any) {
	a.fatalErr = fmt.Errorf(format, args...)
	a.app.Quit()
}

// startTyping stands in for a typist: a goroutine writes 'x' to the
// focused pane's PTY at a fixed interval, so echo latency can be measured
// without going through the compositor. stopTyping ends it.
func (a *App) startTyping(every time.Duration) {
	a.stopTyping()
	p := a.activePane()
	if p == nil {
		return
	}
	stop := make(chan struct{})
	a.typingStop = stop
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-a.done:
				return
			case <-stop:
				return
			case <-t.C:
			}
			p.write(keyX)
		}
	}()
}

func (a *App) stopTyping() {
	if a.typingStop != nil {
		close(a.typingStop)
		a.typingStop = nil
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
	a.paintedGen = a.chromeGen
	a.pollPresentation()
	a.paintDone()
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
	a.stopTyping()
	p := a.activePane()
	if p == nil {
		for _, q := range a.panes {
			if q != nil {
				p = q
			}
		}
	}
	if a.cfg.dump != "" && p != nil {
		a.writeDump(p, a.cfg.dump, a.dumpW, a.dumpH)
	}
	if a.cfg.verify && p != nil {
		a.verify(p)
	}
	if a.cfg.stats {
		a.printStats(p, reason)
	}
	if a.shotsPending > 0 {
		a.reportError("%d shots still waiting for a frame at quit", a.shotsPending)
	}
	a.waitPNG()
	if a.reportOn() {
		a.writeReport(reason)
	}
	if a.cfg.memProfile != "" {
		runtime.GC() // the profile shows allocations up to the last GC
		if err := writeAllocProfile(a.cfg.memProfile); err != nil {
			slog.Error("--memprofile", "err", err)
		}
	}
	a.app.Quit()
}

// writeAllocProfile writes the allocation profile to path.
func writeAllocProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create profile: %w", err)
	}
	if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
		if cerr := f.Close(); cerr != nil {
			slog.Log(context.Background(), LevelTrace, "close profile", "path", path, "err", cerr)
		}
		return fmt.Errorf("write profile %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close profile %s: %w", path, err)
	}
	return nil
}

func (a *App) printStats(p *Pane, reason string) {
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
		fmt.Printf("- tabs open: %d, panes open: %d; focused pane: view %dx%d logical at scale %.4f -> texture %dx%d device px; grid %dx%d cells of %dx%d px\n",
			len(a.tabs), open, g.lw, g.lh, g.scale, g.dw, g.dh, p.cols, p.rows, p.atlas.CellW, p.atlas.CellH)
		fmt.Printf("- focused view: snapshots %d, renders %d, reused %d, wake-ups %d, scale changes %d; keyboard focus: %v\n",
			g.snapshots, g.renders, g.reused, g.wakes, g.scaleChg, p.widget.HasFocus())
		fmt.Printf("- atlas: %d glyphs rasterized (%d as U+FFFD), %dx%d px, %d lookups found no slot\n",
			p.atlas.Rasterized, p.atlas.Replaced, p.atlas.W, p.atlas.H, p.atlas.Full)
	}
	for _, q := range a.panes {
		if q != nil && q != p && !q.removeOnce {
			g := viewGeomC(q.view)
			where := "hidden tab"
			if q.tab == a.active {
				where = "same tab"
			}
			fmt.Printf("- pane %d (%s, tab %d): snapshots %d, renders %d, wake-ups %d\n",
				q.id, where, a.tabIndex(q.tab)+1, g.snapshots, g.renders, g.wakes)
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
	if err := a.st.report(os.Stdout, wall); err != nil {
		slog.Error("--stats", "err", err)
	}
}

// writeDump writes the top-left maxW x maxH device pixels of p's last
// frame to path as a PNG.
func (a *App) writeDump(p *Pane, path string, maxW, maxH int) {
	pix, w, h := downloadLastC(p.view)
	if pix == nil {
		fmt.Fprintln(os.Stderr, "bunk-gui: --dump: no frame")
		return
	}
	a.savePNG(path, pix, w, min(maxW, w), min(maxH, h))
}

// cropBGRA converts the top-left cw x ch pixels of a BGRA image w pixels
// wide to RGBA.
func cropBGRA(pix []byte, w, cw, ch int) []byte {
	rgba := make([]byte, cw*ch*4)
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			si, di := (y*w+x)*4, (y*cw+x)*4
			rgba[di], rgba[di+1], rgba[di+2] = pix[si+2], pix[si+1], pix[si]
		}
	}
	return rgba
}

func threadCPUNs() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(1 /* RUSAGE_THREAD */, &ru); err != nil {
		slog.Log(context.Background(), LevelTrace, "getrusage thread", "err", err)
	}
	return ru.Utime.Nano() + ru.Stime.Nano()
}

func procCPUNs() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		slog.Log(context.Background(), LevelTrace, "getrusage self", "err", err)
	}
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
