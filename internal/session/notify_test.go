package session

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bunk/internal/input"
	"bunk/internal/vt10x"
)

func TestParseEvent(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
		ok   bool
	}{
		{"working", EventWorking, true},
		{"idle", EventIdle, true},
		{"needs-you", EventNeedsYou, true},
		{"done", EventDone, true},
		{"failed", EventFailed, true},
		{"clear", EventClear, true},
		{"", 0, false},
		{"Working", 0, false},
		{"needs_you", 0, false},
		{"event(1)", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := ParseEvent(tc.name)
			if ev != tc.ev || ok != tc.ok {
				t.Fatalf("ParseEvent(%q) = %v, %v; want %v, %v", tc.name, ev, ok, tc.ev, tc.ok)
			}
			if ok && ev.String() != tc.name {
				t.Fatalf("String() = %q, want %q", ev.String(), tc.name)
			}
		})
	}
	if s := Event(0).String(); s != "event(0)" {
		t.Errorf("Event(0).String() = %q", s)
	}
	if s := Event(200).String(); s != "event(200)" {
		t.Errorf("Event(200).String() = %q", s)
	}
}

// TestNotifyStates: each report sets State and Notice, marks the status as
// reported and names an agent even when none was recognised.
func TestNotifyStates(t *testing.T) {
	long := strings.Repeat("y", noticeLimit+10)
	for _, tc := range []struct {
		name    string
		agent   string
		ev      Event
		message string
		state   AgentState
		notice  string
		label   string
	}{
		{"working", "claude", EventWorking, "ignored", AgentWorking, "", "claude"},
		{"idle", "claude", EventIdle, "", AgentIdle, "", "claude"},
		{"needs-you", "claude", EventNeedsYou, "Allow npm install?", AgentNeedsYou, "Allow npm install?", "claude"},
		{"needs-you without text", "codex", EventNeedsYou, "  ", AgentNeedsYou, defaultNotice, "codex"},
		{"needs-you control chars", "claude", EventNeedsYou, "a\x1b[31mb\nc", AgentNeedsYou, "a [31mb c", "claude"},
		{"needs-you long", "claude", EventNeedsYou, long, AgentNeedsYou, strings.Repeat("y", noticeLimit-1) + "…", "claude"},
		{"done", "claude", EventDone, "Fixed the race in ReadLoop.", AgentDone, "Fixed the race in ReadLoop.", "claude"},
		{"done without text", "gemini", EventDone, "", AgentDone, "", "gemini"},
		{"failed", "claude", EventFailed, "API Error: Rate limit reached", AgentFailed, "API Error: Rate limit reached", "claude"},
		{"unrecognised agent", "", EventWorking, "", AgentWorking, "", reportingAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.agent = tc.agent
			s.Notify(tc.ev, tc.message)
			st := s.Status()
			if st.State != tc.state || st.Notice != tc.notice || st.Agent != tc.label || !st.Reported {
				t.Fatalf("Status = %+v; want agent %q state %v notice %q reported", st, tc.label, tc.state, tc.notice)
			}
		})
	}
}

// TestNotifyPriorityOverInference: a report hides what output, bells and
// OSC notifications suggest until Acknowledge or clear; inference then
// takes over again.
func TestNotifyPriorityOverInference(t *testing.T) {
	s := newTestSession(40, 3)
	s.Term = vt10x.New(vt10x.WithSize(40, 3), vt10x.WithBellCallback(s.onBell))
	s.oscScan.status = s.onStatusOSC
	s.agent = "claude"
	expect := func(step string, state AgentState, notice string, reported bool) {
		t.Helper()
		st := s.Status()
		if st.State != state || st.Notice != notice || st.Reported != reported {
			t.Fatalf("%s: %+v; want state %v notice %q reported %v", step, st, state, notice, reported)
		}
	}

	s.lastOutput = time.Now().Add(-time.Hour)
	s.Notify(EventWorking, "")
	expect("working while quiet", AgentWorking, "", true)

	s.oscScan.Scan([]byte("\x1b]9;Claude needs your permission\x07"), func([]byte) {})
	s.Lock()
	s.Feed([]byte("\a"))
	s.Unlock()
	expect("OSC 9 and BEL under a report", AgentWorking, "", true)

	s.lastOutput = time.Now()
	s.Notify(EventIdle, "")
	expect("idle while output flows", AgentIdle, "", true)

	s.Notify(EventNeedsYou, "Approve Bash?")
	expect("needs-you", AgentNeedsYou, "Approve Bash?", true)

	s.Acknowledge()
	expect("acknowledged: inference, attention cleared", AgentWorking, "", false)

	s.Notify(EventDone, "All tests pass")
	expect("done", AgentDone, "All tests pass", true)
	s.oscScan.Scan([]byte("\x1b]133;D;1\x07"), func([]byte) {})
	expect("failure inferred under a report", AgentDone, "All tests pass", true)
	s.Notify(EventClear, "")
	expect("cleared: the inferred failure shows", AgentFailed, "", false)

	s.failed = false
	s.Notify(EventFailed, "")
	s.Acknowledge()
	s.lastOutput = time.Now().Add(-time.Hour)
	expect("failed report acknowledged", AgentIdle, "", false)
}

