package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bunk/internal/notify/wire"
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
	if err := s.WriteInput([]byte("go\r")); err != nil {
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

// screenText returns the session's screen rows joined by newlines.
func screenText(s *session.Session, cols, rows int) string {
	s.Lock()
	defer s.Unlock()
	var b strings.Builder
	for y := range rows {
		for _, g := range session.CaptureRow(s.Term, y, cols) {
			if g.Width >= 0 && g.Char != 0 {
				b.WriteRune(g.Char)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// runSession starts o on a real PTY and returns the session and a channel
// that StatusChanged signals. The session closes when the test ends.
func runSession(t *testing.T, o session.Options) (*session.Session, chan struct{}) {
	t.Helper()
	s, err := session.Start(o)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 64)
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		s.Close()
	})
	s.Run(session.Hooks{
		StatusChanged: func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
		Output: func([]byte, bool, bool) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	}, done)
	return s, changed
}

// waitFor waits until ok accepts the session's status, failing after 10 s
// with the screen.
func waitFor(t *testing.T, s *session.Session, changed chan struct{}, what string, ok func(session.Status) bool) session.Status {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if st := s.Status(); ok(st) {
			return st
		}
		select {
		case <-changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("%s: status %+v; screen:\n%s", what, s.Status(), screenText(s, 80, 10))
		}
	}
}

// TestInBandOverSSH plays a host reached over ssh: a fake ssh runs a fake
// claude, whose hook starts in a session of its own with stdio on
// /dev/null, as agents may start hooks, and runs the notify CLI without a
// socket or token. The report reaches the pane through the agent's
// terminal and names the agent, host, repo and branch.
func TestInBandOverSSH(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".git", "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"ssh": "#!/bin/sh\n\"$(dirname \"$0\")/claude\"\n",
		"claude": "#!/bin/sh\necho ready\nread go\n" +
			"setsid -w env " + cliHelperEnv + "=1 SSH_CONNECTION='10.0.0.2 50000 10.0.0.1 22' \"$EXE\" needs-you 'Allow rm?' </dev/null >/dev/null 2>&1\n" +
			"sleep 30\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, changed := runSession(t, session.Options{
		Cols: 80, Rows: 10, Scrollback: 100, Dir: proj, Env: []string{"EXE=" + exe},
		Args: []string{filepath.Join(bin, "ssh"), "build01"}, Timing: session.Timing{Poll: 20 * time.Millisecond},
	})
	waitFor(t, s, changed, "fake ssh in the foreground", func(st session.Status) bool {
		return st.Process == "ssh" && strings.Contains(screenText(s, 80, 10), "ready")
	})
	if err := s.WriteInput([]byte("go\r")); err != nil {
		t.Fatal(err)
	}
	st := waitFor(t, s, changed, "in-band report", func(st session.Status) bool { return st.State == session.AgentNeedsYou })
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	want := session.Status{Agent: "claude", State: session.AgentNeedsYou, Process: "ssh", Notice: "Allow rm?", Reported: true,
		Cwd: proj, Repo: "proj", Branch: "feature", Host: host}
	// The fake ssh is a script, so its command line starts with /bin/sh and
	// does not name the destination as ssh's does.
	st.Title, st.SSHHost = "", ""
	if st != want {
		t.Fatalf("status %+v\nwant   %+v", st, want)
	}
	if text := screenText(s, 80, 10); strings.Contains(text, "SetUserVar") || strings.Contains(text, wire.UserVar) {
		t.Fatalf("the report reached the screen:\n%s", text)
	}
}

// TestInBandThroughTmux runs the notify CLI inside a private tmux server in
// a session: with allow-passthrough the report passes tmux, without it
// tmux drops the report and shows nothing of it.
func TestInBandThroughTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"all", "off"} {
		t.Run("allow-passthrough "+mode, func(t *testing.T) {
			// tmux socket paths must stay under 108 bytes.
			dir, err := os.MkdirTemp("", "bt")
			if err != nil {
				t.Fatal(err)
			}
			sock := filepath.Join(dir, "s")
			t.Cleanup(func() {
				if out, err := exec.Command("tmux", "-S", sock, "kill-server").CombinedOutput(); err != nil {
					t.Logf("tmux kill-server: %v %s", err, out)
				}
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			})
			script := `echo ready; read go; ` + cliHelperEnv + `=1 "$EXE" done Finished; echo sent; sleep 30`
			s, changed := runSession(t, session.Options{
				Cols: 80, Rows: 10, Scrollback: 100,
				Env:           []string{"EXE=" + exe, "SHELL=/bin/sh", "HOME=" + dir},
				Args:          []string{"tmux", "-f", "/dev/null", "-S", sock, "new-session", "-x", "80", "-y", "9", script, ";", "set", "-g", "allow-passthrough", mode},
				NotifySession: "tok",
				KnownToken:    func(token string) bool { return token == "tok" },
			})
			waitFor(t, s, changed, "tmux pane ready", func(session.Status) bool {
				return strings.Contains(screenText(s, 80, 10), "ready")
			})
			if err := s.WriteInput([]byte("go\r")); err != nil {
				t.Fatal(err)
			}
			// tmux passes a report on as it reads it, before it draws the
			// text after it, so "sent" on screen means the report is in.
			waitFor(t, s, changed, "hook finished", func(session.Status) bool {
				return strings.Contains(screenText(s, 80, 10), "sent")
			})
			st := s.Status()
			if passed := st.State == session.AgentDone && st.Notice == "Finished" && st.Reported; passed != (mode == "all") {
				t.Fatalf("passthrough %s: status %+v", mode, st)
			}
			if text := screenText(s, 80, 10); strings.Contains(text, wire.UserVar) {
				t.Fatalf("the report reached the screen:\n%s", text)
			}
		})
	}
}
