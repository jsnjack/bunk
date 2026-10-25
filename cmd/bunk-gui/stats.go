package main

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const maxSamples = 1 << 16

// series is a preallocated sample buffer; Add never allocates.
type series []float64

func newSeries() series { return make(series, 0, maxSamples) }

func (s *series) Add(v float64) {
	if len(*s) < cap(*s) {
		*s = append(*s, v)
	}
}

type summary struct {
	n             int
	p50, p99, max float64
	mean          float64
}

func summarize(s series) summary {
	if len(s) == 0 {
		return summary{}
	}
	c := slices.Clone(s)
	slices.Sort(c)
	rank := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(c)))) - 1
		return c[max(0, min(i, len(c)-1))]
	}
	sum := 0.0
	for _, v := range c {
		sum += v
	}
	return summary{n: len(c), p50: rank(0.50), p99: rank(0.99), max: c[len(c)-1], mean: sum / float64(len(c))}
}

// runStats collects per-frame measurements for -stats. Only the main thread
// touches it; recording never allocates.
type runStats struct {
	pull, convert, upload, draw, total series // µs, main thread
	gtkFrame                           series // µs, before-paint -> after-paint
	gpu                                series // µs, GL_TIME_ELAPSED
	outOldest, outNewest               series // µs, PTY output applied -> render end
	keyRender, keyPresent              series // µs, key written to the PTY -> render end / presentation
	injectWrite                        series // µs, key injected through the compositor -> written to the PTY
	frames, skipped, clockFrames       int
	resizeDeferred                     int // size requests made while a reflow ran
	resizes, resizeNs, resizeMaxNs     atomic.Int64
	osc52                              int // clipboard writes from applications
	rows, bytes, uploadCalls           int
}

// noteResize records one Session.Resize; it runs on a resize worker.
func (st *runStats) noteResize(ns int64) {
	st.resizes.Add(1)
	st.resizeNs.Add(ns)
	for {
		m := st.resizeMaxNs.Load()
		if ns <= m || st.resizeMaxNs.CompareAndSwap(m, ns) {
			return
		}
	}
}

func newRunStats() *runStats {
	return &runStats{
		pull: newSeries(), convert: newSeries(), upload: newSeries(), draw: newSeries(), total: newSeries(),
		gtkFrame: newSeries(), gpu: newSeries(), outOldest: newSeries(), outNewest: newSeries(),
		keyRender: newSeries(), keyPresent: newSeries(), injectWrite: newSeries(),
	}
}

func (st *runStats) report(w io.Writer, wall time.Duration) {
	_, _ = fmt.Fprintf(w, "| %-40s | %6s | %9s | %9s | %9s | %9s |\n", "metric", "n", "p50", "p99", "max", "mean")
	_, _ = fmt.Fprintf(w, "|%s|%s|%s|%s|%s|%s|\n", strings.Repeat("-", 42), strings.Repeat("-", 8),
		strings.Repeat("-", 11), strings.Repeat("-", 11), strings.Repeat("-", 11), strings.Repeat("-", 11))
	for _, r := range []struct {
		name string
		s    series
	}{
		{"frame total, main thread (µs)", st.total},
		{"  pull: lock + copy dirty rows (µs)", st.pull},
		{"  convert rows -> instances (µs)", st.convert},
		{"  upload (µs)", st.upload},
		{"  draw calls (µs)", st.draw},
		{"GTK frame before->after-paint (µs)", st.gtkFrame},
		{"GPU time, GL_TIME_ELAPSED (µs)", st.gpu},
		{"PTY output -> render end, oldest (µs)", st.outOldest},
		{"PTY output -> render end, newest (µs)", st.outNewest},
		{"key write -> render end (µs)", st.keyRender},
		{"key write -> presented (µs)", st.keyPresent},
	} {
		s := summarize(r.s)
		if s.n == 0 {
			_, _ = fmt.Fprintf(w, "| %-40s | %6d | %9s | %9s | %9s | %9s |\n", r.name, 0, "n/a", "n/a", "n/a", "n/a")
			continue
		}
		_, _ = fmt.Fprintf(w, "| %-40s | %6d | %9.1f | %9.1f | %9.1f | %9.1f |\n", r.name, s.n, s.p50, s.p99, s.max, s.mean)
	}
	secs := wall.Seconds()
	_, _ = fmt.Fprintf(w, "\n- frames rendered: %d (%.1f/s over %.2fs); snapshots without changes: %d; frame-clock frames: %d\n",
		st.frames, float64(st.frames)/secs, secs, st.skipped, st.clockFrames)
	if n := st.resizes.Load(); n > 0 || st.osc52 > 0 {
		mean := 0.0
		if n > 0 {
			mean = float64(st.resizeNs.Load()) / float64(n) / 1e6
		}
		_, _ = fmt.Fprintf(w, "- Session.Resize (reflow + PTY size, on a worker): %d, mean %.1f ms, max %.1f ms; requests coalesced during a reflow: %d; OSC 52 clipboard writes: %d\n",
			n, mean, float64(st.resizeMaxNs.Load())/1e6, st.resizeDeferred, st.osc52)
	}
	if st.frames > 0 {
		_, _ = fmt.Fprintf(w, "- rows rebuilt per frame: %.1f; bytes uploaded per frame: %.0f; glBufferSubData calls per frame: %.2f\n",
			float64(st.rows)/float64(st.frames), float64(st.bytes)/float64(st.frames), float64(st.uploadCalls)/float64(st.frames))
	}
}
