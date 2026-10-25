package session

// Leak tests: start and end many sessions the ways daily use ends them, and
// check that the process returns to where it started: open descriptors,
// goroutines, child processes and live heap (internal/leakcheck).
// SOAK=1 (make soak) runs the long versions.

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"bunk/internal/leakcheck"
)

// leakKind is one way a session ends in TestSessionLeaks.
type leakKind struct {
	name string
	args []string
	// closeAfter > 0: the test calls Close once that many bytes of output
	// (and ready, when set) arrived. 0: the session ends by itself.
	closeAfter int
	ready      string
	// closeOnExit: the test calls Close after the child exits, as front
	// ends do when the child left a process that holds the PTY.
	closeOnExit bool
	// killsGroup: the child ignores SIGHUP, so Close must kill its process
	// group; the test checks that the group is gone.
	killsGroup bool
	// blocksWriter: the child floods queries and never reads; the test
	// queues input too and closes the session while the PTY writer waits
	// for the child.
	blocksWriter bool
}

func shArgs(script string) []string { return []string{"/bin/sh", "-c", script} }

var leakKinds = []leakKind{
	{name: "exits by itself", args: shArgs(`printf 'bye\n'; exit 3`)},
	{name: "busy yes", args: []string{"yes", "bunk leak test"}, closeAfter: 64 << 10},
	{name: "busy urandom", args: shArgs(`cat /dev/urandom | head -c 100000000`), closeAfter: 64 << 10},
	{name: "closed mid-output", args: shArgs(`seq 1 100000000`), closeAfter: 1},
	{name: "ignores SIGHUP", args: shArgs(`trap '' HUP; echo ready; while :; do sleep 1; done`),
		closeAfter: 1, ready: "ready", killsGroup: true},
	{name: "busy and ignores SIGHUP", args: shArgs(`trap '' HUP; echo ready; yes > /dev/null`),
		closeAfter: 1, ready: "ready", killsGroup: true},
	{name: "child holds the pty", args: shArgs(holdPTY), closeOnExit: true},
	{name: "never reads, writer blocked", args: shArgs(`stty raw -echo; echo ready; ` +
		`yes "$(printf '\033[5n')" | head -c 300000; while :; do sleep 1; done`),
		closeAfter: 1, ready: "ready", blocksWriter: true},
}

// holdPTY exits at once and leaves a process that ignores SIGHUP and keeps
// the PTY's slave side open for longer than any test waits. The shell
// ignores SIGHUP before it forks, so the kernel's SIGHUP at its exit
// cannot reach the child first.
const holdPTY = `trap '' HUP; sleep 120 & echo ready; exit 0`

// leakRun is one session of a leak batch.
type leakRun struct {
	kind    leakKind
	s       *Session
	out     atomic.Int64
	ready   atomic.Bool
	head    []byte        // the first output, for ready; reader goroutine only
	output  chan struct{} // signalled after each chunk
	exited  chan struct{}
	closed  chan struct{}
	closing time.Time // when finish called Close
}

func startLeakRun(t *testing.T, id int, k leakKind, tm Timing, done <-chan struct{}) *leakRun {
	t.Helper()
	s, err := Start(Options{ID: id, Cols: 80, Rows: 24, Scrollback: 200, Args: k.args, Timing: tm})
	if err != nil {
		t.Fatalf("%s: %v", k.name, err)
	}
	r := &leakRun{kind: k, s: s, output: make(chan struct{}, 1), exited: make(chan struct{}), closed: make(chan struct{})}
	s.Run(Hooks{
		Output: func(chunk []byte, _, _ bool) {
			r.out.Add(int64(len(chunk)))
			if k.ready != "" && !r.ready.Load() && len(r.head) < 4096 {
				r.head = append(r.head, chunk...)
				r.ready.Store(bytes.Contains(r.head, []byte(k.ready)))
			}
			select {
			case r.output <- struct{}{}:
			default:
			}
		},
		Exited: func() { close(r.exited) },
		Closed: func() { close(r.closed) },
	}, done)
	return r
}

// leakTimeout bounds every wait of a leak run; a healthy one needs a
// fraction of it.
const leakTimeout = 20 * time.Second

