package session

// Tests of the PTY writer (writer.go): a child that never reads its input
// must not stall ReadLoop, hold the session lock or block a caller of
// WriteInput, and the queue must stay bounded and in order.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bunk/internal/leakcheck"
	"bunk/internal/ptychild"
	"bunk/internal/vt10x"
)

// TestNeverReadsChild is the child process of the never-reading tests
// (internal/ptychild), not a test of its own.
func TestNeverReadsChild(t *testing.T) {
	if !ptychild.Requested() {
		t.Skip("child process of the PTY writer tests")
	}
	os.Exit(ptychild.Main())
}

// neverReader is a session whose child floods queries and reads nothing
// until the test creates goFile; then it copies its input to outFile.
type neverReader struct {
	*Session
	goFile, outFile string
	closed          chan struct{}
}

func startNeverReader(t *testing.T, queries int, o Options) *neverReader {
	t.Helper()
	dir := t.TempDir()
	r := &neverReader{goFile: filepath.Join(dir, "go"), outFile: filepath.Join(dir, "out"), closed: make(chan struct{})}
	o.Cols, o.Rows, o.Scrollback = 80, 24, 100
	o.Args = []string{os.Args[0], "-test.run=^TestNeverReadsChild$"}
	o.Env = ptychild.Environ(queries, r.goFile, r.outFile)
	s, err := Start(o)
	if err != nil {
		t.Fatal(err)
	}
	r.Session = s
	done := make(chan struct{})
	s.Run(Hooks{Closed: func() { close(r.closed) }}, done)
	t.Cleanup(func() {
		close(done)
		s.Close()
		select {
		case <-r.closed:
		case <-time.After(10 * time.Second):
			t.Error("ReadLoop did not end after Close")
		}
	})
	return r
}

// read lets the child read its input and waits until outFile holds n
// bytes.
func (r *neverReader) read(t *testing.T, n int) []byte {
	t.Helper()
	if err := os.WriteFile(r.goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		got, _ = os.ReadFile(r.outFile)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child read %d bytes, want %d (writer %+v)", len(got), n, r.WriteStats())
		}
	}
}

// within runs f on another goroutine and returns how long it took. It
// fails the test when f has not returned after limit: f is stuck, and it
// stays stuck until the test's cleanup closes the session.
func within(t *testing.T, limit time.Duration, what string, f func()) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("%s did not return within %v", what, limit)
		return 0
	}
}

// screenHas reports whether the session's screen shows text.
func screenHas(s *Session, text string) bool {
	s.Lock()
	defer s.Unlock()
	cols, rows := s.Term.Size()
	for y := range rows {
		var b strings.Builder
		for _, g := range CaptureRow(s.Term, y, cols) {
			if g.Char != 0 {
				b.WriteRune(g.Char)
			}
		}
		if strings.Contains(b.String(), text) {
			return true
		}
	}
	return false
}

// lockBound is how long the session lock or Status may take while a child
// floods queries: ReadLoop holds the lock while it parses one chunk.
const lockBound = 50 * time.Millisecond * raceSlack

// TestRepliesToNeverReadingChild: a raw-mode child floods 30000 queries
// and never reads the replies. ReadLoop keeps applying its output, the
// lock and Status stay available, and the queued replies stay under
// ReplyBytes. Once the child reads, it gets the replies in stream order,
// without the counted drops, then the input queued after them.
func TestRepliesToNeverReadingChild(t *testing.T) {
	const queries = 30000
	r := startNeverReader(t, queries, Options{})
	var worstLock, worstStatus time.Duration
	samples := 0
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		// IsDead takes the session lock.
		worstLock = max(worstLock, within(t, 5*time.Second, "the session lock", func() { r.IsDead() }))
		worstStatus = max(worstStatus, within(t, 5*time.Second, "Status", func() { r.Status() }))
		samples++
		if ws := r.WriteStats(); ws.PendingReplies > ReplyBytes {
			t.Fatalf("%d reply bytes queued, over ReplyBytes (%d)", ws.PendingReplies, ReplyBytes)
		}
		var flooded bool
		within(t, 5*time.Second, "screenHas", func() { flooded = screenHas(r.Session, ptychild.FloodDone) })
		if flooded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ReadLoop stalled: %q never appeared (writer %+v)", ptychild.FloodDone, r.WriteStats())
		}
	}
	t.Logf("%d samples during the flood: Lock at most %v, Status at most %v", samples, worstLock, worstStatus)
	if worstLock > lockBound || worstStatus > lockBound {
		t.Errorf("Lock took up to %v and Status up to %v during the flood, want at most %v", worstLock, worstStatus, lockBound)
	}

	ws := r.WriteStats()
	t.Logf("after the flood: %+v", ws)
	if ws.DroppedReplies == 0 || !ws.Writing || ws.PendingReplies == 0 || ws.PendingReplies > ReplyBytes {
		t.Fatalf("writer %+v: want a full reply queue, drops, and a write waiting for the child", ws)
	}
	// Input queues behind the replies and returns at once.
	if d := within(t, 5*time.Second, "WriteInput", func() {
		if err := r.WriteInput([]byte("END")); err != nil {
			t.Error(err)
		}
	}); d > lockBound {
		t.Errorf("WriteInput took %v", d)
	}

	want := int(queries-ws.DroppedReplies) * len("\x1b[?1;0$y") // a lower bound
	got := r.read(t, want)
	for !bytes.HasSuffix(got, []byte("END")) {
		got = r.read(t, len(got)+1)
	}
	modes, rest := ptychild.ParseReplies(got)
	if string(rest) != "END" {
		t.Errorf("after %d replies the child read %q, want END", len(modes), rest[:min(len(rest), 40)])
	}
	if len(modes) != queries-int(ws.DroppedReplies) {
		t.Errorf("%d replies arrived, want %d queries minus %d drops", len(modes), queries, ws.DroppedReplies)
	}
	for i := 1; i < len(modes); i++ {
		if modes[i] <= modes[i-1] || modes[i] > queries {
			t.Fatalf("reply %d answers query %d after query %d: out of stream order", i, modes[i], modes[i-1])
		}
	}
	if len(modes) > 0 && modes[0] != 1 {
		t.Errorf("the first reply answers query %d, want 1", modes[0])
	}
}

