// status.go - what a session's agent is doing, for tab labels and switchers.
package session

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentState is what a session's agent appears to be doing.
type AgentState uint8

const (
	AgentNone     AgentState = iota // no known agent runs in the foreground
	AgentWorking                    // output arrived within IdleAfter
	AgentIdle                       // no output for IdleAfter
	AgentNeedsYou                   // the agent asked for attention; not yet acknowledged
	AgentFailed                     // the agent's command failed
)

// IdleAfter is how long an agent counts as working after its last output.
const IdleAfter = 2 * time.Second

// Status describes a session for tab labels and switchers.
type Status struct {
	Agent     string // "claude", "codex", "copilot", …; "" when none is known
	State     AgentState
	Process   string // foreground process name
	Title     string // title set by the application (OSC 0/2)
	Notice    string // text of the latest attention request, if any
	Cwd       string
	Repo      string // git work tree holding Cwd; a linked worktree reports its main repository
	Branch    string // checked-out branch, or a short commit hash when detached
	Container string // e.g. "toolbox fedora"; "" outside containers
	SSHHost   string
}

// Status returns the session's current status. It takes the session lock.
func (s *Session) Status() Status {
	s.Lock()
	defer s.Unlock()
	st := Status{
		Agent:   s.agent,
		State:   s.agentState(time.Now()),
		Process: s.FgProcess,
		Title:   s.Term.Title(),
		Notice:  s.notice,
		Cwd:     s.cwd,
		Repo:    s.repo,
		Branch:  s.branch,
		SSHHost: s.SSHHost,
	}
	if s.ContainerType != "" {
		st.Container = strings.TrimSpace(s.ContainerType + " " + s.ContainerID)
	}
	return st
}

// Acknowledge records that the user has seen the session, clearing a
// pending attention request.
func (s *Session) Acknowledge() {
	s.Lock()
	s.needsYou, s.notice = false, ""
	s.Unlock()
}

// agentState requires the session lock.
func (s *Session) agentState(now time.Time) AgentState {
	switch {
	case s.agent == "":
		return AgentNone
	case s.needsYou:
		return AgentNeedsYou
	case s.failed:
		return AgentFailed
	case now.Sub(s.lastOutput) < IdleAfter:
		return AgentWorking
	}
	return AgentIdle
}

// knownAgents maps a process name, or a path segment of one of its
// arguments, to the agent name shown in tab labels.
var knownAgents = map[string]string{
	"claude":       "claude",
	"codex":        "codex",
	"copilot":      "copilot",
	"gemini":       "gemini",
	"opencode":     "opencode",
	"aider":        "aider",
	"cursor-agent": "cursor",
}

// interpreters run agents written in their languages; for them the script
// path names the agent.
var interpreters = map[string]bool{"node": true, "nodejs": true, "bun": true, "deno": true, "ruby": true}

// agentFromProcess names the agent running as process name with argv, or
// returns "". Agents written in Node or Python run as the interpreter, so
// their script path is checked too (".../@github/copilot/index.js").
func agentFromProcess(name string, argv []string) string {
	if a, ok := knownAgents[name]; ok {
		return a
	}
	if !interpreters[name] && !strings.HasPrefix(name, "python") {
		return ""
	}
	for _, arg := range argv[min(1, len(argv)):] {
		for _, seg := range strings.Split(arg, "/") {
			if a, ok := knownAgents[strings.TrimSuffix(seg, ".js")]; ok {
				return a
			}
		}
	}
	return ""
}

// gitHead finds the git work tree holding dir and returns its repository
// name and checked-out branch, or a short commit hash when HEAD is detached.
func gitHead(dir string) (repo, branch string) {
	if dir == "" {
		return "", ""
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		gitPath := filepath.Join(d, ".git")
		if fi, err := os.Stat(gitPath); err == nil {
			gitDir, repo := gitPath, filepath.Base(d)
			if !fi.IsDir() {
				// A linked worktree or submodule: ".git" holds "gitdir: <path>".
				data, err := os.ReadFile(gitPath)
				if err != nil {
					return repo, ""
				}
				gitDir = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
				if main, _, ok := strings.Cut(gitDir, string(filepath.Separator)+".git"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)); ok {
					repo = filepath.Base(main)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return repo, ""
			}
			h := strings.TrimSpace(string(head))
			if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
				return repo, ref
			}
			return repo, h[:min(7, len(h))]
		}
		if parent := filepath.Dir(d); parent == d {
			return "", ""
		}
	}
}

// refreshStatus updates the agent, cwd and git fields from the foreground
// process. It reports whether anything changed. Called by trackFgProcess,
// without the session lock.
func (s *Session) refreshStatus(shellPid, pgid int, name string, pgidChanged bool) bool {
	s.Lock()
	agent := s.agent
	s.Unlock()
	if pgidChanged {
		agent = ""
		if pgid > 0 {
			agent = agentFromProcess(name, FgProcessCmdline(shellPid))
		}
	}
	cwd := s.Cwd()
	repo, branch := gitHead(cwd)

	s.Lock()
	defer s.Unlock()
	changed := agent != s.agent || cwd != s.cwd || repo != s.repo || branch != s.branch
	if agent != s.agent {
		// A new agent starts with a clean slate.
		s.needsYou, s.failed, s.notice = false, false, ""
	}
	s.agent, s.cwd, s.repo, s.branch = agent, cwd, repo, branch
	return changed
}
