package session

// Output soak: one session receives sustained mixed output (colours, wide
// and combining characters, emoji, unique OSC 8 links, OSC 52, SIXEL images,
// titles, notifications, bells, command marks, kitty keyboard pushes, the
// alternate screen and scroll regions) while the test resizes it. Memory
// must stay within the scrollback cap plus a fixed bound, and the idle
// timer and status machinery must not pile up goroutines or hook calls.
// 20 MiB here (3 MiB with -race); 500 MiB with SOAK=1 (make soak).

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"bunk/internal/leakcheck"
	"bunk/internal/vt10x"
)

// soakHelperEnv makes the test binary write that many bytes of soak output
// to stdout and exit, instead of running tests: the soak session's child.
// It starts once it reads a line, so the session knows it as an agent
// before the output starts.
const soakHelperEnv = "BUNK_SESSION_SOAK_BYTES"

func init() {
	v := os.Getenv(soakHelperEnv)
	if v == "" {
		return
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "soak helper:", err)
		os.Exit(2)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		fmt.Fprintln(os.Stderr, "soak helper:", err)
		os.Exit(2)
	}
	if _, err := writeSoakOutput(os.Stdout, n); err != nil {
		fmt.Fprintln(os.Stderr, "soak helper:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// soakStats counts what writeSoakOutput wrote.
type soakStats struct {
	bytes, lines, links, images, clipboard int64
	events                                 int64 // notifications, bells and command marks
	kittyPushes                            int64
}

// writeSoakOutput writes soak lines to out until total bytes are written.
func writeSoakOutput(out io.Writer, total int64) (soakStats, error) {
	w := bufio.NewWriterSize(out, 64<<10)
	var st soakStats
	var line []byte
	for i := int64(0); st.bytes < total; i++ {
		line = appendSoakLine(line[:0], i, &st)
		n, err := w.Write(line)
		st.bytes += int64(n)
		if err != nil {
			return st, fmt.Errorf("write soak output: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return st, fmt.Errorf("write soak output: %w", err)
	}
	return st, nil
}

// sixel is a 12x12 pixel image in two colours.
const sixel = "\x1bPq\"1;1;12;12#0;2;100;0;0#1;2;0;0;100#0!12~-#1!12~\x1b\\"

// appendSoakLine appends line i of the soak output.
func appendSoakLine(b []byte, i int64, st *soakStats) []byte {
	switch i % 500 {
	case 250: // a full-screen app pushes kitty flags on the alternate screen
		b = append(b, "\x1b[?1049h\x1b[2J\x1b[H\x1b[>1u"...)
		for k := range 5 {
			b = fmt.Appendf(b, "\x1b[%d;1H\x1b[7mfull screen %d\x1b[0m", k+1, i)
		}
		b = append(b, "\x1b[<u\x1b[?1049l"...)
	case 400: // a scroll region
		b = append(b, "\x1b[3;10r\x1b[10;1H\n\n\n\x1b[r\x1b[999;1H"...)
	}
	switch i % 200 {
	case 0:
		b = append(b, "\x1b[?2026h"...)
	case 5:
		b = append(b, "\x1b[?2026l"...)
	}
	b = fmt.Appendf(b, "\x1b[1;3%dm%07d\x1b[0m \x1b[38;5;%dm日本語\x1b[0m \x1b[38;2;%d;%d;%dmé 👍🏽 👨‍👩‍👧 🇳🇱 1️⃣\x1b[0m \x1b[4:3m\x1b[58;2;255;0;0mcurly\x1b[0m ",
		i%8, i, i%256, i%256, i*7%256, i*13%256)
	b = fmt.Appendf(b, "\x1b]8;;https://example.com/soak/%d\x1b\\link\x1b]8;;\x1b\\", i)
	st.links++
	if i%8 == 0 {
		b = append(b, "\x1b]52;c;"...)
		b = base64.StdEncoding.AppendEncode(b, fmt.Appendf(nil, "soak %d", i))
		b = append(b, '\a')
		st.clipboard++
	}
	if i%20 == 0 {
		b = fmt.Appendf(b, "\r%3d%%", i%100)
	}
	if i%30 == 0 {
		b = append(b, "\x1b[>1u"...) // never popped
		st.kittyPushes++
	}
	if i%40 == 0 {
		b = fmt.Appendf(b, "\x1b]2;soak title %d\a", i)
	}
	switch i % 100 {
	case 0:
		b = fmt.Appendf(b, "\x1b]9;soak notice %d\a\x1b]133;C\a", i)
		st.events += 2
	case 25:
		b = append(b, '\a')
		st.events++
	case 50:
		b = append(b, "\x1b]133;D;1\a"...)
		st.events++
	}
	if i%1000 == 999 {
		for range 200 {
			b = append(b, "wrap "...)
		}
	}
	b = append(b, "\r\n"...)
	if i%64 == 32 {
		b = append(b, sixel+"\r\n"...)
		st.images++
	}
	st.lines++
	return b
}

// linkTable returns how many OSC 8 URLs the emulator has interned and their
// bytes. IDs are 1..n with no gaps. Requires the session lock.
func linkTable(term vt10x.Terminal) (n, size int) {
	lo, hi := 0, 0xFFFF
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if term.Link(uint16(mid)) != "" {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for id := 1; id <= lo; id++ {
		size += len(term.Link(uint16(id)))
	}
	return lo, size
}

// soakSample is the session's footprint at one moment.
type soakSample struct {
	at         time.Duration
	heap       uint64 // live heap
	links      uint64 // live heap the link table may account for
	goroutines int
}

// Bounds of the output soak.
const (
	soakScrollback = 2000
	soakMaxCols    = 132
	// soakCellBytes bounds one stored cell: 16 bytes plus its share of the
	// row's side table (grapheme text, underline colour, link, image).
	soakCellBytes = 48
	// soakLinkEntry bounds the table overhead of one interned URL.
	soakLinkEntry = 128
	// soakFixed covers the grids, decoders, buffers and image cache.
	soakFixed = 8 << 20
)

// TestOutputSoak streams mixed output through a real PTY into one session
// while resizing it, sampling its footprint. Live heap must stay under the
// scrollback cap plus the link table plus a fixed bound and must not grow
// once the scrollback is full; goroutines stay flat; the kitty stack stays
// capped; StatusChanged runs per status change, not per chunk. After the
// child exits everything is released.
func TestOutputSoak(t *testing.T) {
	if !leakcheck.Supported() {
		t.Skip("needs /proc")
	}
	total := int64(soakCheckBytes)
	if leakcheck.Soak() {
		total = 500 << 20
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := writeSoakOutput(io.Discard, total)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	base := leakcheck.Take()

	s, err := Start(Options{
		Cols: 100, Rows: 30, Scrollback: soakScrollback,
		Args: []string{exe},
		// A -race binary sleeps a second before exiting unless told not to.
		Env:    []string{soakHelperEnv + "=" + strconv.FormatInt(total, 10), "GORACE=" + os.Getenv("GORACE") + " atexit_sleep_ms=0"},
		Agents: []AgentSpec{{Name: "soak", Processes: []string{filepath.Base(exe)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var applied, oscs, statusCalls atomic.Int64
	exited, closed := make(chan struct{}), make(chan struct{})
	s.Run(Hooks{
		Applied:       func(chunk []byte, _, _ bool) { applied.Add(int64(len(chunk))) },
		OSC:           func([]byte) { oscs.Add(1) },
		StatusChanged: func() { statusCalls.Add(1) },
		Exited:        func() { close(exited) },
		Closed:        func() { close(closed) },
	}, done)

	// The child waits for a line: start it once trackFgProcess knows it as
	// an agent, so every chunk runs the idle timer.
	for wait := time.Now().Add(10 * time.Second); s.Status().Agent != "soak"; {
		if time.Now().After(wait) {
			t.Fatal("the soak child was never recognised as an agent")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.WriteInput([]byte("go\r")); err != nil {
		t.Fatal(err)
	}

	resizerDone := make(chan struct{})
	resizes := 0
	go func() {
		defer close(resizerDone)
		sizes := [][2]int{{100, 30}, {80, 24}, {soakMaxCols, 43}, {63, 20}}
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-closed:
				return
			case <-tick.C:
				sz := sizes[resizes%len(sizes)]
				s.Resize(sz[0], sz[1])
				resizes++
			}
		}
	}()

	start := time.Now()
	var samples []soakSample
	maxKitty, maxNotice := 0, 0
	interval := 250 * time.Millisecond
	if leakcheck.Soak() {
		interval = time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	deadline := time.After(10 * time.Minute)
	for running := true; running; {
		select {
		case <-closed:
			running = false
		case <-deadline:
			t.Fatalf("soak did not finish: %d of %d bytes applied", applied.Load(), want.bytes)
		case <-tick.C:
		}
		maxNotice = max(maxNotice, len(s.Status().Notice))
		s.Acknowledge()
		s.Lock()
		maxKitty = max(maxKitty, len(s.KittyStack))
		n, size := linkTable(s.Term)
		s.Unlock()
		samples = append(samples, soakSample{
			at: time.Since(start), heap: leakcheck.LiveHeap(), goroutines: runtime.NumGoroutine(),
			links: uint64(n*soakLinkEntry + size),
		})
	}
	<-resizerDone
	select {
	case <-exited:
	case <-time.After(leakTimeout):
		t.Fatal("soak child never exited")
	}
	elapsed := time.Since(start)
	if !s.Cmd.ProcessState.Success() {
		t.Fatalf("soak child: %v", s.Cmd.ProcessState)
	}

	ring := uint64(soakScrollback * soakMaxCols * soakCellBytes)
	var peak soakSample
	for _, sm := range samples {
		if sm.heap > peak.heap {
			peak = sm
		}
		if limit := base.Heap + ring + sm.links + soakFixed; sm.heap > limit {
			t.Errorf("at %v live heap %s exceeds the bound %s (scrollback %s, links %s, fixed %s)",
				sm.at.Round(time.Millisecond), leakcheck.MiB(sm.heap), leakcheck.MiB(limit),
				leakcheck.MiB(ring), leakcheck.MiB(sm.links), leakcheck.MiB(soakFixed))
		}
		if sm.goroutines > base.Goroutines+8 {
			t.Errorf("at %v %d goroutines, %d before the session", sm.at.Round(time.Millisecond), sm.goroutines, base.Goroutines)
		}
	}
	// Once the scrollback is full (well within the first half), memory
	// other than the link table must stop growing.
	if half := len(samples) / 2; half >= 2 {
		var early, late uint64
		for i, sm := range samples[1:] {
			v := sm.heap - min(sm.heap, sm.links)
			if i+1 < half {
				early = max(early, v)
			} else {
				late = max(late, v)
			}
		}
		if late > early+3<<20 {
			t.Errorf("live heap without the link table grew from %s in the first half to %s in the second",
				leakcheck.MiB(early), leakcheck.MiB(late))
		}
	}
	if maxKitty > 32 {
		t.Errorf("kitty keyboard stack reached %d entries after %d pushes", maxKitty, want.kittyPushes)
	}
	if maxNotice > noticeLimit*4 {
		t.Errorf("notice grew to %d bytes", maxNotice)
	}
	// Each status event, acknowledgement and process poll may announce one
	// change; per-chunk announcements would be far more.
	if limit := want.events + 2*int64(len(samples)) + 2*int64(elapsed/time.Second) + 10; statusCalls.Load() > limit {
		t.Errorf("StatusChanged ran %d times for %d status events (limit %d)", statusCalls.Load(), want.events, limit)
	}
	t.Logf("%s in %v (%.1f MiB/s): %d lines, %d unique links, %d images, %d OSC forwarded, %d resizes, %d status calls; "+
		"peak live heap %s at %v (baseline %s, links %s), %d samples",
		leakcheck.MiB(uint64(want.bytes)), elapsed.Round(time.Millisecond), float64(want.bytes)/(1<<20)/elapsed.Seconds(),
		want.lines, want.links, want.images, oscs.Load(), resizes, statusCalls.Load(),
		leakcheck.MiB(peak.heap), peak.at.Round(time.Millisecond), leakcheck.MiB(base.Heap), leakcheck.MiB(peak.links), len(samples))

	s = nil // release the session for the final check
	leakcheck.Check(t, "after the soak", base, leakcheck.Limits{FDs: 1, Goroutines: 1, Heap: 2 << 20, Settle: 5 * time.Second})
}
