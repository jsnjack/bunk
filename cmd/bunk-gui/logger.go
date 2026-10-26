package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

// LevelTrace is below Debug: wire data and every state transition, for
// --trace.
const LevelTrace = slog.Level(-8)

// tracePath is the --trace log. Tests point it at a file of their own, so
// they never truncate the log of a real run.
var tracePath = "/tmp/bunk-gui.log"

// logOutput is where slog's default logger writes: stderr, and the trace
// log with --trace. gotk4 forwards GLib and GTK messages to slog.Default,
// so they follow the same levels.
type logOutput struct {
	handlers []slog.Handler
	trace    *os.File
}

// initLogger sends warnings and errors to stderr, everything from Debug
// with debug, and with trace every record to tracePath, truncated first.
func initLogger(stderr io.Writer, debug, trace bool) (*logOutput, error) {
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	o := &logOutput{handlers: []slog.Handler{
		slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}),
	}}
	if trace {
		f, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open trace log: %w", err)
		}
		o.trace = f
		o.handlers = append(o.handlers, slog.NewTextHandler(f, &slog.HandlerOptions{Level: LevelTrace}))
	}
	slog.SetDefault(slog.New(slog.NewMultiHandler(o.handlers...)))
	return o, nil
}

// always logs msg on every output whatever its level, for lines such as
// the address a server listens on.
func (o *logOutput) always(msg string) {
	r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
	for _, h := range o.handlers {
		if err := h.Handle(context.Background(), r.Clone()); err != nil {
			slog.Log(context.Background(), LevelTrace, "log", "msg", msg, "err", err)
		}
	}
}

// close closes the trace log; later records go to stderr only.
func (o *logOutput) close() {
	if o.trace == nil {
		return
	}
	slog.SetDefault(slog.New(o.handlers[0]))
	if err := o.trace.Close(); err != nil {
		slog.Warn("close trace log", "path", tracePath, "err", err)
	}
	o.trace = nil
}
