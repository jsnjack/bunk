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
	"context"
	"io"
	"log/slog"
	"os"
	"time"
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
	var w = io.Discard
	var f *os.File

	if path != "" {
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
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
			f.Close() //nolint:errcheck // logger shutdown; nothing to log the error to
		}
	}
}

// logAlways writes msg to the log file whatever the log level, for lines
// such as the address a server listens on. Like every log line of the TUI,
// it never reaches stderr.
func logAlways(msg string) {
	r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
	if err := L.Handler().Handle(context.Background(), r); err != nil {
		L.Log(context.Background(), LevelTrace, "log", "msg", msg, "err", err)
	}
}
