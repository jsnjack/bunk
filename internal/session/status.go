// status.go - what a session's agent is doing, for tab labels and switchers.
package session

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	case s.failed:
		return AgentFailed
	case s.needsYou:
		return AgentNeedsYou
	case now.Sub(s.lastOutput) < IdleAfter:
		return AgentWorking
	}
	return AgentIdle
}

// AgentSpec says how to recognise one agent CLI in the foreground.
type AgentSpec struct {
	Name      string   // label shown in tabs
	Processes []string // executable or process names
	Scripts   []string // path fragments of the script when an interpreter runs it
}

// DefaultAgents are recognised out of the box. To support another CLI, add
// an entry here or pass one in Options.Agents (front ends read them from
// their config). Processes match the process name or the base name of an
// executable; Scripts match the script path of Node, Python, Bun, Deno or
// Ruby agents, where the process itself is the interpreter.
var DefaultAgents = []AgentSpec{
	{Name: "claude", Processes: []string{"claude"}, Scripts: []string{"@anthropic-ai/claude-code"}},
	{Name: "copilot", Processes: []string{"copilot"}, Scripts: []string{"@github/copilot"}},
	{Name: "codex", Processes: []string{"codex"}, Scripts: []string{"@openai/codex"}},
	{Name: "gemini", Processes: []string{"gemini"}, Scripts: []string{"@google/gemini-cli"}},
	{Name: "opencode", Processes: []string{"opencode"}, Scripts: []string{"opencode-ai"}},
	{Name: "aider", Processes: []string{"aider"}},
	{Name: "cursor", Processes: []string{"cursor-agent"}},
	{Name: "amp", Processes: []string{"amp"}, Scripts: []string{"@sourcegraph/amp"}},
	{Name: "goose", Processes: []string{"goose"}},
	{Name: "qwen", Processes: []string{"qwen"}, Scripts: []string{"@qwen-code/qwen-code"}},
	{Name: "crush", Processes: []string{"crush"}},
	{Name: "droid", Processes: []string{"droid"}},
	{Name: "auggie", Processes: []string{"auggie"}, Scripts: []string{"@augmentcode/auggie"}},
	{Name: "cline", Processes: []string{"cline"}},
	{Name: "kimi", Processes: []string{"kimi"}},
}

// interpreters run agents written in their languages; for them the script
// path names the agent.
var interpreters = map[string]bool{"node": true, "nodejs": true, "bun": true, "deno": true, "ruby": true}

func isInterpreter(name string) bool {
	return interpreters[name] || strings.HasPrefix(name, "python")
}

// matchAgent returns the name of the first spec that recognises a process
// called name with argv, or "".
func matchAgent(specs []AgentSpec, name string, argv []string) string {
	exe := ""
	if len(argv) > 0 {
		exe = filepath.Base(argv[0])
	}
	for _, sp := range specs {
		for _, p := range sp.Processes {
			if p == name || p == exe {
				return sp.Name
			}
		}
	}
	if !isInterpreter(name) && !isInterpreter(exe) {
		return ""
	}
	// The interpreter's first non-flag argument is the script.
	for _, arg := range argv[min(1, len(argv)):] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(arg), filepath.Ext(arg))
		for _, sp := range specs {
			for _, p := range sp.Processes {
				if p == base {
					return sp.Name
				}
			}
			for _, frag := range sp.Scripts {
				if strings.Contains(arg, frag) {
					return sp.Name
				}
			}
		}
		return ""
	}
	return ""
}

