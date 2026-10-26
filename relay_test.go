package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
	"bunk/internal/vt10x"
)

// relayPane returns a pane without a PTY whose agent reports state.
func relayPane(ev session.Event, notice string) *Pane {
	p := &Pane{Session: &session.Session{
		Term: vt10x.New(vt10x.WithSize(20, 2)), Cmd: &exec.Cmd{},
		ScrollbackLines: 10, Scrollback: session.Ring{MaxLines: 10},
	}, w: 21, h: 2}
	if ev != 0 {
		p.Notify(ev, notice)
	}
	return p
}

// relayed decodes the report in a relay's sequence; ok is false for nil.
func relayed(t *testing.T, seq []byte) (wire.Report, bool) {
	t.Helper()
	if seq == nil {
		return wire.Report{}, false
	}
	payload, ok := bytes.CutPrefix(seq, []byte("\x1b]1337;"))
	payload, ok2 := bytes.CutSuffix(payload, []byte("\a"))
	if !ok || !ok2 {
		t.Fatalf("relay wrote %q", seq)
	}
	line, ok := wire.FromUserVar(payload)
	if !ok {
		t.Fatalf("not bunk's user variable: %q", payload)
	}
	r, err := wire.Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestNewAgentRelay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		vars  map[string]string
		relay bool
		host  bool
	}{
		{"plain terminal", nil, false, false},
		{"inside bunk-gui", map[string]string{session.EnvNotifySession: "tok"}, true, false},
		{"over ssh", map[string]string{"SSH_CONNECTION": "10.0.0.2 1 10.0.0.1 22"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentRelay(env(tc.vars))
			if (r != nil) != tc.relay || (r != nil && (r.host != "") != tc.host) {
				t.Fatalf("newAgentRelay = %+v, want relay %v, host %v", r, tc.relay, tc.host)
			}
			if r != nil && r.token != tc.vars[session.EnvNotifySession] {
				t.Fatalf("token %q", r.token)
			}
		})
	}
	var none *agentRelay
	if none.update([]*Pane{relayPane(session.EventNeedsYou, "x")}, nil) != nil || none.finish() != nil {
		t.Fatal("a nil relay wrote a report")
	}
}

// TestMostUrgent: needs you, done, failed, working, idle; ties go to the
// active pane, then layout order.
func TestMostUrgent(t *testing.T) {
	t.Parallel()
	needs, done, failed := relayPane(session.EventNeedsYou, "a"), relayPane(session.EventDone, "b"), relayPane(session.EventFailed, "c")
	working, idle, shell := relayPane(session.EventWorking, ""), relayPane(session.EventIdle, ""), relayPane(0, "")
	needs2 := relayPane(session.EventNeedsYou, "second")
	for _, tc := range []struct {
		name   string
		panes  []*Pane
		active *Pane
		notice string
		state  session.AgentState
	}{
		{"needs you first", []*Pane{idle, done, needs, failed}, idle, "a", session.AgentNeedsYou},
		{"done before failed", []*Pane{failed, working, done}, nil, "b", session.AgentDone},
		{"failed before working", []*Pane{working, failed, idle}, nil, "c", session.AgentFailed},
		{"working before idle", []*Pane{idle, working}, nil, "", session.AgentWorking},
		{"tie: layout order", []*Pane{needs, needs2}, nil, "a", session.AgentNeedsYou},
		{"tie: the active pane", []*Pane{needs, needs2}, needs2, "second", session.AgentNeedsYou},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := mostUrgent(tc.panes, tc.active)
			if !ok || st.State != tc.state || st.Notice != tc.notice {
				t.Fatalf("mostUrgent = %+v, %v; want %v %q", st, ok, tc.state, tc.notice)
			}
		})
	}
	if st, ok := mostUrgent([]*Pane{shell}, shell); ok {
		t.Fatalf("a shell without an agent: %+v", st)
	}
}

// TestAgentRelay: one report per change of the most urgent pane, a clear
// once no agent is left and on exit, and nothing before the first agent.
func TestAgentRelay(t *testing.T) {
	t.Parallel()
	r := newAgentRelay(env(map[string]string{session.EnvNotifySession: "tok"}))
	shell, agent := relayPane(0, ""), relayPane(0, "")
	panes := []*Pane{shell, agent}
	if seq := r.update(panes, shell); seq != nil {
		t.Fatalf("no agent yet, but the relay wrote %q", seq)
	}
	if seq := r.finish(); seq != nil {
		t.Fatalf("nothing to clear on exit, but the relay wrote %q", seq)
	}

	agent.Notify(session.EventNeedsYou, "Allow npm install?")
	rep, ok := relayed(t, r.update(panes, shell))
	if !ok || rep.Session != "tok" || rep.Event != "needs-you" || rep.Message != "Allow npm install?" || rep.Agent != "agent" || rep.Host != "" {
		t.Fatalf("first report %+v", rep)
	}
	if seq := r.update(panes, shell); seq != nil {
		t.Fatalf("an unchanged pane was reported again: %q", seq)
	}

	agent.Acknowledge()
	rep, ok = relayed(t, r.update(panes, agent))
	if !ok || rep.Event != "clear" {
		t.Fatalf("after the acknowledgement: %+v, %v", rep, ok)
	}

	agent.Notify(session.EventWorking, "")
	if rep, ok = relayed(t, r.update(panes, agent)); !ok || rep.Event != "working" {
		t.Fatalf("working: %+v, %v", rep, ok)
	}
	if rep, ok = relayed(t, r.finish()); !ok || rep.Event != "clear" || rep.Session != "tok" {
		t.Fatalf("on exit: %+v, %v", rep, ok)
	}
	if seq := r.finish(); seq != nil {
		t.Fatalf("a second clear: %q", seq)
	}
}

