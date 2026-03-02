// logger.go - slog initialisation for bunk.
//
// A single package-level *slog.Logger (L) is set up once in initLogger() and
// used throughout the codebase.  The logger writes to a file (never stdout/
// stderr, which are owned by the TUI).
//
// Log levels: trace | debug | info | warn | error
// Log format: text (key=value pairs, human-readable)
package main

import (
	"io"
	"log/slog"
	"os"
)

// LevelTrace is below Debug — logs raw PTY byte chunks for deep inspection.
const LevelTrace = slog.Level(-8)

// L is the package-level structured logger.
// Initialised by initLogger(); safe to use as soon as main() calls run().
var L *slog.Logger = slog.Default() // fallback: discard until initLogger runs

// initLogger opens the log file and configures the slog default logger.
// Returns a cleanup function that closes the file.
//
// If path is empty, logging is disabled (output goes to io.Discard).
// level must be one of: "trace", "debug", "info", "warn", "error".
func initLogger(path, level string) (cleanup func()) {
	var w io.Writer = io.Discard
	var f *os.File

	if path != "" {
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			// Can't open log file — silently disable logging.
			f = nil
		} else {
			w = f
		}
	}

	var lvl slog.Level
	switch level {
	case "trace":
		lvl = LevelTrace
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	L = slog.New(h)
	slog.SetDefault(L)

	return func() {
		if f != nil {
			f.Close()
		}
	}
}
