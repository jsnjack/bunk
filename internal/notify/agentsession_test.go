package notify

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"bunk/internal/session"
)

// reportsTarget records whole reports, as *session.Session takes them.
type reportsTarget chan session.Report

func (r reportsTarget) Notify(ev session.Event, text string) {
	r <- session.Report{Event: ev, Message: text}
}

func (r reportsTarget) NotifyReport(rep session.Report) { r <- rep }

func nextReport(t *testing.T, ch reportsTarget) session.Report {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no report within 5 s")
	}
	return session.Report{}
}

// A hook's session_id travels from the CLI through the socket to the
// session; an ID that could do harm typed into a shell is dropped.
func TestAgentSessionReachesTarget(t *testing.T) {
	srv, _, _ := startServer(t)
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	target := make(reportsTarget, 8)
	srv.Register(token, target)
	env := []string{"HOME=/home/u", EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}

	for _, tc := range []struct {
		name, stdin, want string
	}{
		{"claude", `{"session_id":"0f3c2a6e-5d1b-4c3e-9a77-2b1d0e9f8c41","prompt":"hi"}`, "0f3c2a6e-5d1b-4c3e-9a77-2b1d0e9f8c41"},
		{"copilot", `{"sessionId":"abc_123.x","prompt":"hi"}`, "abc_123.x"},
		{"shell characters", `{"session_id":"x; rm -rf ~"}`, ""},
		{"too long", `{"session_id":"` + strings.Repeat("a", 129) + `"}`, ""},
		{"not JSON", "plain text", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runCLI("bunk", []string{"working", "-"}, env, strings.NewReader(tc.stdin), &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if got := nextReport(t, target); got.Event != session.EventWorking || got.AgentSession != tc.want {
				t.Errorf("report %+v, want working with agent session %q", got, tc.want)
			}
		})
	}

	// A client that writes the field itself gets the same check.
	c, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	line := `{"v":1,"session":"` + token + `","event":"idle","agent_session":"$(id)"}` + "\n"
	if _, err := c.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := nextReport(t, target); got.Event != session.EventIdle || got.AgentSession != "" {
		t.Errorf("raw report %+v, want idle without an agent session", got)
	}
}

// A needs-you from the PreToolUse of a question tool is held: its
// PostToolUse reports the answer. Other needs-you reports are not.
func TestNeedsYouHeld(t *testing.T) {
	srv, _, _ := startServer(t)
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	target := make(reportsTarget, 8)
	srv.Register(token, target)
	env := []string{"HOME=/home/u", EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}
	for _, tc := range []struct {
		name, stdin string
		held        bool
	}{
		{"AskUserQuestion", `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which DB?"}]}}`, true},
		{"ExitPlanMode", `{"hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan"}}`, true},
		{"permission prompt", `{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash"}`, false},
		{"other tool", `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runCLI("bunk", []string{"needs-you", "-"}, env, strings.NewReader(tc.stdin), &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if got := nextReport(t, target); got.Event != session.EventNeedsYou || got.Held != tc.held {
				t.Errorf("report %+v, want held %v", got, tc.held)
			}
		})
	}
}