// TestAgentRelayOverSSH: a relay on a host reached over ssh names the
// host and wraps its reports for tmux.
func TestAgentRelayOverSSH(t *testing.T) {
	t.Parallel()
	r := newAgentRelay(env(map[string]string{"SSH_CONNECTION": "x", "TMUX": "/tmp/tmux-1000/default,1,0"}))
	seq := r.update([]*Pane{relayPane(session.EventDone, "Fixed")}, nil)
	inner, ok := bytes.CutPrefix(seq, []byte("\x1bPtmux;"))
	inner, ok2 := bytes.CutSuffix(inner, []byte("\x1b\\"))
	if !ok || !ok2 {
		t.Fatalf("not wrapped for tmux: %q", seq)
	}
	rep, _ := relayed(t, bytes.ReplaceAll(inner, []byte("\x1b\x1b"), []byte("\x1b")))
	if rep.Session != "" || rep.Event != "done" || rep.Host == "" {
		t.Fatalf("report %+v", rep)
	}
}

// TestNestedTUIRelay runs bunk's TUI inside a session that holds a token,
// as in a bunk-gui tab: "bunk notify" in a TUI pane reports to the TUI,
// and the TUI relays the pane to the outer session. Typing into the pane
// acknowledges it, and the outer session clears. When bunk exits, the
// outer session keeps nothing of it.
func TestNestedTUIRelay(t *testing.T) {
	bin := bunkOnPath(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	t.Setenv("BUNK", "")
	t.Setenv("SSH_CONNECTION", "")
	runtime, err := os.MkdirTemp("", "bn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runtime); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	config := filepath.Join(runtime, "config.toml") // defaults, whatever the user's file says
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := session.Start(session.Options{
		Cols: 80, Rows: 12, Scrollback: 100, Args: []string{filepath.Join(bin, "bunk"), "--config", config},
		NotifySession: "tok", KnownToken: func(token string) bool { return token == "tok" },
		Timing: session.Timing{Poll: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		s.Close()
	})
	s.Run(session.Hooks{Closed: func() { close(closed) }}, done)
	screen := func() string {
		s.Lock()
		defer s.Unlock()
		var rows []string
		for y := range 12 {
			var b strings.Builder
			for _, g := range session.CaptureRow(s.Term, y, 80) {
				if g.Width >= 0 && g.Char != 0 {
					b.WriteRune(g.Char)
				}
			}
			rows = append(rows, strings.TrimRight(b.String(), " "))
		}
		return strings.Join(rows, "\n")
	}
	wait := func(what string, ok func(session.Status) bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok(s.Status()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: status %+v; screen:\n%s", what, s.Status(), screen())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("the TUI's shell prompt", func(session.Status) bool { return strings.Contains(screen(), "$") })
	if err := s.WriteInput([]byte("bunk notify needs-you Allow npm install?\r")); err != nil {
		t.Fatal(err)
	}
	wait("the relayed report", func(st session.Status) bool { return st.State == session.AgentNeedsYou })
	if st := s.Status(); st.Notice != "Allow npm install?" || !st.Reported || st.Agent != "agent" || st.Host != "" {
		t.Fatalf("relayed status %+v", st)
	}

	// A key typed into the TUI pane acknowledges it there.
	if err := s.WriteInput([]byte("x")); err != nil {
		t.Fatal(err)
	}
	wait("the relayed clear", func(st session.Status) bool { return st.State == session.AgentNone && !st.Reported })

	if err := s.WriteInput([]byte("\x15bunk notify done Finished\r")); err != nil {
		t.Fatal(err)
	}
	wait("a second report", func(st session.Status) bool { return st.State == session.AgentDone })
	if err := s.WriteInput([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatalf("bunk did not exit; screen:\n%s", screen())
	}
	if st := s.Status(); st.State != session.AgentNone || st.Reported || st.Agent != "" {
		t.Fatalf("after bunk exited: %+v", st)
	}
}