// TestNotifyStatusChangedOnce: Notify runs the hook exactly once for each
// change of Status and never for a repeated report.
func TestNotifyStatusChangedOnce(t *testing.T) {
	s := newTestSession(20, 2)
	s.agent = "claude"
	s.lastOutput = time.Now().Add(-time.Hour)
	var calls atomic.Int32
	s.statusHook = func() { calls.Add(1) }
	for _, step := range []struct {
		ev      Event
		message string
		calls   int32
	}{
		{EventWorking, "", 1},
		{EventWorking, "text is dropped for working", 0},
		{EventNeedsYou, "Approve Bash?", 1},
		{EventNeedsYou, "Approve Bash?", 0},
		{EventNeedsYou, "Approve Edit?", 1},
		{EventDone, "", 1},
		{EventDone, "  ", 0},
		{EventFailed, "", 1},
		{EventIdle, "", 1},
		{EventClear, "", 1}, // inference also says idle, but Reported flips
		{EventClear, "", 0},
		{EventIdle, "", 1},
		{EventIdle, "", 0},
		{Event(99), "unknown", 0},
	} {
		before := calls.Load()
		s.Notify(step.ev, step.message)
		if got := calls.Load() - before; got != step.calls {
			t.Fatalf("Notify(%v, %q): %d StatusChanged calls, want %d", step.ev, step.message, got, step.calls)
		}
	}
}

// TestNotifyHookRunsWithoutLock: the hook may read Status, which takes the
// session lock; Notify must have released it.
func TestNotifyHookRunsWithoutLock(t *testing.T) {
	s := newTestSession(20, 2)
	s.agent = "claude"
	got := make(chan Status, 1)
	s.statusHook = func() { got <- s.Status() }
	done := make(chan struct{})
	go func() {
		s.Notify(EventNeedsYou, "Approve?")
		close(done)
	}()
	select {
	case st := <-got:
		if st.State != AgentNeedsYou {
			t.Fatalf("hook saw %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not run: Notify holds the session lock")
	}
	<-done
}

// TestNotifyForegroundChange: a report belongs to the foreground process
// group it arrived from. It survives the poll that first sees that group
// (the agent reported before bunk recognised it) and goes when the group
// leaves.
func TestNotifyForegroundChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pgid     int    // foreground group at the next poll
		process  string // its name
		changed  bool
		agent    string
		reported bool
	}{
		{"same group", 4242, "sh", false, reportingAgent, true},
		{"agent recognised in the same group", 4242, "claude", true, "claude", true},
		{"group left", 4300, "bash", true, "", false},
		{"no foreground group", 0, "", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.Notify(EventWorking, "")
			s.report.pgid = 4242
			changed := s.refreshStatus(0, tc.pgid, tc.process, true)
			st := s.Status()
			if changed != tc.changed || st.Agent != tc.agent || st.Reported != tc.reported {
				t.Fatalf("refreshStatus changed=%v, status %+v; want changed=%v agent %q reported=%v",
					changed, st, tc.changed, tc.agent, tc.reported)
			}
		})
	}
	s := newTestSession(20, 2)
	s.Notify(EventWorking, "")
	if s.refreshStatus(0, 0, "", false); !s.Status().Reported {
		t.Fatal("a poll without a group change dropped the report")
	}
}

