// cmd.go - cobra command definitions for bunk.
//
// main() in main.go calls Execute(), which hands off to the root command.
// The actual startup logic lives in run().
package main

import (
	"bunk/internal/notify"
	"bunk/internal/theme"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/spf13/cobra"
)

var (
	flagConfig string
	flagTheme  string
	flagDebug  bool
	flagTrace  bool

	// Version is set at build time via -ldflags "-X main.Version=x.y.z".
	Version = "dev"
)

// bunkLogo returns the coloured ASCII art logo with the version appended.
func bunkLogo() string {
	const (
		cyan  = "\033[96m"
		blue  = "\033[94m"
		reset = "\033[0m"
	)
	return cyan + `   __                __   ` + reset + "\n" +
		cyan + `  / /_  __  ______  / /__ ` + reset + "\n" +
		blue + ` / __ \/ / / / __ \/ //_/ ` + reset + "\n" +
		blue + `/ /_/ / /_/ / / / / ,<    ` + reset + "\n" +
		blue + `\____/\__,_/_/ /_/_/|_|   ` + reset + " " + cyan + Version + reset + "\n"
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagConfig, "config", "c", "", "config file path (default: ~/.config/bunk/config.toml)")
	rootCmd.PersistentFlags().StringVar(&flagTheme, "theme", "", "theme `NAME`: "+strings.Join(theme.BuiltinNames(), ", ")+", or a [themes.NAME] table of the config")
	rootCmd.PersistentFlags().BoolVarP(&flagDebug, "debug", "d", false, "enable debug-level logging")
	rootCmd.PersistentFlags().BoolVar(&flagTrace, "trace", false, "enable trace-level logging (logs raw PTY byte chunks)")
	// Declared here so cobra does not add its -v shorthand.
	rootCmd.Flags().Bool("version", false, "print the version and exit")

	// Override help to load the config first so effective (user-overridden)
	// keybindings are shown rather than built-in defaults.
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		cfg, err := LoadConfig(flagConfig, flagTheme)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err) //nolint:errcheck // help runs before terminal initialization
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\nUsage:\n  %s\n\nFlags:\n%s", //nolint:errcheck // help text write
			bunkLogo(),
			keybindingsHelpText(&cfg.Keybindings),
			cmd.UseLine(),
			cmd.Flags().FlagUsages(),
		)
	})
}

var rootCmd = &cobra.Command{
	Use:          "bunk",
	Version:      Version,
	Short:        "A lightweight terminal multiplexer",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(flagConfig, flagTheme, flagDebug, flagTrace)
	},
}

// exitStatus is the error of a command that has already printed why it
// failed; Execute exits with it and prints nothing more.
type exitStatus int

func (s exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(s)) }

// Execute is called by main.  It runs the cobra command tree and exits
// with a failing command's status.
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

// run initialises the screen, spawns the first pane, and blocks until the
// user quits.  All terminal cleanup happens synchronously after the event
// loop returns so it is guaranteed to run before the process exits.
func run(configPath, themeName string, debug, trace bool) error {
	cfg, err := LoadConfig(configPath, themeName)
	if err != nil {
		return err
	}

	// Prevent nested sessions: BUNK=1 is set in every pane's environment.
	if os.Getenv("BUNK") != "" {
		fmt.Fprintf(os.Stderr, "%s\nAlready inside a bunk session (BUNK environment variable is set).\n\n%s\n",
			bunkLogo(), keybindingsHelpText(&cfg.Keybindings))
		os.Exit(1)
	}

	// --trace / --debug override config log level.
	logLevel := cfg.LogLevel
	switch {
	case trace:
		logLevel = "trace"
	case debug:
		logLevel = "debug"
	}
	// Only write to the log file when debug/trace is explicitly requested.
	logFile := ""
	if debug || trace {
		logFile = cfg.LogFile
	}
	cleanup := initLogger(logFile, logLevel)
	defer cleanup()

	L.Info("bunk starting", "theme", cfg.ThemeName, "log_level", logLevel)
	if cfg.ThemeErr != nil {
		L.Warn("config: theme settings", "err", cfg.ThemeErr)
	}

	// Query cell aspect ratio BEFORE screen.Init() — after Init tcell owns
	// stdin and we must not read from it directly.
	cellAspect := queryCellAspect(cfg.CellAspect)
	L.Debug("startup: cell aspect ratio", "aspect", cellAspect)

	hostColors := hostOSCColors{}
	if needsHostOSCColorProbe(cfg.Theme) {
		hostColors = probeHostOSCColors()
	}

	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	screen.SetStyle(tcell.StyleDefault.Background(cfg.Theme.bg).Foreground(cfg.Theme.fg))
	screen.HideCursor()
	screen.Clear()

	app := &App{
		screen:        screen,
		theme:         cfg.Theme,
		hostOSCColors: hostColors,
		cellAspect:    cellAspect,
		keys:          cfg.Keybindings,
		scrollback:    cfg.Scrollback,
		redraw:        make(chan struct{}, 1),
		paneDead:      make(chan *Pane, 8),
		done:          make(chan struct{}),
		oscBuf:        newOSCBuffer(),
		relay:         newAgentRelay(os.Getenv),
	}
	app.alerts.Settings = cfg.Alerts
	app.startDesktopAlerts(os.Getenv)

	// Agent hooks in the panes report through this socket ("bunk notify").
	// Without it bunk still infers each agent's state.
	if srv, err := notify.Listen(); err != nil {
		L.Warn("notify: running without agent hook reports", "err", err)
	} else {
		app.notify = srv
		logAlways("Listening on " + srv.Path())
		defer func() {
			if err := srv.Close(); err != nil {
				L.Warn("notify: close", "err", err)
			}
		}()
	}

	screen.EnableMouse(tcell.MouseMotionEvents)
	screen.EnablePaste()
	screen.EnableFocus()

	// Sync before querying size: Init() may capture a stale TIOCGWINSZ
	// snapshot if the terminal just went fullscreen.
	screen.Sync()
	w, h := screen.Size()
	L.Debug("startup: screen size", "w", w, "h", h)

	app.mu.Lock()
	p, err := app.newPane(0, 0, w, h, "", nil)
	app.mu.Unlock()
	if err != nil {
		screen.Fini()
		return err
	}
	app.root = newLeaf(p, 0, 0, w, h)
	app.active = p

	go app.deathWatcher()
	app.renderWg.Add(1)
	go app.renderLoop()

	app.eventLoop()
	app.closeDesktopAlerts()

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
		"\033]112\007" + // restore outer terminal cursor colour
		"\033[0m" + // reset all SGR attributes
		"\033[2J\033[H" // clear screen + cursor home
	// The outer bunk pane stops showing this bunk's agent.
	app.mu.Lock()
	final := app.relay.finish()
	app.mu.Unlock()
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		tty.Write(final)         //nolint:errcheck
		tty.WriteString(vtreset) //nolint:errcheck
		tty.Close()              //nolint:errcheck // /dev/tty close on shutdown path
	} else {
		os.Stdout.Write(final)         //nolint:errcheck
		os.Stdout.WriteString(vtreset) //nolint:errcheck
	}
	exec.Command("stty", "sane").Run() //nolint:errcheck
	return nil
}
