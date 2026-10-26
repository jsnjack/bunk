// status.go - what a session's agent is doing, for tab labels and switchers.
package session

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"bunk/internal/notify/wire"
)

// AgentState is what a session's agent appears to be doing.
type AgentState uint8

// The agent states.
const (
	AgentNone     AgentState = iota // no known agent runs in the foreground
	AgentWorking                    // output arrived within IdleAfter
	AgentIdle                       // no output for IdleAfter
	AgentNeedsYou                   // the agent asked for attention; not yet acknowledged
	AgentFailed                     // the agent's command failed
	AgentDone                       // the agent reported a finished turn; not yet acknowledged
)

// IdleAfter is how long an agent counts as working after its last output
// (default of Timing.Idle).
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
	// Host is the machine a remote agent runs on, from its in-band report;
	// "" for local agents. With a remote agent, Agent, Cwd, Repo and Branch
	// come from its report too.
	Host string
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
	if r := v.remote; r != (remoteAgent{}) {
		st.Host, st.Cwd, st.Repo, st.Branch = r.host, r.cwd, r.repo, r.branch
	}
	if s.ContainerType != "" {
		st.Container = strings.TrimSpace(s.ContainerType + " " + s.ContainerID)
	}
	return st
}

// Acknowledge records that the user has seen the session. It clears a
// pending attention request and the agent's explicit report, so inference
// shows the session until the agent reports again. It keeps what a remote
// agent's report said about where it runs. It runs no hook itself:
// the next output or process tick (at most Timing.Poll later) announces the
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
	remote   remoteAgent
}

