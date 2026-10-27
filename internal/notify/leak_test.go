package notify

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"bunk/internal/leakcheck"
	"bunk/internal/session"
)

// notifyRun is one session registered with the notify server.
type notifyRun struct {
	s      *session.Session
	token  string
	byExit bool // the child exits after its report; otherwise the test closes it
	status chan struct{}
	exited chan struct{}
	closed chan struct{}
}

// TestNotifySessionLeaks starts sessions with the notify socket in their
// environment, registered with the server as a front end does. Each child
// reports needs-you through the notify CLI (this test binary); then half
// exit by themselves and half are closed while running. After each batch
// the server routes no token, and descriptors (sockets included),
// goroutines, child processes and live heap are back at the baseline.
// 60 sessions here, 400 with SOAK=1.
func TestNotifySessionLeaks(t *testing.T) {
	if !leakcheck.Supported() {
		t.Skip("needs /proc")
	}
	total, batches := 60, 2
	if leakcheck.Soak() {
		total, batches = 400, 8
	}
	srv, _, _ := startServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	limits := leakcheck.Limits{FDs: 2, Goroutines: 2, Heap: 4 << 20, Settle: 5*time.Second + session.HangupGrace}

	routed := func() int {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.targets)
	}
	before := routed() // startServer's own target
	runNotifyBatch(t, srv, exe, 0, 1, done)
	base := leakcheck.Check(t, "warm-up", leakcheck.Take(), limits)
	var first leakcheck.Snapshot
	for b := range batches {
		n := total / batches
		runNotifyBatch(t, srv, exe, 1+b*n, n, done)
		if got := routed(); got != before {
			t.Errorf("batch %d: the server routes %d tokens, %d before", b+1, got, before)
		}
		snap := leakcheck.Check(t, fmt.Sprintf("batch %d", b+1), base, limits)
		t.Logf("batch %d: %d sessions, %d fds, %d goroutines, heap %s (baseline %d, %d, %s)",
			b+1, n, len(snap.FDs), snap.Goroutines, leakcheck.MiB(snap.Heap),
			len(base.FDs), base.Goroutines, leakcheck.MiB(base.Heap))
		if t.Failed() {
			t.FailNow()
		}
		if b == 0 {
			first = snap
		} else if snap.Heap > first.Heap+2<<20 {
			t.Errorf("batch %d: live heap %s, %s after batch 1", b+1, leakcheck.MiB(snap.Heap), leakcheck.MiB(first.Heap))
		}
	}
}

// runNotifyBatch starts n reporting sessions and ends them all.
func runNotifyBatch(t *testing.T, srv *Server, exe string, first, n int, done <-chan struct{}) {
	t.Helper()
	runs := make([]*notifyRun, n)
	for i := range runs {
		token, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		r := &notifyRun{token: token, byExit: (first+i)%2 == 0, status: make(chan struct{}, 1),
			exited: make(chan struct{}), closed: make(chan struct{})}
		tail := "exec sleep 60"
		if r.byExit {
			tail = "exit 0"
		}
		// The child waits for a line, so its token is registered before
		// the hook reports, as when a user submits a prompt. A -race
		// binary sleeps a second before exiting unless GORACE says not to.
		script := `read go; GORACE="$GORACE atexit_sleep_ms=0" ` + cliHelperEnv + `=1 "$0" needs-you "report $1"; ` + tail
		r.s, err = session.Start(session.Options{
			ID: first + i, Cols: 80, Rows: 5, Scrollback: 50,
			Args:         []string{"/bin/sh", "-c", script, exe, fmt.Sprint(first + i)},
			NotifySocket: srv.Path(), NotifySession: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		r.s.Run(session.Hooks{
			StatusChanged: func() {
				select {
				case r.status <- struct{}{}:
				default:
				}
			},
			Exited: func() { close(r.exited) },
			Closed: func() { close(r.closed) },
		}, done)
		srv.Register(token, r.s)
		if err := r.s.WriteInput([]byte("go\r")); err != nil {
			t.Fatal(err)
		}
		runs[i] = r
	}
	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Go(func() { r.finish(t, srv) })
	}
	wg.Wait()
}

// finish waits for the report, then ends the session as its run says and
// unregisters it, as bunk-gui's removePane does.
func (r *notifyRun) finish(t *testing.T, srv *Server) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for st := r.s.Status(); !st.Reported || st.State != session.AgentNeedsYou; st = r.s.Status() {
		select {
		case <-r.status:
		case <-timer.C:
			t.Errorf("session %d: no needs-you report; status %+v", r.s.ID, st)
			srv.Unregister(r.token)
			r.s.Close()
			return
		}
	}
	if r.byExit {
		select {
		case <-r.exited:
		case <-timer.C:
			t.Errorf("session %d: child never exited", r.s.ID)
		}
	}
	srv.Unregister(r.token)
	r.s.Close()
	for _, ch := range []chan struct{}{r.exited, r.closed} {
		select {
		case <-ch:
		case <-timer.C:
			t.Errorf("session %d did not end", r.s.ID)
			return
		}
	}
}