// TestNotifyEnvReachesChild runs env on a real PTY: Options.NotifySocket and
// NotifySession reach the child, and values the front end inherited never do.
func TestNotifyEnvReachesChild(t *testing.T) {
	t.Setenv(EnvNotifySocket, "/stale/socket")
	t.Setenv(EnvNotifySession, "stale-token")
	for _, tc := range []struct {
		name          string
		socket, token string
		want          []string
	}{
		{"front end exports", "/run/user/1000/bunk/notify-7.sock", "0123456789abcdef0123456789abcdef",
			[]string{EnvNotifySession + "=0123456789abcdef0123456789abcdef", EnvNotifySocket + "=/run/user/1000/bunk/notify-7.sock"}},
		{"tui leaves empty", "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := runEnv(t, Options{NotifySocket: tc.socket, NotifySession: tc.token})
			if strings.Join(lines, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("child env %q, want %q", lines, tc.want)
			}
		})
	}
}

// runEnv starts `env` in a session and returns its BUNK_NOTIFY_ lines, sorted.
func runEnv(t *testing.T, o Options) []string {
	t.Helper()
	o.Cols, o.Rows, o.Scrollback = 120, 6, 100
	o.Args = []string{"/bin/sh", "-c", "env | grep '^BUNK_NOTIFY_'; echo END-OF-ENV"}
	s, err := Start(o)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	s.Run(Hooks{Closed: func() { close(closed) }}, done)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("env did not finish")
	}
	s.Lock()
	defer s.Unlock()
	var lines []string
	text := func(row []vt10x.Glyph) string {
		var b strings.Builder
		for _, g := range row {
			if g.Width >= 0 {
				b.WriteRune(g.Char)
			}
		}
		return strings.TrimRight(b.String(), " \x00")
	}
	all := make([]string, 0, s.Scrollback.Count+6)
	for i := range s.Scrollback.Count {
		all = append(all, text(s.Scrollback.Get(i)))
	}
	for y := range 6 {
		all = append(all, text(CaptureRow(s.Term, y, 120)))
	}
	end := false
	for _, l := range all {
		if strings.HasPrefix(l, "BUNK_NOTIFY_") {
			lines = append(lines, l)
		}
		end = end || l == "END-OF-ENV"
	}
	if !end {
		t.Fatalf("env output incomplete: %q", all)
	}
	sort.Strings(lines)
	return lines
}

// TestNotifyThroughRun: Run hands Hooks.StatusChanged to Notify, and the
// first process poll keeps a report from the child's foreground group.
func TestNotifyThroughRun(t *testing.T) {
	t.Parallel()
	s, err := Start(Options{Cols: 40, Rows: 5, Scrollback: 100, Args: []string{"/bin/sh", "-c", "sleep 30"}, Timing: fastTiming})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 16)
	done := make(chan struct{})
	defer func() {
		close(done)
		s.Close()
	}()
	s.Run(Hooks{StatusChanged: func() { changed <- struct{}{} }}, done)
	s.Notify(EventNeedsYou, "Approve Bash?")
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("StatusChanged did not run")
	}
	// The first poll of trackFgProcess sees the child's group for the first
	// time and names its foreground process.
	for deadline := time.Now().Add(10 * time.Second); s.Status().Process == ""; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no process poll: %+v", s.Status())
		}
	}
	if st := s.Status(); st.State != AgentNeedsYou || st.Notice != "Approve Bash?" || !st.Reported {
		t.Fatalf("after the first poll: %+v", st)
	}
}

// TestIdleAfterSilentAgent: an agent that printed only before bunk
// recognised it still gets exactly one StatusChanged for idle, from the
// process tracker, within Timing.Idle plus one poll of its detection.
// ReadLoop's idle timer never runs here: no output arrives after detection.
func TestIdleAfterSilentAgent(t *testing.T) {
	t.Parallel()
	// ReadLoop reads the agent's output before the first poll detects the
	// agent, and the agent still counts as working then, on a loaded
	// machine too.
	fast := fastTiming
	fast.Poll, fast.Idle = 100*time.Millisecond, time.Second
	for _, tc := range timings(fast) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testIdleAfterSilentAgent(t, tc.timing)
		})
	}
}