// TestInputToNeverReadingChild: WriteInput returns at once while the child
// reads nothing; a paste that would pass the cap is refused whole and
// memory stays bounded; once the child reads, the accepted input arrives
// intact and in order, an 8 MiB paste included.
func TestInputToNeverReadingChild(t *testing.T) {
	r := startNeverReader(t, 0, Options{})
	waitScreenText(t, r.Session, ptychild.FloodDone)
	var want bytes.Buffer
	var worst time.Duration
	for i := range 20000 { // about 250 KiB: more than the kernel buffers
		key := fmt.Appendf(nil, "<key %d>", i)
		worst = max(worst, within(t, 5*time.Second, "WriteInput", func() {
			if err := r.WriteInput(key); err != nil {
				t.Error(err)
			}
		}))
		want.Write(key)
	}
	if worst > lockBound {
		t.Errorf("WriteInput took up to %v with a child that does not read", worst)
	}
	// The writer fills the kernel's buffers, then waits for the child.
	var ws WriteStats
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		prev := ws
		ws = r.WriteStats()
		if ws.PendingInput > 0 && ws == prev {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the writer did not settle: %+v", ws)
		}
	}
	t.Logf("with the kernel's buffers full: %+v (%d bytes written)", ws, want.Len()-ws.PendingInput)

	paste := bytes.Repeat([]byte("0123456789abcdef"), (8<<20)/16)
	if err := r.WriteInput(paste); err != nil {
		t.Fatalf("an 8 MiB paste: %v", err)
	}
	want.Write(paste)

	over := bytes.Repeat([]byte("x"), DefaultInputBytes-len(paste))
	before := leakcheck.LiveHeap()
	for i := range 10 {
		if err := r.WriteInput(over); !errors.Is(err, ErrInputFull) {
			t.Fatalf("paste %d past the cap: %v, want ErrInputFull", i, err)
		}
	}
	grown := int64(leakcheck.LiveHeap()) - int64(before)
	ws = r.WriteStats()
	if ws.RefusedInput != 10 || ws.PendingInput > DefaultInputBytes || ws.PendingInput < len(paste) {
		t.Errorf("after 10 refused pastes: %+v", ws)
	}
	if grown > 1<<20 {
		t.Errorf("10 refused %d MiB pastes grew the live heap by %d bytes", len(over)>>20, grown)
	}

	tail := []byte("<after the refusals>END")
	if err := r.WriteInput(tail); err != nil {
		t.Fatal(err)
	}
	want.Write(tail)
	got := r.read(t, want.Len())
	if !bytes.Equal(got, want.Bytes()) {
		i := 0
		for i < min(len(got), want.Len()) && got[i] == want.Bytes()[i] {
			i++
		}
		t.Fatalf("the child read %d bytes, want %d; first difference at %d: %q, want %q", len(got), want.Len(), i,
			got[i:min(len(got), i+40)], want.Bytes()[i:min(want.Len(), i+40)])
	}
	if !r.WaitWritten(5 * time.Second) {
		t.Fatalf("the writer still runs: %+v", r.WriteStats())
	}
	if ws := r.WriteStats(); ws.PendingInput != 0 || ws.PendingReplies != 0 {
		t.Errorf("after the child read everything: %+v", ws)
	}
}

