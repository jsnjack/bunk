package notify

import (
	"os"
	"testing"
	"time"

	"bunk/internal/session"
)

// TestEndToEnd runs the whole path: a real session on a PTY gets the socket
// and token in its environment, its child pipes Claude Code's Notification
// input into the notify CLI (this test binary), the server routes the
// report, and Status shows it after StatusChanged.
func TestEndToEnd(t *testing.T) {
	srv, _, _ := startServer(t)
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The child waits for a line, so the session is registered before the
	// hook runs, as when a user submits a prompt.
	script := `read go; printf '%s' "$1" | ` + cliHelperEnv + `=1 "$0" needs-you -; echo "exit=$?"; sleep 30`
	s, err := session.Start(session.Options{
		Cols: 80, Rows: 5, Scrollback: 100,
		Args:         []string{"/bin/sh", "-c", script, exe, claudeNotification},
		NotifySocket: srv.Path(), NotifySession: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 64)
	done := make(chan struct{})
	defer func() {
		close(done)
		s.Close()
		srv.Unregister(token)
	}()
	s.Run(session.Hooks{StatusChanged: func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}}, done)
	srv.Register(token, s)
	if _, err := s.PTY.Write([]byte("go\r")); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		st := s.Status()
		if st.State == session.AgentNeedsYou {
			if st.Notice != "Claude needs your permission to use Bash" || !st.Reported || st.Agent != "agent" {
				t.Fatalf("status %+v", st)
			}
			return
		}
		select {
		case <-changed:
		case <-deadline:
			s.Lock()
			screen := session.CaptureRow(s.Term, 1, 80)
			s.Unlock()
			var b []rune
			for _, g := range screen {
				b = append(b, g.Char)
			}
			t.Fatalf("no needs-you report; status %+v, screen row 1 %q", st, string(b))
		}
	}
}