func testIdleAfterSilentAgent(t *testing.T, tm Timing) {
	s, err := Start(Options{
		Cols: 40, Rows: 5, Scrollback: 100,
		Args:   []string{"/bin/sh", "-c", "printf ready; exec sleep 30"},
		Agents: []AgentSpec{{Name: "silent", Processes: []string{"sleep"}}},
		Timing: tm,
	})
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		at time.Time
		st Status
	}
	calls := make(chan call, 64)
	done := make(chan struct{})
	defer func() {
		close(done)
		s.Close()
	}()
	// The process is the agent, and its output waits in the PTY, before
	// the session runs: a poll that saw the shell would never look again,
	// since exec keeps the process group.
	comm := fmt.Sprintf("/proc/%d/comm", s.Cmd.Process.Pid)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		if b, _ := os.ReadFile(comm); strings.TrimSpace(string(b)) == "sleep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child never ran sleep")
		}
	}
	s.Run(Hooks{StatusChanged: func() { calls <- call{time.Now(), s.Status()} }}, done)

	var detected time.Time
	var seen []AgentState
	idle := 0
	timeout := time.After(10 * time.Second)
	for idle == 0 {
		select {
		case c := <-calls:
			if c.st.Agent != "silent" {
				continue
			}
			if detected.IsZero() {
				detected = c.at
				if c.st.State != AgentWorking {
					t.Fatalf("detected as %v, want working: detection came before the agent's output or %v after it",
						c.st.State, tm.idle())
				}
			}
			if len(seen) > 0 && seen[len(seen)-1] == c.st.State {
				t.Fatalf("state %v announced twice in a row: %v", c.st.State, seen)
			}
			seen = append(seen, c.st.State)
			if c.st.State == AgentIdle {
				idle++
				if late := c.at.Sub(detected); late > tm.idle()+tm.poll()+500*time.Millisecond {
					t.Fatalf("idle reported %v after detection, want at most Timing.Idle + 1 poll", late)
				}
			}
		case <-timeout:
			t.Fatalf("no idle StatusChanged; states announced: %v, status now %+v", seen, s.Status())
		}
	}
	// More polls: nothing else changes, so nothing else is announced.
	quiet := time.After(max(tm.poll()*3/2, 300*time.Millisecond))
	for {
		select {
		case c := <-calls:
			t.Fatalf("extra StatusChanged after idle: %+v (before: %v)", c.st, seen)
		case <-quiet:
			if st := s.Status(); st.State != AgentIdle {
				t.Fatalf("status %+v", st)
			}
			return
		}
	}
}

// TestStatusChangeAnnouncedOnce: the reader's idle timer, the process
// tracker's tick and Notify share one record of the announced state, so
// each change of the agent's state is announced exactly once.
func TestStatusChangeAnnouncedOnce(t *testing.T) {
	s := newTestSession(20, 2)
	s.agent = "claude"
	s.lastOutput = time.Now()
	steps := []struct {
		name string
		run  func() bool
		want bool
	}{
		{"tick sees the agent working", func() bool { return s.refreshStatus(0, 0, "claude", false) }, true},
		{"next tick", func() bool { return s.refreshStatus(0, 0, "claude", false) }, false},
		{"idle timer after output stopped", func() bool {
			s.Lock()
			s.lastOutput = time.Now().Add(-IdleAfter - time.Millisecond)
			s.Unlock()
			return s.agentChangedNow()
		}, true},
		{"tick after the timer announced idle", func() bool { return s.refreshStatus(0, 0, "claude", false) }, false},
		{"tick after an unannounced Acknowledge", func() bool {
			s.Lock()
			s.needsYou = true
			s.Unlock()
			if !s.agentChangedNow() {
				t.Fatal("needs-you not announced")
			}
			s.Acknowledge()
			return s.refreshStatus(0, 0, "claude", false)
		}, true},
		{"timer after the tick", func() bool { return s.agentChangedNow() }, false},
	}
	for _, step := range steps {
		if got := step.run(); got != step.want {
			t.Fatalf("%s: changed = %v, want %v", step.name, got, step.want)
		}
	}
}