// TestCloseStopsBlockedWriter: Close ends a write blocked on a child that
// never reads, drops the queue and leaves no goroutine, descriptor or
// queued memory behind.
func TestCloseStopsBlockedWriter(t *testing.T) {
	if !leakcheck.Supported() {
		t.Skip("needs /proc")
	}
	base := leakcheck.Take()
	func() {
		r := startNeverReader(t, 20000, Options{})
		waitScreenText(t, r.Session, ptychild.FloodDone)
		if err := r.WriteInput(bytes.Repeat([]byte("y"), 4<<20)); err != nil {
			t.Fatal(err)
		}
		if ws := r.WriteStats(); !ws.Writing || ws.PendingInput == 0 || ws.DroppedReplies == 0 {
			t.Fatalf("writer %+v: want a write blocked on the child", ws)
		}
		r.Close()
		if !r.WaitWritten(5 * time.Second) {
			t.Fatalf("Close did not stop the writer: %+v", r.WriteStats())
		}
		if ws := r.WriteStats(); ws.PendingInput != 0 || ws.PendingReplies != 0 {
			t.Errorf("after Close: %+v", ws)
		}
		if err := r.WriteInput([]byte("late")); err != nil || r.WriteStats().Writing {
			t.Errorf("input after Close: %v, writer %+v", err, r.WriteStats())
		}
		select {
		case <-r.closed:
		case <-time.After(10 * time.Second):
			t.Fatal("ReadLoop did not end")
		}
	}()
	leakcheck.Check(t, "after Close", base, leakcheck.Limits{FDs: 1, Goroutines: 1, Heap: 2 << 20, Settle: 5 * time.Second})
}

// TestWriteInputAllocs: a key costs no allocation once the queue and the
// writer goroutine have been used.
func TestWriteInputAllocs(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			if _, err := pr.Read(buf); err != nil {
				return
			}
		}
	}()
	s := newTestSession(10, 2)
	s.PTY = pw
	key := []byte("x")
	allocs := testing.AllocsPerRun(1000, func() {
		if err := s.WriteInput(key); err != nil {
			t.Fatal(err)
		}
		for s.WriteStats().Writing {
			runtime.Gosched()
		}
	})
	closeWriter(t, s, pw)
	<-drained
	if err := pr.Close(); err != nil {
		t.Error(err)
	}
	if allocs != 0 {
		t.Errorf("WriteInput of one key: %v allocations", allocs)
	}
}

// writerEvent is one reply or input event of an order test: "<", the kind
// (R or I), the event's number, ":", fill bytes and ">".
func writerEvent(reply bool, n, size int) []byte {
	kind := byte('I')
	if reply {
		kind = 'R'
	}
	b := fmt.Appendf(nil, "<%c%d:", kind, n)
	fill := byte('a' + n%26)
	for len(b) < size-1 {
		b = append(b, fill)
	}
	return append(b, '>')
}

// parseWriterEvents splits what the PTY received into events. It fails
// the test when an event is broken: cut short or with bytes of another
// event inside.
func parseWriterEvents(t *testing.T, got []byte) [][]byte {
	t.Helper()
	var events [][]byte
	for len(got) > 0 {
		end := bytes.IndexByte(got, '>')
		if got[0] != '<' || end < 0 {
			t.Fatalf("event %d starts with %q, not a whole event", len(events), got[:min(len(got), 30)])
		}
		ev := got[:end+1]
		var kind byte
		var n int
		if _, err := fmt.Sscanf(string(ev), "<%c%d:", &kind, &n); err != nil || (kind != 'R' && kind != 'I') {
			t.Fatalf("event %d %q: no header", len(events), ev[:min(len(ev), 30)])
		}
		if !bytes.Equal(ev, writerEvent(kind == 'R', n, len(ev))) {
			t.Fatalf("event %d (%c%d, %d bytes) holds bytes of another event", len(events), kind, n, len(ev))
		}
		events = append(events, ev)
		got = got[end+1:]
	}
	return events
}

// slowPipe returns a pipe that holds only a page and a reader that takes
// what arrives in small reads with pauses, so writes block and are split.
func slowPipe(t *testing.T, pace byte) (w *os.File, received func() []byte) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	setPipeSize(t, pw, 4096)
	var mu sync.Mutex
	var got bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16+int(pace)*64)
		for i := 0; ; i++ {
			n, err := pr.Read(buf)
			mu.Lock()
			got.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
			if pace&1 != 0 && i%64 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	return pw, func() []byte {
		<-done
		if err := pr.Close(); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return got.Bytes()
	}
}