// agentFromProcess names the agent running as process name with argv, from
// the session's specs (DefaultAgents plus Options.Agents), or returns "".
func (s *Session) agentFromProcess(name string, argv []string) string {
	if a := matchAgent(s.agents, name, argv); a != "" {
		return a
	}
	return matchAgent(DefaultAgents, name, argv)
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

// statusOSC is what an OSC sequence says about the agent.
type statusOSC struct {
	notice   string // attention request text
	started  bool   // a command started (OSC 133;C)
	finished bool   // a command finished (OSC 133;D)
	exitCode int
}

// noticeLimit caps the notice text kept for tab labels.
const noticeLimit = 200

// parseStatusOSC reads an attention request (OSC 9, 99, 777) or a command
// boundary (OSC 133;C, 133;D) from an OSC payload without its terminator.
func parseStatusOSC(num int, p []byte) (statusOSC, bool) {
	text := string(p)
	switch num {
	case 9:
		// "9;4;…" and other "9;<digit>;…" forms are ConEmu commands such as
		// progress bars, not notifications.
		if code, _, ok := strings.Cut(text, ";"); ok {
			if _, err := strconv.Atoi(code); err == nil {
				return statusOSC{}, false
			}
		}
		return noticeOSC(text)
	case 777:
		fields := strings.SplitN(text, ";", 3)
		if fields[0] != "notify" || len(fields) < 2 {
			return statusOSC{}, false
		}
		// VTE's shell integration sends this after every long command.
		if fields[1] == "Command completed" {
			return statusOSC{}, false
		}
		if len(fields) == 3 && strings.TrimSpace(fields[2]) != "" {
			return noticeOSC(fields[2])
		}
		return noticeOSC(fields[1])
	case 99:
		// kitty: "metadata;payload"; metadata is key=value pairs joined by ':'.
		meta, body, ok := strings.Cut(text, ";")
		if !ok {
			return statusOSC{}, false
		}
		encoded := false
		for _, kv := range strings.Split(meta, ":") {
			k, v, _ := strings.Cut(kv, "=")
			switch {
			case k == "e" && v == "1":
				encoded = true
			case k == "p" && v != "title" && v != "body":
				return statusOSC{}, false // close, alive, icon, … are not text
			}
		}
		if encoded {
			b, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				return statusOSC{}, false
			}
			body = string(b)
		}
		return noticeOSC(body)
	case 133:
		mark, rest, _ := strings.Cut(text, ";")
		switch mark {
		case "C":
			return statusOSC{started: true}, true
		case "D":
			code, _, _ := strings.Cut(rest, ";")
			n, _ := strconv.Atoi(code)
			return statusOSC{finished: true, exitCode: n}, true
		}
	}
	return statusOSC{}, false
}

// noticeOSC makes a notice from untrusted text: control characters become
// spaces and the result is trimmed and capped.
func noticeOSC(text string) (statusOSC, bool) {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Needs your attention"
	}
	if r := []rune(text); len(r) > noticeLimit {
		text = string(r[:noticeLimit]) + "…"
	}
	return statusOSC{notice: text}, true
}

// recentlyLeft is how long after an agent leaves the foreground a command
// result still counts as the agent's: the shell reports it once its prompt
// returns, which can be after the next process poll.
const recentlyLeft = 2 * time.Second

// onStatusOSC is the OSC scanner's status callback. It runs on the reader
// goroutine, without the session lock.
func (s *Session) onStatusOSC(num int, payload []byte) {
	st, ok := parseStatusOSC(num, payload)
	if !ok {
		return
	}
	s.Lock()
	defer s.Unlock()
	switch {
	case st.notice != "" && s.agent != "" && !s.agentGone:
		s.needsYou, s.notice = true, st.notice
	case st.started:
		if s.agentGone {
			s.agent, s.agentGone = "", false
		}
		s.failed = false
	case st.finished && st.exitCode != 0:
		switch {
		case s.agent != "" && !s.agentGone:
			s.failed = true
		case s.agent == "" && s.lastAgent != "" && time.Since(s.agentLeft) < recentlyLeft:
			// The agent already left; show its failure until the next command.
			s.agent, s.failed, s.agentGone = s.lastAgent, true, true
		default:
			return
		}
	default:
		return
	}
	s.statusDirty = true
}

// onBell is vt10x's bell callback; it runs inside Write, with the session
// lock held. A bell from an agent counts as an attention request.
func (s *Session) onBell() {
	if s.agent != "" && !s.agentGone && !s.needsYou {
		s.needsYou = true
		s.statusDirty = true
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
			agent = s.agentFromProcess(name, FgProcessCmdline(shellPid))
		}
	}
	cwd := s.Cwd()
	repo, branch := gitHead(cwd)

	s.Lock()
	defer s.Unlock()
	if pgidChanged {
		switch {
		case s.agentGone:
			// A new command replaces the failed agent shown since it left.
			s.agentGone = false
		case agent == "" && s.agent != "" && s.failed:
			// Keep showing the failed agent until the next command starts.
			s.agentGone = true
			agent = s.agent
		}
	} else if s.agentGone {
		agent = s.agent
	}
	changed := agent != s.agent || cwd != s.cwd || repo != s.repo || branch != s.branch
	if agent != s.agent {
		if s.agent != "" {
			s.lastAgent, s.agentLeft = s.agent, time.Now()
		}
		// A new agent starts with a clean slate.
		s.needsYou, s.failed, s.notice = false, false, ""
	}
	s.agent, s.cwd, s.repo, s.branch = agent, cwd, repo, branch
	return changed
}
