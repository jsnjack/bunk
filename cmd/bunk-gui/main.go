// Command bunk-gui is a GTK4 terminal for coding agents, around bunk's
// session core.
//
// One AdwApplicationWindow without a title bar holds tabs; a tab is a BSP
// tree of panes (GtkPaned), and a pane is a TermView (view.go): a widget that
// renders one session with OpenGL into a texture at the exact device-pixel
// size of the widget, so text stays on the pixel grid at fractional scales.
// Tabs show their agent's status in a bar on top or a rail on the side
// (chrome.go). The emulator, PTY, scrollback and agent status come from
// bunk/internal/session; keys and mouse reports are encoded by
// bunk/internal/input.
//
//	bunk-gui                     # run $SHELL
//	bunk-gui -e 'htop'           # run sh -c CMD; quit when it exits
//	bunk-gui -e 'cat big.txt; sleep 1' -stats -timeout 10s
//
// Keys: Ctrl+Shift+T/W/N new/close tab, new window; Ctrl+PgUp/PgDn,
// Ctrl+(Shift+)Tab and Ctrl+1…9 switch tabs; Ctrl+Space switcher; Ctrl+J next
// tab needing you; Ctrl+B expand/collapse the rail; F1 / Alt+F1 split (in
// the same container); F12 zoom pane; Alt+arrows move between panes;
// Ctrl+= / Ctrl+- / Ctrl+0 text size; Ctrl+Shift+C/V copy/paste;
// Shift+PgUp/PgDn scroll; Ctrl+Q quit.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
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
	configFile string
	layout     string
	dump       string
	dumpSize   string
	stats      bool
	allocProbe bool
	memProfile string
	report     string
	waitFocus  bool
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

type atlasKey struct {
	font  string
	scale float64
}

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
	quitDialog   *adw.AlertDialog
	quitBody     []string // the quit dialog's list of busy tabs

	notify       *notify.Server // agent hook reports; nil when the socket failed
	cfgMonitor   gio.FileMonitorrer
	fileSettings Settings // as the config file last read had them
	toastLog     []string
	chromeKey    string // what the tab items show, to skip identical rebuilds
	layout       string // "bar" or "rail"
	railOpen     bool
	fontSize     float64 // points; settings.FontSize is 100 %
	fontDesc     string  // Pango description at fontSize; atlases are keyed by it

	tabs               []*Tab
	active             *Tab
	panes              []*Pane // by id; nil once closed
	atlases            map[atlasKey]*Atlas
	monScale           float64
	initCols, initRows int
	clock              uintptr

	st        *runStats
	beforePnt int64
	rendered  bool // a frame was published in the current frame-clock cycle
	pending   [256]pendingFrame
	pendHead  int
	pendTail  int
	mem0      runtime.MemStats

	// -report and test hooks.
	rep                                *runReport
	glRenderer, glVersion, gskRenderer string
	ptrX, ptrY                         float64 // last pointer position over the window
	injectAt                           int64   // monoNs of the last injected key, 0 if consumed
	typingStop                         chan struct{}
	pngJobs                            sync.WaitGroup
	pngMu                              sync.Mutex
	pngErrs                            []string

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
	configErr           error
}

var theApp *App

var keyX = []byte{'x'}

