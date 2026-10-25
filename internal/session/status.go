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
	AgentDone                       // the agent reported a finished turn; not yet acknowledged
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
	Reported  bool   // State and Notice come from the agent's own report (Notify)
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
	v := s.view(time.Now())
	st := Status{
		Agent:    v.agent,
		State:    v.state,
		Process:  s.FgProcess,
		Title:    s.Term.Title(),
		Notice:   v.notice,
		Reported: v.reported,
		Cwd:      s.cwd,
		Repo:     s.repo,
		Branch:   s.branch,
		SSHHost:  s.SSHHost,
	}
	if s.ContainerType != "" {
		st.Container = strings.TrimSpace(s.ContainerType + " " + s.ContainerID)
	}
	return st
}

// Acknowledge records that the user has seen the session. It clears a
// pending attention request and the agent's explicit report, so inference
// shows the session until the agent reports again. It runs no hook itself:
// the next output or process tick (at most a second later) announces the
// change through Hooks.StatusChanged, so callers may hold their own locks.
func (s *Session) Acknowledge() {
	s.Lock()
	s.needsYou, s.notice = false, ""
	s.report = agentReport{}
	s.Unlock()
}

// agentView is the part of Status that agent reports and inference decide.
type agentView struct {
	agent    string
	state    AgentState
	notice   string
	reported bool
}

// reportingAgent labels a session whose agent reported its state but was
// not recognised from the foreground process (an unknown CLI, or one
// started through a wrapper).
const reportingAgent = "agent"

// takeAgentChange reports whether the agent view differs from the one
// StatusChanged last announced, and records it as announced. Every path
// that runs StatusChanged for the agent's state goes through it, so each
// change is announced once. It requires the session lock.
func (s *Session) takeAgentChange(now time.Time) bool {
	v := s.view(now)
	if v == s.shown {
		return false
	}
	s.shown = v
	return true
}

// agentChangedNow is takeAgentChange for callers without the lock, such as
// ReadLoop's idle timer.
func (s *Session) agentChangedNow() bool {
	s.Lock()
	defer s.Unlock()
	return s.takeAgentChange(time.Now())
}

// view requires the session lock. An explicit report takes priority over
// inference.
func (s *Session) view(now time.Time) agentView {
	if s.report.state != AgentNone {
		agent := s.agent
		if agent == "" {
			agent = reportingAgent
		}
		return agentView{agent: agent, state: s.report.state, notice: s.report.notice, reported: true}
	}
	return agentView{agent: s.agent, state: s.agentState(now), notice: s.notice}
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

// defaultNotice is the notice of an attention request without text.
const defaultNotice = "Needs your attention"

// noticeOSC makes a notice from an attention request's text.
func noticeOSC(text string) (statusOSC, bool) {
	return statusOSC{notice: cleanNotice(text, defaultNotice)}, true
}

// cleanNotice makes a notice from untrusted text: control characters become
// spaces and the result is trimmed and capped. Empty text gives fallback.
func cleanNotice(text, fallback string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	if r := []rune(text); len(r) > noticeLimit {
		text = string(r[:noticeLimit-1]) + "…"
	}
	return text
}

// recentlyLeft is how long after an agent leaves the foreground a command
// result still counts as the agent's: the shell reports it once its prompt
// returns, which can be after the next process poll.
const recentlyLeft = 2 * time.Second

// onStatusOSC is the OSC scanner's status callback. It runs on the reader
// goroutine, without the session lock. ReadLoop announces the change after
// the chunk.
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
		}
	}
}

// onBell is vt10x's bell callback; it runs inside Write, with the session
// lock held. A bell from an agent counts as an attention request.
func (s *Session) onBell() {
	if s.agent != "" && !s.agentGone {
		s.needsYou = true
	}
}

