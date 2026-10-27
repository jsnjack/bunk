package session

import (
	"os"
	"slices"
	"testing"
	"time"
)

// stretchStep is output at an offset from the first one, or the user's
// input.
type stretchStep struct {
	at    time.Duration
	input bool
}

// every returns output steps from from to to, step apart.
func every(from, to, step time.Duration) []stretchStep {
	var out []stretchStep
	for at := from; at <= to; at += step {
		out = append(out, stretchStep{at: at})
	}
	return out
}

// TestInferredDoneStretch: a known agent whose working stretch lasted
// Timing.Done is done once its output stops for Timing.Idle; a shorter
// stretch ends idle. Gaps shorter than Timing.Idle keep a stretch going;
// a longer gap or the user's input starts a new one. Zero fields take
// DoneAfter and IdleAfter; the set values give the other answer than the
// defaults would. Nothing waits: the output times are made up.
func TestInferredDoneStretch(t *testing.T) {
	t.Parallel()
	const sec = time.Second
	input := []stretchStep{{input: true}}
	for _, tc := range []struct {
		name   string
		timing Timing
		steps  []stretchStep
		done   bool
	}{
		{"defaults, DoneAfter of output", Timing{}, every(0, DoneAfter, sec), true},
		{"defaults, just short of DoneAfter", Timing{}, append(every(0, DoneAfter-2*sec, sec), stretchStep{at: DoneAfter - time.Millisecond}), false},
		{"defaults, gaps just under IdleAfter", Timing{}, every(0, DoneAfter+2*sec, IdleAfter-100*time.Millisecond), true},
		{"defaults, a gap of IdleAfter splits it", Timing{}, slices.Concat(every(0, 6*sec, sec), every(6*sec+IdleAfter, 14*sec, sec)), false},
		{"defaults, input splits it", Timing{}, slices.Concat(every(0, 6*sec, sec), input, every(7*sec, 14*sec, sec)), false},
		{"defaults, input before the stretch", Timing{}, slices.Concat(input, every(0, DoneAfter, sec)), true},
		{"shorter Done", Timing{Done: 3 * sec}, every(0, 3*sec, sec), true},
		{"longer Idle", Timing{Idle: 5 * sec}, every(0, DoneAfter+2*sec, 3*sec), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.timing = tc.timing
			s.agent = "claude"
			start := time.Now().Add(-time.Hour)
			var last time.Duration
			for _, st := range tc.steps {
				if st.input {
					if err := s.WriteInput([]byte("y")); err != nil {
						t.Fatal(err)
					}
					continue
				}
				s.Lock()
				s.noteOutput(start.Add(st.at))
				s.Unlock()
				last = st.at
			}
			quiet := start.Add(last + tc.timing.idle())
			s.Lock()
			defer s.Unlock()
			if got := s.view(quiet.Add(-time.Millisecond)).state; got != AgentWorking {
				t.Errorf("just before Timing.Idle of silence: %v, want working", got)
			}
			want := AgentIdle
			if tc.done {
				want = AgentDone
			}
			if got := s.view(quiet).state; got != want {
				t.Errorf("after Timing.Idle of silence: %v, want %v", got, want)
			}
		})
	}
}

