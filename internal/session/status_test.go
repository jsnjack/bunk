package session

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bunk/internal/vt10x"
)

func TestAgentFromProcess(t *testing.T) {
	s := &Session{agents: []AgentSpec{{Name: "mine", Processes: []string{"my-agent"}, Scripts: []string{"@me/agent"}}}}
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"claude", []string{"claude", "--resume"}, "claude"},
		{"codex", []string{"codex"}, "codex"},
		{"node", []string{"node", "/usr/local/bin/copilot"}, "copilot"},
		{"node", []string{"node", "/usr/lib/node_modules/@github/copilot/index.js"}, "copilot"},
		{"node", []string{"node", "--no-warnings", "/opt/lib/@google/gemini-cli/dist/index.js"}, "gemini"},
		{"MainThread", []string{"/usr/bin/node", "/home/u/.npm/bin/copilot"}, "copilot"},
		{"python3", []string{"python3", "/home/u/.local/bin/aider", "--model", "x"}, "aider"},
		{"cursor-agent", []string{"cursor-agent"}, "cursor"},
		{"my-agent", []string{"my-agent"}, "mine"},
		{"node", []string{"node", "/srv/@me/agent/cli.js"}, "mine"},
		{"node", []string{"node", "server.js"}, ""},
		{"node", []string{"node", "server.js", "--dir", "/tmp/claude"}, ""},
		{"less", []string{"less", "/tmp/codex"}, ""},
		{"vim", []string{"vim", "CLAUDE.md"}, ""},
		{"bash", nil, ""},
	} {
		if got := s.agentFromProcess(tc.name, tc.argv); got != tc.want {
			t.Errorf("agentFromProcess(%q, %q) = %q, want %q", tc.name, tc.argv, got, tc.want)
		}
	}
}