// remoteAgent is where an agent that reports in-band runs, from its latest
// report. The zero value means a local agent.
type remoteAgent struct {
	agent, host, repo, branch, cwd string
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
// inference. A remote agent named by its report counts as known, so
// inference keeps naming it after an acknowledgement.
func (s *Session) view(now time.Time) agentView {
	agent := s.agent
	if agent == "" {
		agent = s.remote.agent
	}
	if s.report.state != AgentNone {
		if agent == "" {
			agent = reportingAgent
		}
		return agentView{agent: agent, state: s.report.state, notice: s.report.notice, reported: true, remote: s.remote}
	}
	if agent == "" && s.remoteClient && s.needsYou {
		// An OSC 9/99/777 notification from the other end of ssh or mosh.
		agent = reportingAgent
	}
	return agentView{agent: agent, state: s.agentState(now, agent), notice: s.notice, remote: s.remote}
}

// agentState requires the session lock; agent is the agent view names.
func (s *Session) agentState(now time.Time, agent string) AgentState {
	switch {
	case agent == "":
		return AgentNone
	case s.failed:
		return AgentFailed
	case s.needsYou:
		return AgentNeedsYou
	case now.Sub(s.lastOutput) < s.timing.idle():
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

// AgentOf names the agent in DefaultAgents that runs as process name with
// argv, or returns "". The notify CLI uses it to name the agent that ran a
// hook.
func AgentOf(name string, argv []string) string {
	return matchAgent(DefaultAgents, name, argv)
}

// agentFromProcess names the agent running as process name with argv, from
// the session's specs (DefaultAgents plus Options.Agents), or returns "".
func (s *Session) agentFromProcess(name string, argv []string) string {
	if a := matchAgent(s.agents, name, argv); a != "" {
		return a
	}
	return matchAgent(DefaultAgents, name, argv)
}

// GitHead finds the git work tree holding dir and returns its repository
// name and checked-out branch, or a short commit hash when HEAD is detached.
// A linked worktree reports its main repository.
func GitHead(dir string) (repo, branch string) {
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
			n, err := strconv.Atoi(code)
			if err != nil && code != "" {
				L().Log(context.Background(), LevelTrace, "OSC 133;D: exit code is not a number", "code", code, "err", err)
			}
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
// returns, which can be after the next process poll (default of
// Timing.RecentlyLeft).
const recentlyLeft = 2 * time.Second

// onStatusOSC is the OSC scanner's status callback. It runs on the reader
// goroutine, without the session lock. ReadLoop announces the change after
// the chunk. An attention request counts from a known agent, and from any
// program while ssh or mosh runs in the foreground: the agent on the other
// end is out of sight.
func (s *Session) onStatusOSC(num int, payload []byte) {
	if num == inBandOSC {
		s.onInBand(payload)
		return
	}
	st, ok := parseStatusOSC(num, payload)
	if !ok {
		return
	}
	s.Lock()
	defer s.Unlock()
	switch {
	case st.notice != "" && (s.agent != "" && !s.agentGone || s.remoteClient):
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
		case s.agent == "" && s.lastAgent != "" && time.Since(s.agentLeft) < s.timing.recentlyLeft():
			// The agent already left; show its failure until the next command.
			s.agent, s.failed, s.agentGone = s.lastAgent, true, true
		}
	}
}

// onBell is vt10x's bell callback; it runs inside Write, with the session
// lock held. A bell from an agent counts as an attention request. A bell
// from the other end of ssh or mosh does not: a remote shell rings it on a
// failed tab completion.
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
	repo, branch := GitHead(cwd)
	remote := isRemoteClient(name)

	s.Lock()
	defer s.Unlock()
	if s.remoteClient && (pgidChanged || !remote) && s.agent == "" {
		// A remote attention request ends with its connection.
		s.needsYou, s.notice = false, ""
	}
	s.remoteClient = remote
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
	if pgidChanged && s.remote != (remoteAgent{}) && (pgid <= 0 || pgid != s.remotePGID) {
		// The ssh or bunk that carried the remote agent's reports has left.
		s.remote, s.remotePGID = remoteAgent{}, 0
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

// The events an agent can report.
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
// Options.NotifySocket and Options.NotifySession export to the child. Every
// front end (bunk's TUI, bunk-gui) uses the same names. The names avoid
// words such as TOKEN or KEY, which agent CLIs strip from the environment
// of their hooks.
const (
	EnvNotifySocket  = "BUNK_NOTIFY_SOCKET"
	EnvNotifySession = "BUNK_NOTIFY_SESSION"
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
	if _, ok := ParseEvent(ev.String()); !ok {
		return
	}
	pgid := 0
	if ev != EventClear {
		pgid = s.fgPGID()
	}
	s.Lock()
	s.record(Report{Event: ev, Message: message}, pgid)
	changed := s.takeAgentChange(time.Now())
	hook := s.statusHook
	s.Unlock()
	L().Debug("session: agent report", "id", s.ID, "event", ev.String(), "changed", changed)
	if changed && hook != nil {
		hook()
	}
}

// Report is what an agent reports about itself: an event, its message, and
// for an agent on another machine, where it runs.
type Report struct {
	Event   Event
	Message string
	// Agent, Host, Repo, Branch and Cwd describe a remote agent; all are ""
	// in local reports.
	Agent, Host, Repo, Branch, Cwd string
}

// record applies r, which arrived while process group pgid was in the
// foreground. EventClear also forgets where a remote agent runs, and an
// attention request inferred from the other end of ssh, such as the OSC
// 777 alert a remote bunk sends beside its report. Other events keep the
// remote agent's place unless r names a new one. It requires the session
// lock.
func (s *Session) record(r Report, pgid int) {
	var rep agentReport
	switch r.Event {
	case EventClear:
		s.report = agentReport{}
		s.remote, s.remotePGID = remoteAgent{}, 0
		if s.remoteClient && s.agent == "" {
			s.needsYou, s.notice = false, ""
		}
		return
	case EventWorking:
		rep.state = AgentWorking
	case EventIdle:
		rep.state = AgentIdle
	case EventNeedsYou:
		rep.state, rep.notice = AgentNeedsYou, cleanNotice(r.Message, defaultNotice)
	case EventDone:
		rep.state, rep.notice = AgentDone, cleanNotice(r.Message, "")
	case EventFailed:
		rep.state, rep.notice = AgentFailed, cleanNotice(r.Message, "")
	default:
		return
	}
	rep.pgid = pgid
	s.report = rep
	where := remoteAgent{
		agent: cleanNotice(r.Agent, ""), host: cleanNotice(r.Host, ""), repo: cleanNotice(r.Repo, ""),
		branch: cleanNotice(r.Branch, ""), cwd: cleanNotice(r.Cwd, ""),
	}
	if where != (remoteAgent{}) {
		s.remote, s.remotePGID = where, pgid
	}
}

// inBandOSC carries in-band reports: OSC 1337 SetUserVar=bunk_notify=….
const inBandOSC = 1337

// parseInBand reads an in-band report from an OSC 1337 payload without its
// terminator. It returns the report's token, "" when it has none.
func parseInBand(payload []byte) (token string, r Report, ok bool) {
	line, ok := wire.FromUserVar(payload)
	if !ok {
		return "", Report{}, false
	}
	w, err := wire.Decode(line)
	if err != nil {
		L().Debug("session: malformed in-band report", "err", err)
		return "", Report{}, false
	}
	ev, ok := ParseEvent(w.Event)
	if !ok {
		return "", Report{}, false
	}
	return w.Session, Report{Event: ev, Message: w.Message, Agent: w.Agent, Host: w.Host, Repo: w.Repo,
		Branch: w.Branch, Cwd: w.Cwd}, true
}

// onInBand records a report that arrived in the session's output
// (docs/remote-agent-status.md). It accepts a report whose token
// Options.KnownToken knows, in any session, and any other report while ssh
// or mosh runs in the foreground. It runs on the reader goroutine, without
// the session lock; ReadLoop announces the change after the chunk.
func (s *Session) onInBand(payload []byte) {
	token, r, ok := parseInBand(payload)
	if !ok {
		return
	}
	known := token != "" && s.knownToken != nil && s.knownToken(token)
	pgid := 0
	if r.Event != EventClear {
		pgid = s.fgPGID()
	}
	s.Lock()
	defer s.Unlock()
	if !known && !s.remoteClient {
		L().Debug("session: dropped an in-band report", "id", s.ID, "event", r.Event.String(), "token", token != "")
		return
	}
	L().Debug("session: in-band report", "id", s.ID, "event", r.Event.String(), "known_token", known, "host", r.Host)
	s.record(r, pgid)
}

// remoteClients are the foreground processes whose output comes from
// another machine.
var remoteClients = map[string]bool{"ssh": true, "mosh": true, "mosh-client": true}

func isRemoteClient(name string) bool { return remoteClients[name] }

// fgPGID returns the terminal's foreground process group, or 0. It reads
// /proc, so call it without the session lock.
func (s *Session) fgPGID() int {
	if s.Cmd == nil || s.Cmd.Process == nil {
		return 0
	}
	return TermFgPGID(s.Cmd.Process.Pid)
}