// refreshStatus updates the agent, cwd and git fields from the foreground
// process. It reports whether anything changed, including an agent state
// that changed with time alone, such as working turning idle without output.
// Called by trackFgProcess on every tick, without the session lock.
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
	if pgidChanged && s.report.state != AgentNone && (pgid <= 0 || pgid != s.report.pgid) {
		// The process group that reported has left the foreground.
		s.report = agentReport{}
		changed = true
	}
	if agent != s.agent {
		if s.agent != "" {
			s.lastAgent, s.agentLeft = s.agent, time.Now()
		}
		// A new agent starts with a clean slate.
		s.needsYou, s.failed, s.notice = false, false, ""
	}
	s.agent, s.cwd, s.repo, s.branch = agent, cwd, repo, branch
	if s.takeAgentChange(time.Now()) {
		changed = true
	}
	return changed
}

// Event is a state an agent reports about itself from its hooks, through
// internal/notify, instead of bunk inferring it.
type Event uint8

const (
	EventWorking  Event = iota + 1 // the agent started a turn
	EventIdle                      // the agent waits for a prompt
	EventNeedsYou                  // the agent needs an answer or an approval
	EventDone                      // the agent finished its turn
	EventFailed                    // the agent's turn failed
	EventClear                     // forget the agent's reports
)

// eventNames are the events' names on the wire and on the command line.
var eventNames = [...]string{
	EventWorking:  "working",
	EventIdle:     "idle",
	EventNeedsYou: "needs-you",
	EventDone:     "done",
	EventFailed:   "failed",
	EventClear:    "clear",
}

// String returns the event's wire name, such as "needs-you".
func (e Event) String() string {
	if int(e) < len(eventNames) && eventNames[e] != "" {
		return eventNames[e]
	}
	return "event(" + strconv.Itoa(int(e)) + ")"
}

// ParseEvent returns the event with the given wire name.
func ParseEvent(name string) (Event, bool) {
	for e, n := range eventNames {
		if n != "" && n == name {
			return Event(e), true
		}
	}
	return 0, false
}

// EnvNotifySocket and EnvNotifySession name the environment variables that
// Options.NotifySocket and Options.NotifySession export to the child. The
// names avoid words such as TOKEN or KEY, which agent CLIs strip from the
// environment of their hooks.
const (
	EnvNotifySocket  = "BUNK_GUI_SOCKET"
	EnvNotifySession = "BUNK_GUI_SESSION"
)

// agentReport is the agent's latest explicit report. Protected by mu.
type agentReport struct {
	state  AgentState // AgentNone: no report, inference decides
	notice string
	pgid   int // foreground process group when the report arrived; 0 if unknown
}

// Notify records a state the agent in the session reported about itself.
// The report takes priority over the states inferred from output, bells and
// OSC notifications. It holds until Acknowledge, an EventClear report, or a
// change of the foreground process group. message is kept as the notice of
// EventNeedsYou, EventDone and EventFailed.
//
// Notify takes the session lock. When Status changed, it then runs
// Hooks.StatusChanged (as passed to Run) once, without the lock, so callers
// must not hold a lock that the hook takes.
func (s *Session) Notify(ev Event, message string) {
	var r agentReport
	switch ev {
	case EventClear:
	case EventWorking:
		r.state = AgentWorking
	case EventIdle:
		r.state = AgentIdle
	case EventNeedsYou:
		r.state, r.notice = AgentNeedsYou, cleanNotice(message, defaultNotice)
	case EventDone:
		r.state, r.notice = AgentDone, cleanNotice(message, "")
	case EventFailed:
		r.state, r.notice = AgentFailed, cleanNotice(message, "")
	default:
		return
	}
	if r.state != AgentNone {
		r.pgid = s.fgPGID()
	}
	s.Lock()
	s.report = r
	changed := s.takeAgentChange(time.Now())
	hook := s.statusHook
	s.Unlock()
	L().Debug("session: agent report", "id", s.ID, "event", ev.String(), "changed", changed)
	if changed && hook != nil {
		hook()
	}
}

// fgPGID returns the terminal's foreground process group, or 0. It reads
// /proc, so call it without the session lock.
func (s *Session) fgPGID() int {
	if s.Cmd == nil || s.Cmd.Process == nil {
		return 0
	}
	return TermFgPGID(s.Cmd.Process.Pid)
}
