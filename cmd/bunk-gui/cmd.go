package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/spf13/cobra"

	"bunk/internal/graphics"
	"bunk/internal/notify"
	"bunk/internal/session"
	"bunk/internal/theme"
)

// config holds the command-line options of a run.
type config struct {
	cmd        string
	tabs       []string
	font       string
	size       string
	configFile string
	layout     string
	theme      string
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
	agentHome  string // tests: the home directory whose agent hooks count
	// restoreID is the snapshot to open (--restore); offerRestore shows
	// the banner for snapshots no window holds (restore.go).
	restoreID    string
	offerRestore bool

	imageBytes    int // per pane: bitmaps of image placements (session.Options.ImageBytes)
	inputBytes    int // per pane: input queued for a program not reading it (session.Options.InputBytes)
	imageGPUBytes int // per pane: textures of placements not on screen
}

var (
	opts      config
	flagDebug bool
	flagTrace bool
)

// testFlags only serve the window tests, other automated runs and the
// windows bunk-gui starts itself, so --help leaves them out.
var testFlags = []string{"restore", "offer-restore", "do", "report", "verify", "wait-focus", "alloc-probe", "dump", "dump-size", "maximize-after", "agent-home"}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&opts.configFile, "config", "c", configPath(), "settings `FILE`")
	pf.BoolVarP(&flagDebug, "debug", "d", false, "log debug detail to stderr")
	pf.BoolVar(&flagTrace, "trace", false, "log everything to "+tracePath+", truncated on start")

	f := rootCmd.Flags()
	// Declared here so cobra does not add its -v shorthand.
	f.Bool("version", false, "print the version and exit")
	f.StringVar(&opts.cmd, "command", "", "run `CMD` with sh -c instead of $SHELL in the first tab; quit when it exits")
	f.StringArrayVar(&opts.tabs, "tab", nil, "open another tab running `CMD` (repeatable); the first tab stays selected")
	f.StringVar(&opts.font, "font", "", "Pango font description, e.g. \"Noto Sans Mono 11\" (default: config file)")
	f.StringVar(&opts.size, "size", "120x36", "initial size in cells, `COLSxROWS`")
	f.StringVar(&opts.layout, "layout", "", "tab layout, `rail|bar` (default: config file)")
	f.StringVar(&opts.theme, "theme", "", "theme `NAME`: "+strings.Join(theme.BuiltinNames(), ", ")+", or a [themes.NAME] table of the config (default: config file)")
	f.StringVar(&opts.gsk, "gsk", "gl", "GSK renderer to request when GSK_RENDERER is unset: gl, vulkan, or default")
	f.IntVar(&opts.imageBytes, "image-bytes", graphics.DefaultStoreBytes, "per pane, keep image bitmaps up to `N` bytes; older ones draw as half blocks")
	f.IntVar(&opts.imageGPUBytes, "image-gpu-bytes", DefaultImageGPUBytes, "per pane, keep image textures not on screen up to `N` bytes")
	f.IntVar(&opts.inputBytes, "input-bytes", session.DefaultInputBytes, "per pane, queue up to `N` bytes of input for a program that is not reading it; more is refused")
	f.BoolVar(&opts.stats, "stats", false, "print frame statistics on exit")
	f.DurationVar(&opts.timeout, "timeout", 0, "quit after this long")
	f.DurationVar(&opts.typeEvery, "type-every", 0, "write 'x' to the selected pane's PTY at this interval (latency runs)")
	f.StringVar(&opts.memProfile, "memprofile", "", "record every allocation and write the profile to `FILE` on exit (go tool pprof -sample_index=alloc_objects)")

	f.StringVar(&opts.restoreID, "restore", "", "open the saved window `ID` (restore.go)")
	f.BoolVar(&opts.offerRestore, "offer-restore", true, "offer the windows of the last session back")
	f.StringVar(&opts.script, "do", "", "run test `ACTIONS` on the main thread, e.g. '1s scroll 10; 2s dump x.png' (see script.go)")
	f.StringVar(&opts.report, "report", "", "on exit, write a JSON record of the run (state snapshots, marks, latencies) to `FILE`")
	f.StringVar(&opts.agentHome, "agent-home", "", "read the agents' hook configuration under `DIR`, not the home directory")
	f.BoolVar(&opts.verify, "verify", false, "on exit, check texel:pixel mapping through GSK")
	f.BoolVar(&opts.waitFocus, "wait-focus", false, "headless tests: give the compositor a keyboard, wait for keyboard focus, then start --do")
	f.BoolVar(&opts.allocProbe, "alloc-probe", false, "record every allocation, so marks count objects allocated by the frame path (slow)")
	f.StringVar(&opts.dump, "dump", "", "on exit, write a PNG crop of the last frame to `FILE`")
	f.StringVar(&opts.dumpSize, "dump-size", "1280x720", "size of the --dump crop in device pixels")
	f.DurationVar(&opts.maximize, "maximize-after", 0, "maximize the window after this long (resize checks)")
	for _, name := range testFlags {
		f.Lookup(name).Hidden = true
	}
}