// finish closes the session the way its kind says and waits until it has
// exited and its output is closed. It runs on its own goroutine.
func (r *leakRun) finish(t *testing.T) {
	timer := time.NewTimer(leakTimeout)
	defer timer.Stop()
	deadline := timer.C
	if r.kind.closeAfter > 0 {
		for r.out.Load() < int64(r.kind.closeAfter) || r.kind.ready != "" && !r.ready.Load() {
			select {
			case <-r.output:
			case <-r.closed:
				t.Errorf("%s: output ended after %d bytes, before the test closed it", r.kind.name, r.out.Load())
				r.s.Close()
				return
			case <-deadline:
				t.Errorf("%s: only %d bytes of output", r.kind.name, r.out.Load())
				r.s.Close()
				return
			}
		}
		if r.kind.blocksWriter {
			r.blockWriter(t, deadline)
		}
		r.closing = time.Now()
		r.s.Close()
	}
	if r.kind.closeOnExit {
		select {
		case <-r.exited:
		case <-deadline:
			t.Errorf("%s: child never exited", r.kind.name)
		}
		r.closing = time.Now()
		r.s.Close()
	}
	for _, step := range []struct {
		name string
		ch   chan struct{}
	}{{"exited", r.exited}, {"closed", r.closed}} {
		select {
		case <-step.ch:
		case <-deadline:
			t.Errorf("%s: session never %s (output %d bytes)", r.kind.name, step.name, r.out.Load())
			return
		}
	}
	if r.kind.killsGroup {
		waitGroupGone(t, r.kind.name, r.s.Cmd.Process.Pid)
	}
	if r.kind.closeOnExit {
		// The process left holding the PTY is no child of the test any
		// more; end it so it does not outlive the test.
		if err := syscall.Kill(-r.s.Cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("%s: kill the process holding the PTY: %v", r.kind.name, err)
		}
	}
}

// blockWriter waits until the child's unread replies fill the reply queue,
// then queues input behind them.
func (r *leakRun) blockWriter(t *testing.T, deadline <-chan time.Time) {
	for r.s.WriteStats().DroppedReplies == 0 {
		select {
		case <-r.output:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Errorf("%s: no reply dropped: %+v", r.kind.name, r.s.WriteStats())
			return
		}
	}
	if err := r.s.WriteInput(bytes.Repeat([]byte("k"), 64<<10)); err != nil {
		t.Errorf("%s: %v", r.kind.name, err)
	}
	if ws := r.s.WriteStats(); !ws.Writing || ws.PendingInput < 64<<10 || ws.PendingReplies == 0 {
		t.Errorf("%s: want a blocked writer before Close: %+v", r.kind.name, ws)
	}
}

// waitGroupGone waits until no process is left in process group pgid.
func waitGroupGone(t *testing.T, what string, pgid int) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s: process group %d still has processes (kill: %v)", what, pgid, err)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runLeakBatch starts n sessions, cycling through leakKinds, and ends them
// all. Each session's ending starts as soon as it runs, so busy ones stop
// at their limit instead of loading the machine while later ones start.
// The sessions use fastTiming: a child that ignores SIGHUP is killed after
// its HangupGrace.
func runLeakBatch(t *testing.T, first, n int, done <-chan struct{}) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		r := startLeakRun(t, first+i, leakKinds[(first+i)%len(leakKinds)], fastTiming, done)
		wg.Go(func() { r.finish(t) })
	}
	wg.Wait()
}

// leakLimits: what a batch may leave behind once it settled. A session's
// goroutines end when its child exits and its PTY closes; a child that
// ignores SIGHUP exits Timing.HangupGrace after Close, at most HangupGrace.
var leakLimits = leakcheck.Limits{FDs: 2, Goroutines: 2, Heap: 4 << 20, Settle: 4*time.Second + HangupGrace}

// TestSessionLeaks starts and ends 300 sessions (2000 with SOAK=1) in
// batches: shells that exit by themselves, busy ones closed mid-output,
// children that ignore SIGHUP and children that leave a process holding the
// PTY. After each batch the open descriptors, goroutines, child processes
// and live heap must be back at the baseline, and the heap must not grow
// from batch to batch.
func TestSessionLeaks(t *testing.T) {
	if !leakcheck.Supported() {
		t.Skip("needs /proc")
	}
	total, batches := 300, 2
	if leakcheck.Soak() {
		total, batches = 2000, 10
	}
	done := make(chan struct{})
	defer close(done)

	// Warm up: the first PTY creates the runtime's poller descriptors.
	runLeakBatch(t, 0, 1, done)
	base := leakcheck.Check(t, "warm-up", leakcheck.Take(), leakLimits)
	if t.Failed() {
		t.FailNow()
	}
	var first leakcheck.Snapshot
	start := time.Now()
	for b := range batches {
		n := total / batches
		runLeakBatch(t, 1+b*n, n, done)
		snap := leakcheck.Check(t, fmt.Sprintf("batch %d", b+1), base, leakLimits)
		t.Logf("batch %d: %d sessions, %d fds, %d goroutines, heap %s (baseline %d, %d, %s), %v",
			b+1, n, len(snap.FDs), snap.Goroutines, leakcheck.MiB(snap.Heap),
			len(base.FDs), base.Goroutines, leakcheck.MiB(base.Heap), time.Since(start).Round(time.Millisecond))
		if t.Failed() {
			t.FailNow()
		}
		if b == 0 {
			first = snap
			continue
		}
		// Per-session leaks show as growth from one batch to the next.
		if limit := first.Heap + 2<<20; snap.Heap > limit {
			t.Errorf("batch %d: live heap %s, %s after batch 1: it grows with every batch",
				b+1, leakcheck.MiB(snap.Heap), leakcheck.MiB(first.Heap))
		}
	}
}

