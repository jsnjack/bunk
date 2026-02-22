// cmd.go – cobra command definitions for bunk.
//
// main() in main.go calls Execute(), which hands off to the root command.
// The actual startup logic lives in run().
package main

import (
	"os"
	"os/exec"

	"github.com/gdamore/tcell/v2"
	"github.com/spf13/cobra"
)

var (
	flagConfig string
	flagTheme  string
	flagDebug  bool
)

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "config file path (default: ~/.config/bunk/config.toml)")
	rootCmd.PersistentFlags().StringVar(&flagTheme, "theme", "", "built-in theme name: default, solarized-dark, dracula, nord")
	rootCmd.PersistentFlags().BoolVar(&flagDebug, "debug", false, "enable debug-level logging")
}

var rootCmd = &cobra.Command{
	Use:   "bunk",
	Short: "A lightweight terminal multiplexer",
	Long: `bunk - a lightweight terminal multiplexer.

Key bindings:
  F1            Auto-split the focused pane (vertical if wide, horizontal if tall)
  Alt+Arrow     Move focus to the nearest pane in that direction
  Shift+PgUp/Dn Scroll back / forward through pane history
  Ctrl+F        Search in the current pane
  Ctrl+C        Copy selection (falls through to ^C if nothing is selected)
  Ctrl+V        Paste from system clipboard
  Ctrl+Q        Quit`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(flagConfig, flagTheme, flagDebug)
	},
}

// Execute is called by main.  It runs the cobra command tree.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// run initialises the screen, spawns the first pane, and blocks until the
// user quits.  All terminal cleanup happens synchronously after the event
// loop returns so it is guaranteed to run before the process exits.
func run(configPath, themeName string, debug bool) error {
	cfg := LoadConfig(configPath, themeName)

	// --debug overrides config log level.
	logLevel := cfg.LogLevel
	if debug {
		logLevel = "debug"
	}
	cleanup := initLogger(cfg.LogFile, logLevel)
	defer cleanup()

	L.Info("bunk starting", "theme", themeName, "log_level", logLevel)

	// Query cell aspect ratio BEFORE screen.Init() — after Init tcell owns
	// stdin and we must not read from it directly.
	cellAspect := queryCellAspect(cfg.CellAspect)
	L.Debug("startup: cell aspect ratio", "aspect", cellAspect)

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
		screen:     screen,
		theme:      cfg.Theme,
		cellAspect: cellAspect,
		redraw:     make(chan struct{}, 1),
		paneDead:   make(chan *Pane, 8),
		done:       make(chan struct{}),
		oscCh:      make(chan []byte, oscChanSize),
	}

	screen.EnableMouse(tcell.MouseMotionEvents)
	screen.EnablePaste()

	// Sync before querying size: Init() may capture a stale TIOCGWINSZ
	// snapshot if the terminal just went fullscreen.
	screen.Sync()
	w, h := screen.Size()
	L.Debug("startup: screen size", "w", w, "h", h)

	p, err := NewPane(app.nextID, 0, 0, w, h, nil, app.redraw, app.paneDead, app.done, app.oscCh)
	if err != nil {
		screen.Fini()
		return err
	}
	app.nextID++
	app.root = newLeaf(p, 0, 0, w, h)
	app.active = p

	go app.deathWatcher()
	app.renderWg.Add(1)
	go app.renderLoop()

	app.eventLoop()

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
		"\033[0m" + // reset all SGR attributes
		"\033[2J\033[H" // clear screen + cursor home
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		tty.WriteString(vtreset) //nolint:errcheck
		tty.Close()
	} else {
		os.Stdout.WriteString(vtreset) //nolint:errcheck
	}
	exec.Command("stty", "sane").Run() //nolint:errcheck
	return nil
}