func TestGitHead(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mainRepo := filepath.Join(root, "bunk")
	write(filepath.Join(mainRepo, ".git", "HEAD"), "ref: refs/heads/shared-core\n")
	write(filepath.Join(mainRepo, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/bunk-gui\n")
	linked := filepath.Join(root, "elsewhere", "wt")
	write(filepath.Join(linked, ".git"), "gitdir: "+filepath.Join(mainRepo, ".git", "worktrees", "wt")+"\n")
	detached := filepath.Join(root, "detached")
	write(filepath.Join(detached, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	if err := os.MkdirAll(filepath.Join(mainRepo, "internal", "session"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ dir, repo, branch string }{
		{mainRepo, "bunk", "shared-core"},
		{filepath.Join(mainRepo, "internal", "session"), "bunk", "shared-core"},
		{linked, "bunk", "bunk-gui"},
		{detached, "detached", "0123456"},
		{root, "", ""},
		{"", "", ""},
	} {
		repo, branch := gitHead(tc.dir)
		if repo != tc.repo || branch != tc.branch {
			t.Errorf("gitHead(%q) = %q, %q; want %q, %q", tc.dir, repo, branch, tc.repo, tc.branch)
		}
	}
}

func TestStatusStates(t *testing.T) {
	s := newTestSession(20, 2)
	if st := s.Status(); st.State != AgentNone {
		t.Fatalf("no agent: state %v, want AgentNone", st.State)
	}
	s.agent = "claude"
	s.lastOutput = time.Now()
	if st := s.Status(); st.State != AgentWorking || st.Agent != "claude" {
		t.Fatalf("recent output: %+v, want claude working", st)
	}
	s.lastOutput = time.Now().Add(-IdleAfter - time.Millisecond)
	if st := s.Status(); st.State != AgentIdle {
		t.Fatalf("quiet: state %v, want AgentIdle", st.State)
	}
	s.needsYou, s.notice = true, "Claude needs your permission"
	if st := s.Status(); st.State != AgentNeedsYou || st.Notice == "" {
		t.Fatalf("attention: %+v, want needs-you with notice", st)
	}
	s.Acknowledge()
	if st := s.Status(); st.State != AgentIdle || st.Notice != "" {
		t.Fatalf("after Acknowledge: %+v, want idle without notice", st)
	}
	s.ContainerType, s.ContainerID = "toolbox", "fedora"
	if st := s.Status(); st.Container != "toolbox fedora" {
		t.Fatalf("container label %q", st.Container)
	}
}

func TestParseStatusOSC(t *testing.T) {
	long := strings.Repeat("x", noticeLimit+50)
	for _, tc := range []struct {
		num     int
		payload string
		want    statusOSC
		ok      bool
	}{
		{9, "Claude needs your permission to use Bash", statusOSC{notice: "Claude needs your permission to use Bash"}, true},
		{9, "4;1;50", statusOSC{}, false},
		{9, "", statusOSC{notice: "Needs your attention"}, true},
		{9, "line1\nline2\x07", statusOSC{notice: "line1 line2"}, true},
		{9, long, statusOSC{notice: strings.Repeat("x", noticeLimit-1) + "…"}, true},
		{777, "notify;Codex;Approval requested", statusOSC{notice: "Approval requested"}, true},
		{777, "notify;Agent waiting", statusOSC{notice: "Agent waiting"}, true},
		{777, "notify;Command completed;make test", statusOSC{}, false},
		{777, "precmd", statusOSC{}, false},
		{99, "i=1:d=0;Hello", statusOSC{notice: "Hello"}, true},
		{99, "e=1:p=body;" + base64.StdEncoding.EncodeToString([]byte("Done")), statusOSC{notice: "Done"}, true},
		{99, "i=1:p=close;", statusOSC{}, false},
		{133, "D;1", statusOSC{finished: true, exitCode: 1}, true},
		{133, "D;0;aid=7", statusOSC{finished: true}, true},
		{133, "D", statusOSC{finished: true}, true},
		{133, "C", statusOSC{started: true}, true},
		{133, "A", statusOSC{}, false},
	} {
		got, ok := parseStatusOSC(tc.num, []byte(tc.payload))
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseStatusOSC(%d, %q) = %+v, %v; want %+v, %v", tc.num, tc.payload, got, ok, tc.want, tc.ok)
		}
	}
}

// TestStatusAttentionFlow drives a session through notifications, bells,
// acknowledgement and failures the way ReadLoop and the process tracker do.
func TestStatusAttentionFlow(t *testing.T) {
	s := newTestSession(40, 3)
	s.Term = vt10x.New(vt10x.WithSize(40, 3), vt10x.WithBellCallback(s.onBell))
	s.oscScan.status = s.onStatusOSC
	scan := func(seq string) { s.oscScan.Scan([]byte(seq), func([]byte) {}) }
	feed := func(text string) {
		s.Lock()
		s.Feed([]byte(text))
		s.Unlock()
	}
	expect := func(step string, agent string, state AgentState, notice string) {
		t.Helper()
		st := s.Status()
		if st.Agent != agent || st.State != state || st.Notice != notice {
			t.Fatalf("%s: got agent %q state %v notice %q; want %q %v %q", step, st.Agent, st.State, st.Notice, agent, state, notice)
		}
	}

	scan("\x1b]9;nobody is listening\x07")
	feed("\a")
	expect("no agent", "", AgentNone, "")

	s.agent = "claude"
	scan("\x1b]9;Claude needs your permission\x07")
	expect("notification", "claude", AgentNeedsYou, "Claude needs your permission")
	if !s.statusDirty {
		t.Fatal("notification did not mark status for reporting")
	}
	s.Acknowledge()
	expect("acknowledged", "claude", AgentIdle, "")

	feed("\a")
	expect("bell", "claude", AgentNeedsYou, "")
	s.Acknowledge()
	scan("\x1b]777;notify;Command completed;make\x07")
	expect("shell integration notice", "claude", AgentIdle, "")

	scan("\x1b]133;D;1\x07")
	expect("failure", "claude", AgentFailed, "")
	s.refreshStatus(0, 0, "bash", true)
	expect("failed agent left", "claude", AgentFailed, "")
	s.refreshStatus(0, 0, "bash", false)
	expect("next poll", "claude", AgentFailed, "")
	scan("\x1b]133;C\x07")
	expect("next command", "", AgentNone, "")

	s.agent = "codex"
	s.refreshStatus(0, 0, "bash", true)
	expect("codex left", "", AgentNone, "")
	scan("\x1b]133;D;2\x07")
	expect("late failure report", "codex", AgentFailed, "")
	s.refreshStatus(0, 0, "ls", true)
	expect("new command after late failure", "", AgentNone, "")
}

// TestReadLoopReportsAttention checks that an attention request reaches
// Hooks.StatusChanged and Status through the real reader.
func TestReadLoopReportsAttention(t *testing.T) {
	s := newTestSession(40, 3)
	s.oscScan.status = s.onStatusOSC
	s.agent = "copilot"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.PTY = r
	changed := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() {
		s.ReadLoop(Hooks{StatusChanged: func() { changed <- struct{}{} }})
		close(done)
	}()
	if _, err := w.Write([]byte("\x1b]9;Allow npm install?\x07")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for s.Status().State != AgentNeedsYou {
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("status = %+v, want needs-you", s.Status())
		}
	}
	if st := s.Status(); st.Notice != "Allow npm install?" {
		t.Fatalf("notice = %q", st.Notice)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
}
