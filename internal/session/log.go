package session

import "log/slog"

// LevelTrace is below slog.LevelDebug; it logs raw PTY traffic.
const LevelTrace = slog.Level(-8)

// L returns slog's default logger, so logging set up by the front end after
// this package is initialised applies here too.
func L() *slog.Logger { return slog.Default() }
