package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"bunk/internal/notify/wire"
)

// inBandPayload returns r as the payload of an in-band report, as the OSC
// scanner hands it to onStatusOSC: without "ESC ] 1337;" and the BEL.
func inBandPayload(t *testing.T, r wire.Report) []byte {
	t.Helper()
	line, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	seq := wire.InBand(line)
	return bytes.TrimSuffix(bytes.TrimPrefix(seq, []byte("\x1b]1337;")), []byte("\a"))
}

// TestInBandAccepted: a known token counts in any session; an unknown or
// missing one only while ssh or mosh runs in the foreground.
func TestInBandAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		token    string
		remote   bool
		accepted bool
	}{
		{"known token in a local pane", "tok", false, true},
		{"known token over ssh", "tok", true, true},
		{"no token over ssh", "", true, true},
		{"unknown token over ssh", "stale", true, true},
		{"no token in a local pane", "", false, false},
		{"unknown token in a local pane", "stale", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.knownToken = func(token string) bool { return token == "tok" }
			s.remoteClient = tc.remote
			s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Session: tc.token, Event: "needs-you", Message: "Allow rm?"}))
			st := s.Status()
			if got := st.State == AgentNeedsYou && st.Reported && st.Notice == "Allow rm?"; got != tc.accepted {
				t.Fatalf("accepted %v, want %v: %+v", got, tc.accepted, st)
			}
		})
	}
	s := newTestSession(20, 2)
	s.remoteClient = true
	s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Session: "tok", Event: "done"}))
	if st := s.Status(); st.State != AgentDone {
		t.Fatalf("without KnownToken, ssh still admits a report: %+v", st)
	}
	for _, bad := range []string{"SetUserVar=bunk_notify=!!", "SetUserVar=other=e30=", "File=inline=1:AAAA"} {
		s := newTestSession(20, 2)
		s.remoteClient = true
		s.onStatusOSC(inBandOSC, []byte(bad))
		if st := s.Status(); st.State != AgentNone || st.Reported {
			t.Fatalf("%q changed the status: %+v", bad, st)
		}
	}
	s = newTestSession(20, 2)
	s.remoteClient = true
	s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Event: "waiting"}))
	if st := s.Status(); st.State != AgentNone {
		t.Fatalf("an unknown event changed the status: %+v", st)
	}
}

// TestInBandRemoteFields: a remote agent's report names the agent, host,
// repo, branch and directory instead of the local ssh process's. They
// survive an acknowledgement and go with clear.
func TestInBandRemoteFields(t *testing.T) {
	t.Parallel()
	s := newTestSession(20, 2)
	s.remoteClient = true
	s.cwd, s.repo, s.branch, s.SSHHost = "/home/me", "dotfiles", "master", "build01"
	s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Event: "needs-you", Message: "Allow rm?", Agent: "claude",
		Host: "build01.example", Repo: "bunk", Branch: "remote-status", Cwd: "/srv/bunk"}))
	want := Status{Agent: "claude", State: AgentNeedsYou, Notice: "Allow rm?", Reported: true, Host: "build01.example",
		Repo: "bunk", Branch: "remote-status", Cwd: "/srv/bunk", SSHHost: "build01"}
	if st := s.Status(); st != want {
		t.Fatalf("status %+v\nwant   %+v", st, want)
	}

	s.Acknowledge()
	want.State, want.Notice, want.Reported = AgentIdle, "", false
	if st := s.Status(); st != want {
		t.Fatalf("acknowledged: %+v\nwant          %+v", st, want)
	}
	// Inference now follows the remote agent's output, as for a local one.
	s.Lock()
	s.lastOutput = time.Now()
	s.Unlock()
	if st := s.Status(); st.State != AgentWorking || st.Agent != "claude" {
		t.Fatalf("output after acknowledging: %+v", st)
	}

	// A report without fields keeps the fields of the last one.
	s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Event: "done", Message: "Fixed"}))
	if st := s.Status(); st.Agent != "claude" || st.Host != "build01.example" || st.State != AgentDone {
		t.Fatalf("report without fields: %+v", st)
	}

	// A remote bunk sends an OSC 777 alert beside its report; clear drops
	// it with the report.
	s.onStatusOSC(777, []byte("notify;claude is done;Fixed"))
	s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Event: "clear"}))
	want = Status{Cwd: "/home/me", Repo: "dotfiles", Branch: "master", SSHHost: "build01"}
	if st := s.Status(); st != want {
		t.Fatalf("after clear: %+v\nwant        %+v", st, want)
	}
}

// TestRemoteFieldsEndWithForeground: the remote fields belong to the
// foreground process group that carried them, as reports do.
func TestRemoteFieldsEndWithForeground(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		pgid    int
		process string
		kept    bool
	}{
		{"same group", 4242, "ssh", true},
		{"ssh exited", 4300, "bash", false},
		{"no foreground group", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.remoteClient = true
			s.onStatusOSC(inBandOSC, inBandPayload(t, wire.Report{Event: "working", Agent: "codex", Host: "gpu01"}))
			s.Lock()
			s.report.pgid, s.remotePGID = 4242, 4242
			s.takeAgentChange(time.Now()) // ReadLoop announced the report
			s.Unlock()
			changed := s.refreshStatus(0, tc.pgid, tc.process, true)
			st := s.Status()
			if kept := st.Host == "gpu01" && st.Agent == "codex"; kept != tc.kept || changed == tc.kept {
				t.Fatalf("refreshStatus changed=%v, status %+v; want kept=%v", changed, st, tc.kept)
			}
		})
	}
}

