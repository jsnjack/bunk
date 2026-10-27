// writer.go - the queue between a session and its PTY. Replies to queries
// and user input wait here and one goroutine writes them, so no caller
// waits for the child to read, and nothing writes under the session lock.
package session

import (
	"errors"
	"os"
	"sync"
	"time"
)

// ReplyBytes caps the replies to terminal queries that wait for the child
// to read them. A reply that would pass it is dropped whole.
const ReplyBytes = 64 << 10

// DefaultInputBytes caps the input that waits for the child to read it:
// keys, pastes, mouse and focus reports. Options.InputBytes overrides it.
// An input event that would pass it is refused whole with ErrInputFull.
const DefaultInputBytes = 16 << 20

// ErrInputFull is returned by WriteInput when the child has not read
// enough of the input before it to make room.
var ErrInputFull = errors.New("the program is not reading its input")

const (
	// writeChunk bounds one write, so the counts follow a large paste as
	// the child reads it.
	writeChunk = 64 << 10
	// keepBytes and keepRuns: an empty queue keeps buffers up to these
	// sizes for the next event and releases larger ones (a paste's).
	keepBytes = 4 << 10
	keepRuns  = 64
)

// WriteStats describes a session's PTY writer.
type WriteStats struct {
	PendingReplies int    // reply bytes queued, not yet written
	PendingInput   int    // input bytes queued, not yet written
	DroppedReplies uint64 // replies dropped at ReplyBytes
	RefusedInput   uint64 // input events refused with ErrInputFull
	Writing        bool   // the writer goroutine runs
}

// run is a stretch of queued bytes of one kind.
type run struct {
	n     int // bytes not yet written
	reply bool
}

// ptyWriter queues bytes for the PTY in the order they arrive and writes
// them from one goroutine, which runs while bytes are queued. Each reply
// and input event is queued whole, so the child reads it in one piece.
// Its mutex is taken under the session lock, never the other way round.
type ptyWriter struct {
	mu       sync.Mutex
	f        *os.File // the PTY as of the newest event
	q        []byte   // q[head:] is queued; the goroutine may be writing a prefix
	head     int
	runs     []run // the kinds of q[head:], oldest first from runs[first]
	first    int
	replies  int // queued reply bytes
	input    int // queued input bytes
	inputCap int // Options.InputBytes, else DefaultInputBytes
	dropped  uint64
	refused  uint64
	running  bool   // the goroutine runs; it alone moves head
	closed   bool   // the PTY is closed: nothing is queued any more
	start    func() // drain, bound once: starting it allocates nothing
}

// queue adds data to the queue for f and starts the writer goroutine. A
// reply that does not fit is dropped; input that does not fit is refused
// with ErrInputFull. Nothing is queued for a closed or missing PTY.
func (w *ptyWriter) queue(f *os.File, data []byte, reply bool) error {
	if len(data) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || f == nil {
		return nil
	}
	if reply {
		if w.replies+len(data) > ReplyBytes {
			w.dropped++
			return nil
		}
		w.replies += len(data)
	} else {
		limit := w.inputCap
		if limit <= 0 {
			limit = DefaultInputBytes
		}
		if w.input+len(data) > limit {
			w.refused++
			return ErrInputFull
		}
		w.input += len(data)
	}
	w.f = f
	w.q = append(w.q, data...)
	if last := len(w.runs) - 1; last >= w.first && w.runs[last].reply == reply {
		w.runs[last].n += len(data)
	} else {
		w.runs = append(w.runs, run{n: len(data), reply: reply})
	}
	if !w.running {
		w.running = true
		if w.start == nil {
			w.start = w.drain
		}
		go w.start()
	}
	return nil
}

// drain writes the queue until it is empty, the PTY is closed or a write
// fails, which drops what is left: the child can never read it.
func (w *ptyWriter) drain() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for !w.closed && w.head < len(w.q) {
		// Appends go past len(q), so the chunk stays untouched while
		// the lock is released; only this goroutine moves bytes.
		if w.head >= len(w.q)-w.head {
			w.compact()
		}
		f, chunk := w.f, w.q[w.head:min(len(w.q), w.head+writeChunk)]
		w.mu.Unlock()
		n, err := f.Write(chunk)
		w.mu.Lock()
		if w.closed {
			break
		}
		w.written(n)
		if err != nil {
			L().Debug("session: PTY write failed, dropping queued bytes", "err", err,
				"replies", w.replies, "input", w.input)
			break
		}
	}
	w.reset()
	w.running = false
}

// written advances the queue over n written bytes.
func (w *ptyWriter) written(n int) {
	w.head += n
	for n > 0 {
		r := &w.runs[w.first]
		k := min(n, r.n)
		r.n -= k
		n -= k
		if r.reply {
			w.replies -= k
		} else {
			w.input -= k
		}
		if r.n == 0 {
			w.first++
		}
	}
}

// compact moves the queued bytes to the front of the buffer.
func (w *ptyWriter) compact() {
	n := copy(w.q, w.q[w.head:])
	w.q = w.q[:n]
	w.head = 0
	n = copy(w.runs, w.runs[w.first:])
	w.runs = w.runs[:n]
	w.first = 0
}

// reset empties the queue, keeping a small buffer for the next event.
func (w *ptyWriter) reset() {
	if cap(w.q) > keepBytes {
		w.q = nil
	}
	if cap(w.runs) > keepRuns {
		w.runs = nil
	}
	w.q, w.head = w.q[:0], 0
	w.runs, w.first = w.runs[:0], 0
	w.replies, w.input = 0, 0
}

// close drops the queue and queues nothing more. A write in progress ends
// when the PTY closes; its goroutine then exits.
func (w *ptyWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.reset()
	w.q, w.runs = nil, nil
}

func (w *ptyWriter) stats() WriteStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WriteStats{PendingReplies: w.replies, PendingInput: w.input, DroppedReplies: w.dropped,
		RefusedInput: w.refused, Writing: w.running}
}

// WriteInput queues one input event (a key, a paste, a mouse or focus
// report) for the child and returns at once; the event reaches the PTY
// whole, after everything queued before it. When the child has not read
// enough of the input before it, the event is refused with ErrInputFull
// and nothing of it is written. Input to a closed session is dropped. The
// output that follows input starts a new working stretch (noteOutput), so
// an echoed prompt the user types never counts as the agent's work.
func (s *Session) WriteInput(data []byte) error {
	s.inputs.Add(1)
	return s.out.queue(s.PTY, data, false)
}

// reply queues a reply to a query, in stream order. It may run with the
// session locked, as CaptureAndWrite does.
func (s *Session) reply(data []byte) {
	if err := s.out.queue(s.PTY, data, true); err != nil {
		L().Debug("session: reply not queued", "pane", s.ID, "err", err)
	}
}

// WriteStats returns what the session's PTY writer holds and has dropped.
func (s *Session) WriteStats() WriteStats { return s.out.stats() }

// WaitWritten waits up to timeout until everything queued for the child
// has been written, or dropped by a failed write or Close. It reports
// whether the queue emptied. Tests use it before they read the PTY.
func (s *Session) WaitWritten(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for s.WriteStats().Writing {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}
