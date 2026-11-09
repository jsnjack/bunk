package notify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copilotSession names the Copilot CLI session of the event logs below.
const copilotSession = "b6301a8b-3987-431a-9db0-70f87a4e62c9"

// copilotNotification is the input of Copilot CLI's notification hook for
// a permission prompt sent at ts.
func copilotNotification(ts int64) string {
	return fmt.Sprintf(`{"message":"Run command: ls -la","notificationType":"permission_prompt","title":"Permission needed",`+
		`"sessionId":%q,"timestamp":%d,"cwd":"/w"}`, copilotSession, ts)
}

// Lines of a Copilot CLI event log, as Copilot CLI 1.0.107 writes them.
func copilotEvent(typ, data string) string {
	return `{"type":"` + typ + `","data":` + data + `,"id":"e1","timestamp":"2026-11-09T10:49:35.793Z","parentId":"p1"}` + "\n"
}

func copilotRequest(id string) string {
	return copilotEvent("permission.requested", `{"requestId":"`+id+`","permissionRequest":{"kind":"shell","toolCallId":"call_1",`+
		`"fullCommandText":"ls -la","commands":[{"identifier":"ls","readOnly":true}]},"promptRequest":{"kind":"commands","fullCommandText":"ls -la"},`+
		`"assistedApprovalAssessment":{"status":"completed","recommendation":"approve"},"permissionMode":"assisted"}`)
}

func copilotPromptHook(ts int64) string {
	return copilotEvent("hook.start", fmt.Sprintf(`{"hookInvocationId":"h1","hookType":"notification","input":{"message":"Run command: ls -la",`+
		`"notificationType":"permission_prompt","title":"Permission needed","sessionId":%q,"timestamp":%d,"cwd":"/w"}}`, copilotSession, ts))
}

func copilotCompletion(id string) string {
	return copilotEvent("permission.completed", `{"requestId":"`+id+`","toolCallId":"call_1","result":{"kind":"approved"},"decisionSource":"assisted_approval"}`)
}

// copilotOutput is a tool's output that quotes the events above.
func copilotOutput(size int) string {
	const quoted = `{"type":"permission.requested","data":{"requestId":"quoted"}} `
	text := strings.Repeat(quoted, size/len(quoted)+1)
	return copilotEvent("tool.execution_complete", fmt.Sprintf(`{"toolCallId":"call_0","success":true,"result":{"content":%q}}`, text[:size]))
}

// copilotHome writes a Copilot CLI home whose session has the event log
// lines, and returns Dirs that find it through COPILOT_HOME.
func copilotHome(t *testing.T, lines ...string) (Dirs, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "session-state", copilotSession)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, copilotEventLog)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	return Dirs{Home: t.TempDir(), Getenv: func(k string) string {
		if k == "COPILOT_HOME" {
			return home
		}
		return ""
	}}, path
}

// TestCopilotDecided: a permission prompt counts as decided once the event
// log shows its notification and no open request, and as asking the user
// otherwise.
func TestCopilotDecided(t *testing.T) {
	t.Parallel()
	const ts = 1794221375793
	in := copilotNotification(ts)
	for _, tc := range []struct {
		name  string
		input string
		log   []string
		want  bool
	}{
		{"decided by Assisted Permissions", in, []string{copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1")}, true},
		{"decided before an earlier request", in,
			[]string{copilotRequest("r0"), copilotPromptHook(ts - 9000), copilotCompletion("r0"), copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1")}, true},
		{"asks the user", in, []string{copilotRequest("r1"), copilotPromptHook(ts)}, false},
		{"another request still open", in,
			[]string{copilotRequest("r0"), copilotPromptHook(ts - 9000), copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1")}, false},
		{"log not yet written up to the notification", in, []string{copilotRequest("r0"), copilotPromptHook(ts - 9000), copilotCompletion("r0")}, false},
		{"notification being written", in, []string{copilotRequest("r1"), copilotPromptHook(ts)[:120]}, false},
		{"quoted events do not count", in,
			[]string{copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1"), copilotOutput(4096)}, true},
		{"decided at the end of a long log", in,
			[]string{copilotOutput(copilotScan), copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1")}, true},
		{"another notification", strings.Replace(in, copilotPrompt, "elicitation_dialog", 1),
			[]string{copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1")}, false},
		{"no timestamp", strings.Replace(in, fmt.Sprint(ts), "0", 1), []string{copilotRequest("r1"), copilotPromptHook(0), copilotCompletion("r1")}, false},
		{"session ID outside the home", strings.Replace(in, copilotSession, "../"+copilotSession, 1),
			[]string{copilotRequest("r1"), copilotPromptHook(ts), copilotCompletion("r1")}, false},
		{"Claude Code notification", claudeNotification, nil, false},
		{"plain text", "Allow npm install?", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, _ := copilotHome(t, tc.log...)
			if got := copilotDecided(tc.input, d, 3*copilotPoll); got != tc.want {
				t.Fatalf("decided %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCopilotDecidedWaits: the decision may reach the event log after the
// notification starts; the user's answer takes longer than the wait.
func TestCopilotDecidedWaits(t *testing.T) {
	t.Parallel()
	const ts = 1794221375793
	d, path := copilotHome(t, copilotRequest("r1"))
	written := make(chan struct{})
	go func() {
		defer close(written)
		for _, line := range []string{copilotPromptHook(ts), copilotCompletion("r1")} {
			time.Sleep(2 * copilotPoll)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := f.WriteString(line); err != nil {
				t.Error(err)
			}
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	start := time.Now()
	if !copilotDecided(copilotNotification(ts), d, 10*time.Second) {
		t.Fatal("a request decided during the wait counts as asking the user")
	}
	<-written
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("waited %v after the decision", took)
	}

	d, _ = copilotHome(t, copilotRequest("r1"), copilotPromptHook(ts))
	start = time.Now()
	if copilotDecided(copilotNotification(ts), d, 4*copilotPoll) {
		t.Fatal("an open request counts as decided")
	}
	if took := time.Since(start); took < 4*copilotPoll {
		t.Fatalf("gave up after %v", took)
	}

	missing := Dirs{Home: t.TempDir()}
	start = time.Now()
	if copilotDecided(copilotNotification(ts), missing, 10*time.Second) {
		t.Fatal("a session without an event log counts as decided")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("waited %v for an event log that does not exist", took)
	}
}