// TestRemoteInference: while ssh or mosh runs in the foreground, OSC
// 9/99/777 mean needs-you without a known agent, and BEL means nothing.
func TestRemoteInference(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		remote bool
		num    int
		text   string
		state  AgentState
		notice string
	}{
		{"OSC 9 over ssh", true, 9, "Approve edit?", AgentNeedsYou, "Approve edit?"},
		{"OSC 777 over ssh", true, 777, "notify;Codex;Approval requested", AgentNeedsYou, "Approval requested"},
		{"OSC 99 over mosh", true, 99, "p=body;Done", AgentNeedsYou, "Done"},
		{"OSC 9 progress over ssh", true, 9, "4;1;50", AgentNone, ""},
		{"OSC 9 in a plain shell", false, 9, "Approve edit?", AgentNone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(20, 2)
			s.remoteClient = tc.remote
			s.onStatusOSC(tc.num, []byte(tc.text))
			st := s.Status()
			if st.State != tc.state || st.Notice != tc.notice || st.Reported {
				t.Fatalf("status %+v, want state %v notice %q", st, tc.state, tc.notice)
			}
			if tc.state != AgentNone && st.Agent != reportingAgent {
				t.Fatalf("agent %q, want %q", st.Agent, reportingAgent)
			}
		})
	}

	s := newTestSession(20, 2)
	s.remoteClient = true
	s.Lock()
	if _, err := s.Term.Write([]byte("\a")); err != nil {
		t.Fatal(err)
	}
	s.Unlock()
	s.onBell()
	if st := s.Status(); st.State != AgentNone {
		t.Fatalf("a bell over ssh: %+v", st)
	}

	// The request ends with the connection.
	s.onStatusOSC(9, []byte("Approve edit?"))
	s.refreshStatus(0, 4300, "bash", true)
	if st := s.Status(); st.State != AgentNone || st.Notice != "" {
		t.Fatalf("after ssh exited: %+v", st)
	}
	s.refreshStatus(0, 4400, "mosh-client", true)
	if !s.remoteClient {
		t.Fatal("mosh-client is not a remote client")
	}
}

// TestInBandStatusChangedOnce: ReadLoop announces an in-band report once,
// and a change of the remote fields alone counts as a change.
func TestInBandStatusChangedOnce(t *testing.T) {
	t.Parallel()
	s := newTestSession(20, 2)
	s.remoteClient = true
	announce := func() bool {
		s.Lock()
		defer s.Unlock()
		return s.takeAgentChange(time.Now())
	}
	report := wire.Report{Event: "working", Agent: "claude", Repo: "a"}
	s.onStatusOSC(inBandOSC, inBandPayload(t, report))
	if !announce() || announce() {
		t.Fatal("the first report is not announced exactly once")
	}
	s.onStatusOSC(inBandOSC, inBandPayload(t, report))
	if announce() {
		t.Fatal("a repeated report was announced")
	}
	report.Repo = "b"
	s.onStatusOSC(inBandOSC, inBandPayload(t, report))
	if !announce() {
		t.Fatal("a new repo was not announced")
	}
}

// TestInBandThroughRun: a child that writes an in-band report to its
// terminal, as the notify CLI does, changes Status through ReadLoop.
func TestInBandThroughRun(t *testing.T) {
	t.Parallel()
	line, err := wire.Report{Session: "tok", Event: "done", Message: "Fixed it", Agent: "gemini", Repo: "bunk"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	seq := wire.InBand(line)
	var printf strings.Builder
	for _, b := range seq {
		fmt.Fprintf(&printf, "\\%03o", b)
	}
	s, err := Start(Options{Cols: 40, Rows: 5, Scrollback: 100, Timing: fastTiming,
		Args:       []string{"/bin/sh", "-c", "printf '" + printf.String() + "'; sleep 30"},
		KnownToken: func(token string) bool { return token == "tok" }})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 16)
	done := make(chan struct{})
	defer func() {
		close(done)
		s.Close()
	}()
	s.Run(Hooks{StatusChanged: func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}}, done)
	deadline := time.After(10 * time.Second)
	for {
		if st := s.Status(); st.State == AgentDone {
			if st.Notice != "Fixed it" || st.Agent != "gemini" || st.Repo != "bunk" || !st.Reported {
				t.Fatalf("status %+v", st)
			}
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("no report: %+v", s.Status())
		}
	}
	s.Lock()
	row := CaptureRow(s.Term, 0, 40)
	s.Unlock()
	for _, g := range row {
		if g.Char != ' ' && g.Char != 0 {
			t.Fatalf("the report left text on screen: %q", g.Char)
		}
	}
}

// FuzzInBand: parseInBand never panics and accepts only reports with a
// known event and a bounded token.
func FuzzInBand(f *testing.F) {
	for _, seed := range []string{
		"SetUserVar=bunk_notify=" + "eyJ2IjoxLCJldmVudCI6Im5lZWRzLXlvdSJ9",
		"SetUserVar=bunk_notify=",
		"SetUserVar=bunk_notify=e30=",
		"SetUserVar=bunk_notify=////",
		"File=inline=1:AAAA",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		token, r, ok := parseInBand(payload)
		if !ok {
			return
		}
		if _, known := ParseEvent(r.Event.String()); !known || len(token) > wire.MaxToken {
			t.Fatalf("accepted %q: token %q, %+v", payload, token, r)
		}
		s := newTestSession(10, 2)
		s.remoteClient = true
		s.onStatusOSC(inBandOSC, payload)
		if err := checkNotice(s.Status().Notice); err != nil {
			t.Fatalf("%q: %v", payload, err)
		}
	})
}