func main() {
	// "bunk-gui notify …" is the agent hook CLI: no window, no GTK.
	if len(os.Args) > 1 && os.Args[1] == "notify" {
		os.Exit(notify.RunCLI(os.Args[2:], os.Environ(), os.Stdin))
	}
	var cfg config
	flag.StringVar(&cfg.cmd, "e", "", "run `CMD` with sh -c instead of $SHELL in the first tab")
	flag.Var(&cfg.tabs, "tab", "open another tab running `CMD` (repeatable); the first tab stays selected")
	flag.StringVar(&cfg.font, "font", "", "Pango font description, e.g. \"Noto Sans Mono 11\" (default: config file)")
	flag.StringVar(&cfg.size, "size", "120x36", "initial size in cells, COLSxROWS")
	flag.StringVar(&cfg.configFile, "config", configPath(), "settings `FILE` (optional)")
	flag.StringVar(&cfg.layout, "layout", "", "tab layout: rail or bar (default: config file)")
	flag.StringVar(&cfg.dump, "dump", "", "on exit, write a PNG crop of the last frame to `FILE`")
	flag.StringVar(&cfg.dumpSize, "dump-size", "1280x720", "size of the -dump crop in device pixels")
	flag.BoolVar(&cfg.stats, "stats", false, "print frame statistics on exit")
	flag.BoolVar(&cfg.allocProbe, "alloc-probe", false, "record every allocation, so marks count objects allocated by the frame path (slow)")
	flag.StringVar(&cfg.report, "report", "", "on exit, write a JSON record of the run (state snapshots, marks, latencies) to `FILE`")
	flag.BoolVar(&cfg.waitFocus, "wait-focus", false, "headless tests: give the compositor a keyboard, wait for keyboard focus, then start -do")
	flag.StringVar(&cfg.memProfile, "memprofile", "", "record every allocation and write the profile to `FILE` on exit (go tool pprof -sample_index=alloc_objects)")
	flag.BoolVar(&cfg.verify, "verify", false, "on exit, check texel:pixel mapping through GSK")
	flag.DurationVar(&cfg.timeout, "timeout", 0, "quit after this long (automated runs)")
	flag.DurationVar(&cfg.typeEvery, "type-every", 0, "write 'x' to the selected pane's PTY at this interval (latency runs)")
	flag.DurationVar(&cfg.maximize, "maximize-after", 0, "maximize the window after this long (resize checks)")
	flag.StringVar(&cfg.script, "do", "", "run test `ACTIONS` on the main thread, e.g. '1s scroll 10; 2s dump x.png' (see script.go)")
	flag.StringVar(&cfg.gsk, "gsk", "gl", "GSK renderer to request when GSK_RENDERER is unset: gl, vulkan, or default")
	flag.Parse()
	if cfg.allocProbe || cfg.memProfile != "" {
		runtime.MemProfileRate = 1
		runtime.KeepAlive(make([]byte, 8<<20)) // the next sample point was drawn at the old rate
	}

	// GTK 4.22 picks the Vulkan GSK renderer by default. With it, every GL
	// texture is exported as a dmabuf with an implicit modifier that RADV
	// rejects, and GTK copies each frame through the CPU. The GL renderer
	// samples our texture directly.
	if os.Getenv("GSK_RENDERER") == "" && cfg.gsk != "default" {
		_ = os.Setenv("GSK_RENDERER", cfg.gsk)
	}
	// With the GL renderer GDK need not probe Vulkan at all (it logs about
	// forty loader lines at startup otherwise).
	if os.Getenv("GSK_RENDERER") == "gl" && os.Getenv("GDK_DISABLE") == "" {
		_ = os.Setenv("GDK_DISABLE", "vulkan")
	}

	a := &App{cfg: cfg, done: make(chan struct{}), st: &runStats{}, atlases: map[atlasKey]*Atlas{},
		rep: &runReport{Marks: []markReport{}, Snapshots: []*uiSnapshot{}, Errors: []string{}}}
	a.settings, a.configErr = loadSettings(cfg.configFile)
	a.fileSettings = a.settings
	if cfg.font != "" {
		// A full Pango description: split off a trailing size.
		a.settings.Font = cfg.font
		if i := strings.LastIndexByte(cfg.font, ' '); i > 0 {
			var size float64
			if _, err := fmt.Sscanf(cfg.font[i+1:], "%g", &size); err == nil && size > 0 {
				a.settings.Font, a.settings.FontSize = cfg.font[:i], size
			}
		}
	}
	if cfg.layout == "rail" || cfg.layout == "bar" {
		a.settings.Tabs = cfg.layout
	}
	a.layout, a.railOpen, a.fontSize = a.settings.Tabs, a.settings.RailOpen, a.settings.FontSize
	a.fontDesc = a.font()
	if cfg.stats || cfg.report != "" || cfg.allocProbe {
		a.st = newRunStats()
	}
	theApp = a
	monoBase = glibMonotonicUs() * 1000
	monoStart = time.Now()

	if srv, err := notify.Listen(); err != nil {
		slog.Warn("notify: running without agent hook reports", "err", err)
	} else {
		a.notify = srv
	}
	a.app = adw.NewApplication(appID, gio.ApplicationNonUnique)
	a.app.ConnectActivate(a.activate)
	code := a.app.Run(os.Args[:1])
	close(a.done)
	if a.notify != nil {
		_ = a.notify.Close()
	}
	for _, p := range a.panes {
		if p != nil && p.s != nil {
			p.s.Close()
		}
	}
	os.Exit(code)
}

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
	if _, err := fmt.Sscanf(a.cfg.size, "%dx%d", &a.initCols, &a.initRows); err != nil || a.initCols < 2 || a.initRows < 1 {
		a.fatal("bad -size %q", a.cfg.size)
		return
	}
	loadCSS()

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
	a.win.ConnectCloseRequest(a.onCloseRequest)
	a.installShortcuts()
	a.watchConfig()
	ptr := gtk.NewEventControllerMotion()
	ptr.ConnectMotion(func(x, y float64) { a.ptrX, a.ptrY = x, y })
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

// waitFocus readies input on the headless test compositor before -do
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
		a.reportError("-wait-focus: %v", err)
		a.startScript()
		return
	}
	tries := 0
	coreglib.TimeoutAdd(50, func() bool {
		switch {
		case a.win.IsActive():
		case tries == 100:
			a.reportError("-wait-focus: the window did not become active within 5 s")
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

func (a *App) fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bunk-gui: "+format+"\n", args...)
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
		a.writeDump(p, a.cfg.dump)
	}
	if a.cfg.verify && p != nil {
		a.verify(p)
	}
	if a.cfg.stats {
		a.printStats(p, reason)
	}
	a.waitPNG()
	if a.reportOn() {
		a.writeReport(reason)
	}
	if a.cfg.memProfile != "" {
		runtime.GC() // the profile shows allocations up to the last GC
		if f, err := os.Create(a.cfg.memProfile); err == nil {
			_ = pprof.Lookup("allocs").WriteTo(f, 0)
			_ = f.Close()
		}
	}
	a.app.Quit()
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
	a.savePNG(path, pix, w, cw, ch)
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