// TestNotifyAgentSession: a report's agent session ID shows in Status
// until the agent clears or its group leaves the foreground; a report
// without one keeps it, and an ID that is not wire.ValidAgentSession is
// dropped.
func TestNotifyAgentSession(t *testing.T) {
	s := newTestSession(20, 2)
	s.NotifyReport(Report{Event: EventWorking, AgentSession: "sess-1"})
	if got := s.Status().AgentSession; got != "sess-1" {
		t.Fatalf("AgentSession = %q, want sess-1", got)
	}
	s.NotifyReport(Report{Event: EventDone, AgentSession: "$(rm -rf ~)"})
	s.Notify(EventIdle, "")
	if got := s.Status().AgentSession; got != "sess-1" {
		t.Fatalf("after reports without a valid ID: %q, want sess-1", got)
	}
	s.NotifyReport(Report{Event: EventWorking, AgentSession: "sess-2"})
	if got := s.Status().AgentSession; got != "sess-2" {
		t.Fatalf("a new conversation (/clear): %q, want sess-2", got)
	}
	s.Notify(EventClear, "")
	if got := s.Status().AgentSession; got != "" {
		t.Fatalf("after clear: %q", got)
	}

	s.NotifyReport(Report{Event: EventWorking, AgentSession: "sess-3"})
	s.agentSessionPGID = 4242
	s.refreshStatus(0, 4242, "claude", true)
	if got := s.Status().AgentSession; got != "sess-3" {
		t.Fatalf("same group: %q, want sess-3", got)
	}
	if changed := s.refreshStatus(0, 4300, "bash", true); !changed || s.Status().AgentSession != "" {
		t.Fatalf("group left: changed=%v, AgentSession %q", changed, s.Status().AgentSession)
	}
}

// TestAcknowledgeKeepsReportedNeedsYou: looking at a session, or keys that
// move through a prompt's choices, leave a needs-you its agent reported;
// other keys answer it, unless the agent reports the end itself (Held, a question), and Esc
// or Ctrl+C cancel either. A done goes at any meeting, and so does an
// inferred request.
func TestAcknowledgeKeepsReportedNeedsYou(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  Report
		how  Ack
		want AgentState
	}{
		{"seen needs-you", Report{Event: EventNeedsYou}, AckSeen, AgentNeedsYou},
		{"answered needs-you", Report{Event: EventNeedsYou}, AckAnswer, AgentNone},
		{"cancelled needs-you", Report{Event: EventNeedsYou}, AckCancel, AgentNone},
		{"seen question", Report{Event: EventNeedsYou, Held: true}, AckSeen, AgentNeedsYou},
		{"answered question", Report{Event: EventNeedsYou, Held: true}, AckAnswer, AgentNeedsYou},
		{"cancelled question", Report{Event: EventNeedsYou, Held: true}, AckCancel, AgentNone},
		{"seen done", Report{Event: EventDone}, AckSeen, AgentNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.NotifyReport(tc.rep)
			s.AcknowledgeAs(tc.how)
			if got := s.Status().State; got != tc.want {
				t.Errorf("state %v, want %v", got, tc.want)
			}
		})
	}
	// The agent's own report ends a held question.
	s := newTestSession(20, 2)
	s.NotifyReport(Report{Event: EventNeedsYou, Held: true})
	s.Acknowledge()
	s.Notify(EventWorking, "")
	if got := s.Status().State; got != AgentWorking {
		t.Errorf("after the answer: %v", got)
	}
}

func TestKeyAck(t *testing.T) {
	for _, tc := range []struct {
		ev   input.Event
		want Ack
	}{
		{input.Event{Key: input.KeyEnter}, AckAnswer},
		{input.Event{Key: input.KeyEnter, Mod: input.ModKeypad}, AckAnswer},
		{input.Event{Key: input.KeyEscape}, AckCancel},
		{input.Event{Key: input.KeyRune, Rune: 'c', Mod: input.ModCtrl}, AckCancel},
		{input.Event{Key: input.KeyRune, Rune: '1'}, AckAnswer}, // picks a choice of a permission prompt
		{input.Event{Key: input.KeyRune, Rune: 'y'}, AckAnswer},
		{input.Event{Key: input.KeyDown}, AckSeen},
		{input.Event{Key: input.KeyBacktab}, AckSeen},
	} {
		if got := KeyAck(tc.ev); got != tc.want {
			t.Errorf("KeyAck(%+v) = %v, want %v", tc.ev, got, tc.want)
		}
	}
}
