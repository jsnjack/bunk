// osc.go - OSC (Operating System Command) sequence pre-scanner.
//
// vt10x handles OSC 0/1/2 (title), 4 (colour), and 104 (colour reset).
// OSC 10/11/12 dynamic-colour queries are answered manually in pane.go so the
// replies can be gated on bunk's current mode and theme state.
// Everything else is silently dropped.
//
// Modern terminal emulators (foot, kitty, iTerm2, Ptyxis/VTE, …) understand
// these sequences, which must reach the host terminal directly:
//
//	OSC 7   - shell CWD notification; lets the terminal open new tabs in the
//	           same directory ("open here"), update tab titles with the path, etc.
//	OSC 8   - inline hyperlinks; terminals can open URLs on Ctrl+click.
//	OSC 52  - clipboard read/write; lets programs access the system clipboard
//	           without needing xclip/xdotool.
//	OSC 133 - shell prompt markers; terminals use these for jump-to-prompt,
//	           command timing, semantic prompt integration, etc.
//
// The oscScanner runs over each raw PTY chunk BEFORE it is fed to vt10x.
// When it finds a complete OSC in the forward-set it appends a copy to the
// App's oscBuffer; the render loop flushes that buffer to os.Stdout just
// before tcell.Show().  Writing from the single render goroutine serialises
// our writes with tcell's own writes, preventing interleaved escapes.
package main

import (
	"io"
	"sync"
)

// ---------------------------------------------------------------------------
// oscBuffer: serialised host-OSC queue between PTY readers and the renderer.
// ---------------------------------------------------------------------------

// oscBufferMax caps the bytes accumulated in an oscBuffer between flushes.
// 1 MiB easily holds a directory listing of tens of thousands of files'
// worth of OSC 8 hyperlinks; if the renderer is genuinely stuck for that
// long, dropping further sequences is preferable to unbounded memory growth.
const oscBufferMax = 1 << 20

// oscBuffer accumulates OSC byte sequences from any number of pane reader
// goroutines and is flushed to a writer (os.Stdout) by the render loop.
//
// It replaced an earlier bounded chan<- []byte approach. With the channel,
// bursty output like `ls --hyperlink=auto` on a large directory could fill
// the channel before the render loop drained it, causing dispatch() to drop
// individual OSC sequences. Drops were interleaved at random — most damaging
// when an OSC 8 close (\x1b]8;;\x1b\\) was dropped while the matching open
// got through, leaving every subsequent character (including the next
// shell prompt) styled as part of the unclosed hyperlink.
//
// A mutex-guarded byte buffer eliminates per-sequence drops at the cost of
// holding a lock briefly per append. The lock is uncontended in steady
// state; bursty appends are still cheap because the buffer's underlying
// slice grows amortised.
type oscBuffer struct {
	mu  sync.Mutex
	buf []byte
}

// newOSCBuffer returns an empty oscBuffer ready for use.
func newOSCBuffer() *oscBuffer { return &oscBuffer{} }

// append copies seq into the buffer. If the buffer is at the safety cap the
// sequence is dropped (rare; only happens if the renderer is wedged).
func (b *oscBuffer) append(seq []byte) {
	b.mu.Lock()
	if len(b.buf)+len(seq) <= oscBufferMax {
		b.buf = append(b.buf, seq...)
	} else {
		L.Debug("osc: buffer at cap, dropping sequence", "len", len(seq), "buffered", len(b.buf))
	}
	b.mu.Unlock()
}

// flush writes the accumulated bytes to w and resets the buffer.
// Holding the lock across the write is safe because writes to os.Stdout are
// fast and the alternative (copying first, releasing, then writing) just
// adds an allocation without changing user-visible latency.
func (b *oscBuffer) flush(w io.Writer) {
	b.mu.Lock()
	if len(b.buf) > 0 {
		w.Write(b.buf) //nolint:errcheck
		b.buf = b.buf[:0]
	}
	b.mu.Unlock()
}