// TestInferredDoneHolds: an inferred finished turn stays until Acknowledge,
// new output, EventClear or a foreground change. An attention request and
// a report take priority over it, and an agent that reports through hooks
// is never inferred done.
func TestInferredDoneHolds(t *testing.T) {
	t.Parallel()
	// work feeds a working stretch of DoneAfter that ended an hour ago.
	work := func(s *Session) {
		start := time.Now().Add(-time.Hour)
		s.Lock()
		for _, st := range every(0, DoneAfter, time.Second) {
			s.noteOutput(start.Add(st.at))
		}
		s.Unlock()
	}
	expect := func(t *testing.T, s *Session, step string, state AgentState, notice string, reported bool) {
		t.Helper()
		if st := s.Status(); st.State != state || st.Notice != notice || st.Reported != reported {
			t.Fatalf("%s: %+v; want state %v notice %q reported %v", step, st, state, notice, reported)
		}
	}
	done := func(t *testing.T) *Session {
		t.Helper()
		s := newTestSession(20, 2)
		s.agent = "claude"
		work(s)
		expect(t, s, "after a long stretch", AgentDone, "", false)
		return s
	}

	t.Run("Acknowledge", func(t *testing.T) {
		s := done(t)
		s.Acknowledge()
		expect(t, s, "acknowledged", AgentIdle, "", false)
	})
	t.Run("new output", func(t *testing.T) {
		s := done(t)
		now := time.Now()
		s.Lock()
		s.noteOutput(now)
		working, after := s.view(now).state, s.view(now.Add(IdleAfter)).state
		s.Unlock()
		if working != AgentWorking || after != AgentIdle {
			t.Fatalf("new output: %v, then %v after it; want working, then idle", working, after)
		}
	})
	t.Run("clear", func(t *testing.T) {
		s := done(t)
		s.Notify(EventClear, "")
		expect(t, s, "cleared", AgentIdle, "", false)
	})
	t.Run("process poll", func(t *testing.T) {
		s := done(t)
		s.refreshStatus(0, 0, "claude", false)
		expect(t, s, "same foreground group", AgentDone, "", false)
		s.refreshStatus(0, 4300, "claude", true)
		expect(t, s, "new foreground group", AgentIdle, "", false)
	})
	t.Run("agent left", func(t *testing.T) {
		s := done(t)
		s.refreshStatus(0, 4300, "bash", true)
		expect(t, s, "shell in the foreground", AgentNone, "", false)
	})
	t.Run("attention request", func(t *testing.T) {
		s := done(t)
		s.Lock()
		s.needsYou, s.notice = true, "Approve Bash?"
		s.Unlock()
		expect(t, s, "attention request", AgentNeedsYou, "Approve Bash?", false)
		s.Acknowledge()
		expect(t, s, "acknowledged", AgentIdle, "", false)
	})
	t.Run("report", func(t *testing.T) {
		s := done(t)
		s.Notify(EventDone, "Fixed the race")
		expect(t, s, "done report", AgentDone, "Fixed the race", true)
		s.Acknowledge()
		expect(t, s, "acknowledged", AgentIdle, "", false)
	})
	t.Run("hooks", func(t *testing.T) {
		s := newTestSession(20, 2)
		s.agent = "claude"
		s.Notify(EventWorking, "")
		s.Acknowledge()
		work(s)
		expect(t, s, "an agent that reported", AgentIdle, "", false)
		s.Notify(EventDone, "All tests pass")
		expect(t, s, "its done report", AgentDone, "All tests pass", true)
		s.Acknowledge()
		expect(t, s, "acknowledged", AgentIdle, "", false)

		s.Notify(EventClear, "")
		work(s)
		expect(t, s, "after clear", AgentDone, "", false)
	})
	t.Run("hooks of a group that left", func(t *testing.T) {
		s := newTestSession(20, 2)
		s.agent = "claude"
		s.Notify(EventIdle, "")
		s.Lock()
		s.hookedPGID = 4242
		s.Unlock()
		s.Acknowledge()
		s.refreshStatus(0, 4242, "claude", true)
		work(s)
		expect(t, s, "the poll that first sees the reporting group", AgentIdle, "", false)
		s.refreshStatus(0, 4300, "claude", true)
		work(s)
		expect(t, s, "another group", AgentDone, "", false)
	})
	t.Run("remote agent", func(t *testing.T) {
		s := newTestSession(20, 2)
		s.Lock()
		s.remote = remoteAgent{agent: "claude", host: "build01"}
		s.Unlock()
		work(s)
		if st := s.Status(); st.Agent != "claude" || st.State != AgentIdle {
			t.Fatalf("remote agent after a long stretch of ssh output: %+v, want claude idle", st)
		}
	})
}

// TestInferredDoneThroughReadLoop: after a working stretch of Timing.Done,
// the reader's idle timer announces done instead of idle, once; the
// process tick then finds nothing new. The next output announces working,
// and that short stretch ends idle.
func TestInferredDoneThroughReadLoop(t *testing.T) {
	t.Parallel()
	fast := fastTiming
	fast.Done = 500 * time.Millisecond
	for _, tc := range timings(fast) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testInferredDoneThroughReadLoop(t, tc.timing)
		})
	}
}

func testInferredDoneThroughReadLoop(t *testing.T, tm Timing) {
	s := newTestSession(40, 3)
	s.timing = tm
	s.agent = "claude"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.PTY = r
	announced := make(chan Status, 16)
	closed := make(chan struct{})
	go func() {
		s.ReadLoop(Hooks{StatusChanged: func() { announced <- s.Status() }})
		close(closed)
	}()
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		<-closed
	}()
	write := func(text string) {
		t.Helper()
		if _, err := w.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	next := func(what string, want AgentState) Status {
		t.Helper()
		select {
		case st := <-announced:
			if st.State != want {
				t.Fatalf("%s: announced %+v, want %v", what, st, want)
			}
			return st
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: nothing announced; status %+v", what, s.Status())
		}
		return Status{}
	}

	// The stretch is made Timing.Done long at once, so the test waits only
	// for the idle timer. If a stall let the timer announce idle first, the
	// next output starts another stretch.
	for {
		write("working\r\n")
		next("output", AgentWorking)
		s.Lock()
		working := s.shown.state == AgentWorking
		if working {
			s.workStart = s.workStart.Add(-tm.done())
		}
		s.Unlock()
		if working {
			break
		}
		next("idle after a stall", AgentIdle)
	}
	if st := next("end of the stretch", AgentDone); st.Reported || st.Notice != "" || st.Agent != "claude" {
		t.Fatalf("inferred done: %+v, want claude, not reported, no notice", st)
	}
	if s.refreshStatus(0, 0, "claude", false) {
		t.Fatal("the process tick announced the finished turn again")
	}
	write("more\r\n")
	next("new output", AgentWorking)
	next("end of a short stretch", AgentIdle)
}