var rootCmd = &cobra.Command{
	Use:          "bunk-gui",
	Short:        "A GTK4 terminal for coding agents",
	Version:      Version,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runApp(opts, flagDebug, flagTrace)
	},
}

// exitStatus is the error of a command that has already printed why it
// failed; Execute exits with it and prints nothing more.
type exitStatus int

func (s exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(s)) }

// Execute runs the command line and exits with a failing command's status.
func Execute() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	var status exitStatus
	if errors.As(err, &status) {
		os.Exit(int(status))
	}
	os.Exit(1)
}

// runApp opens bunk-gui with the options from the command line and returns
// when the application quits.
func runApp(cfg config, debug, trace bool) error {
	logs, err := initLogger(os.Stderr, debug, trace)
	if err != nil {
		return err
	}
	defer logs.close()
	cols, rows, err := parseDims(cfg.size)
	if err != nil {
		return fmt.Errorf("--size: %w", err)
	}
	if cols < 2 {
		return fmt.Errorf("--size %q: at least 2 columns", cfg.size)
	}
	dumpW, dumpH, err := parseDims(cfg.dumpSize)
	if err != nil {
		return fmt.Errorf("--dump-size: %w", err)
	}
	if cfg.allocProbe || cfg.memProfile != "" {
		runtime.MemProfileRate = 1
		runtime.KeepAlive(make([]byte, 8<<20)) // the next sample point was drawn at the old rate
	}

	// GTK 4.22 picks the Vulkan GSK renderer by default. With it, every GL
	// texture is exported as a dmabuf with an implicit modifier that RADV
	// rejects, and GTK copies each frame through the CPU. The GL renderer
	// samples our texture directly.
	if os.Getenv("GSK_RENDERER") == "" && cfg.gsk != "default" {
		if err := os.Setenv("GSK_RENDERER", cfg.gsk); err != nil {
			return fmt.Errorf("set GSK_RENDERER: %w", err)
		}
	}
	// With the GL renderer GDK need not probe Vulkan at all (it logs about
	// forty loader lines at startup otherwise).
	if os.Getenv("GSK_RENDERER") == "gl" && os.Getenv("GDK_DISABLE") == "" {
		if err := os.Setenv("GDK_DISABLE", "vulkan"); err != nil {
			return fmt.Errorf("set GDK_DISABLE: %w", err)
		}
	}

	a := &App{cfg: cfg, done: make(chan struct{}), st: &runStats{}, atlases: map[atlasKey]*Atlas{},
		rep:      &runReport{Marks: []markReport{}, Snapshots: []*uiSnapshot{}, Errors: []string{}},
		initCols: cols, initRows: rows, dumpW: dumpW, dumpH: dumpH}
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
	a.layout, a.railOpen, a.fontSize = a.settings.Tabs, a.settings.RailOpen, a.settings.startSize()
	a.fontDesc = a.font()
	a.theme, a.themeErr = a.selectTheme()
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
		logs.always("Listening on " + srv.Path())
	}
	a.app = adw.NewApplication(appID, gio.ApplicationNonUnique)
	a.app.ConnectActivate(a.activate)
	code := a.app.Run(os.Args[:1])
	close(a.done)
	if a.notify != nil {
		if err := a.notify.Close(); err != nil {
			slog.Warn("notify: close", "err", err)
		}
	}
	for _, p := range a.panes {
		if p != nil && p.s != nil {
			p.s.Close()
		}
	}
	if a.fatalErr != nil {
		return a.fatalErr
	}
	if code != 0 {
		return fmt.Errorf("GTK application exited with status %d", code)
	}
	return nil
}

// parseDims reads a WxH size whose sides are at least 1.
func parseDims(s string) (w, h int, err error) {
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("%q is not WxH: %w", s, err)
	}
	if w < 1 || h < 1 {
		return 0, 0, fmt.Errorf("%q: each side must be at least 1", s)
	}
	return w, h, nil
}
