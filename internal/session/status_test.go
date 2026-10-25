package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