// TestCloseKillsChildIgnoringSIGHUP: a child that handles SIGHUP exits from
// it; one that ignores it is killed with its process group after
// Timing.HangupGrace, so neither keeps its process or goroutines.
func TestCloseKillsChildIgnoringSIGHUP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		script string
		signal syscall.Signal
	}{
		{"handles SIGHUP", `echo ready; while :; do sleep 1; done`, syscall.SIGHUP},
		{"ignores SIGHUP", `trap '' HUP; echo ready; while :; do sleep 1; done`, syscall.SIGKILL},
	}
	// A child that handles SIGHUP must end within the grace on a loaded
	// machine too.
	fast := fastTiming
	fast.HangupGrace = 500 * time.Millisecond
	for _, tc := range timings(fast) {
		grace := tc.timing.hangupGrace()
		for _, tt := range tests {
			t.Run(tc.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				done := make(chan struct{})
				defer close(done)
				k := leakKind{name: tt.name, args: shArgs(tt.script), closeAfter: 1, ready: "ready", killsGroup: true}
				r := startLeakRun(t, 1, k, tc.timing, done)
				r.finish(t)
				if t.Failed() {
					return
				}
				ws, ok := r.s.Cmd.ProcessState.Sys().(syscall.WaitStatus)
				if !ok || !ws.Signaled() || ws.Signal() != tt.signal {
					t.Fatalf("child ended with %v, want signal %v", r.s.Cmd.ProcessState, tt.signal)
				}
				took := time.Since(r.closing)
				if tt.signal == syscall.SIGHUP && took >= grace {
					t.Errorf("a child that handles SIGHUP took %v to end", took)
				}
				if tt.signal == syscall.SIGKILL && took < grace {
					t.Errorf("killed %v after Close, before the grace of %v", took, grace)
				}
				if tt.signal == syscall.SIGKILL && tc.timing.HangupGrace > 0 && took >= HangupGrace {
					t.Errorf("killed %v after Close: Timing.HangupGrace (%v) was not used", took, tc.timing.HangupGrace)
				}
			})
		}
	}
}

// TestCloseReleasesHeldPTY: when the child exits but leaves a process that
// holds the PTY open, Close still closes the master, ends ReadLoop and
// frees the descriptor, after resizes and SetCellPixels too (their ioctls
// must not put the master back into blocking mode, where Close waits for
// the Read that only that process can end).
func TestCloseReleasesHeldPTY(t *testing.T) {
	if !leakcheck.Supported() {
		t.Skip("needs /proc")
	}
	done := make(chan struct{})
	defer close(done)
	before := leakcheck.FDs()
	r := startLeakRun(t, 1, leakKind{name: "holds the pty", args: shArgs(holdPTY)}, fastTiming, done)
	defer func() {
		if err := syscall.Kill(-r.s.Cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("kill the process holding the PTY: %v", err)
		}
	}()
	select {
	case <-r.exited:
	case <-time.After(leakTimeout):
		t.Fatal("child never exited")
	}
	r.s.Resize(100, 30)
	r.s.SetCellPixels(9, 18)
	select {
	case <-r.closed:
		t.Fatal("output closed before Close while a process holds the PTY")
	case <-time.After(100 * time.Millisecond):
	}
	r.s.Close()
	select {
	case <-r.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not end ReadLoop while another process holds the PTY")
	}
	for fd, target := range leakcheck.FDs() {
		if target == "/dev/ptmx" && before[fd] != target {
			t.Errorf("PTY master %d still open after Close", fd)
		}
	}
}

// TestResizeWhileOutputCloses: front ends resize sessions whose child may
// be exiting, while ReadLoop closes the PTY. The window-size ioctl must
// hold the descriptor (the race detector checks it), or it could reach
// another file that reused the number.
func TestResizeWhileOutputCloses(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	defer close(done)
	for i := range 20 {
		r := startLeakRun(t, i, leakKind{name: "exits", args: shArgs(`printf 'bye\n'`)}, fastTiming, done)
		resized := make(chan struct{})
		go func() {
			defer close(resized)
			for n := 0; ; n++ {
				select {
				case <-r.closed:
					return
				default:
				}
				r.s.Resize(70+n%20, 20)
				r.s.SetCellPixels(8+n%2, 16)
			}
		}()
		r.finish(t)
		<-resized
	}
}