// writerOrder queues the events ops describes through one session and
// checks that the PTY received the accepted ones whole and in order. Each
// op byte: bit 0 a reply (else input), bits 1-3 the size class, bit 4 a
// reply queued with the session locked, as CaptureAndWrite does.
func writerOrder(t *testing.T, pace byte, ops []byte) {
	sizes := [8]int{8, 16, 100, 1000, 4096, 9000, 30000, 70000}
	pw, received := slowPipe(t, pace)
	s := newTestSession(10, 2)
	s.PTY = pw
	s.out.inputCap = 200000
	var want bytes.Buffer
	for i, op := range ops {
		reply := op&1 != 0
		ev := writerEvent(reply, i, sizes[op>>1&7])
		if reply {
			dropped := s.WriteStats().DroppedReplies
			if op&16 != 0 {
				s.Lock()
				s.reply(ev)
				s.Unlock()
			} else {
				s.reply(ev)
			}
			if s.WriteStats().DroppedReplies == dropped {
				want.Write(ev)
			}
		} else if err := s.WriteInput(ev); err == nil {
			want.Write(ev)
		} else if !errors.Is(err, ErrInputFull) {
			t.Fatal(err)
		}
		if ws := s.WriteStats(); ws.PendingReplies > ReplyBytes || ws.PendingInput > s.out.inputCap {
			t.Fatalf("op %d: queue over its caps: %+v", i, ws)
		}
	}
	closeWriter(t, s, pw)
	got := received()
	parseWriterEvents(t, got)
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("received %d bytes, want the %d bytes of the accepted events in order", len(got), want.Len())
	}
}

// FuzzPTYWriterOrder: replies and input events, of sizes from a few bytes
// to past the pipe's and the reply queue's capacity, reach a slow reader
// whole and in the order they were queued; only dropped replies and
// refused input are missing.
func FuzzPTYWriterOrder(f *testing.F) {
	f.Add(byte(0), []byte{0, 1, 2, 3})
	f.Add(byte(3), []byte{1, 1, 1, 0, 0, 0, 17, 17, 16})
	f.Add(byte(1), []byte{15, 15, 14, 1, 13, 0, 12, 3, 11, 5})
	f.Add(byte(2), []byte{13, 13, 13, 13, 13, 13, 1, 0, 1, 0, 1, 0})
	f.Add(byte(5), []byte{14, 14, 14, 14, 1, 31, 30, 3, 2})
	f.Fuzz(func(t *testing.T, pace byte, ops []byte) {
		if len(ops) > 64 {
			ops = ops[:64]
		}
		writerOrder(t, pace, ops)
	})
}

// TestPTYWriterConcurrentOrder: a reader goroutine queuing replies with
// the session locked and a front end queuing input at the same time; each
// event arrives whole, and each side's events in the order it queued them.
func TestPTYWriterConcurrentOrder(t *testing.T) {
	pw, received := slowPipe(t, 3)
	s := newTestSession(10, 2)
	s.PTY = pw
	const each = 400
	var accepted atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range each {
			dropped := s.WriteStats().DroppedReplies
			s.Lock()
			s.reply(writerEvent(true, i, 8+i%7*300))
			s.Unlock()
			if s.WriteStats().DroppedReplies == dropped {
				accepted.Add(1)
			}
		}
	})
	wg.Go(func() {
		for i := range each {
			if err := s.WriteInput(writerEvent(false, i, 8+i%5*900)); err != nil {
				t.Error(err)
				return
			}
			accepted.Add(1)
		}
	})
	wg.Wait()
	closeWriter(t, s, pw)
	events := parseWriterEvents(t, received())
	if int64(len(events)) != accepted.Load() {
		t.Errorf("%d events arrived, %d were accepted", len(events), accepted.Load())
	}
	last := map[byte]int{'R': -1, 'I': -1}
	for _, ev := range events {
		var kind byte
		var n int
		if _, err := fmt.Sscanf(string(ev), "<%c%d:", &kind, &n); err != nil {
			t.Fatal(err)
		}
		if n <= last[kind] {
			t.Fatalf("%c%d arrived after %c%d", kind, n, kind, last[kind])
		}
		last[kind] = n
	}
}

// TestGraphicsReplyQueued: the emulator's graphics acknowledgements are
// replies too: queued in stream order with the CSI replies, not written
// under the lock.
func TestGraphicsReplyQueued(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	s := newTestSession(20, 5)
	s.PTY = pw
	s.Term = vt10x.New(vt10x.WithSize(20, 5), vt10x.WithGraphicsReply(s.reply))
	s.Lock()
	s.CaptureAndWrite([]byte("\x1b[18t\x1b_Ga=q,i=7,f=24,s=1,v=1;/wAA\x1b\\\x1b[5n"))
	s.Unlock()
	closeWriter(t, s, pw)
	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[8;5;20t\x1b_Gi=7;OK\x1b\\\x1b[0n"; string(got) != want {
		t.Errorf("replies %q, want %q", got, want)
	}
}
