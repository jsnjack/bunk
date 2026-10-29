package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

const (
	// stdinLimit caps the hook input read for "-"; agents send a JSON
	// object that can hold a whole assistant message.
	stdinLimit = 4 << 20
	// exitError is the only failure status. Agents treat status 2 as
	// "block the action" (Claude Code, Codex, Gemini CLI), so the CLI never
	// returns it.
	exitError = 1
)

// stdinTimeout bounds the wait for hook input, so an agent that keeps the
// pipe open cannot hang its hook. Tests shorten it.
var stdinTimeout = 2 * time.Second

// hookTextFields are the keys whose string value becomes the message when
// the message is a hook's JSON input, in order of preference: Claude Code
// and Copilot CLI notifications, Claude Code and Codex Stop, the Codex
// notify program, Gemini CLI AfterAgent, failures.
var hookTextFields = []string{"message", "last_assistant_message", "last-assistant-message", "prompt_response", "error"}

// usage is the help text; %[1]s is the program name, bunk or bunk-gui.
const usage = `usage: %[1]s notify <event> [message | -]
       %[1]s notify hooks [agent]
       %[1]s notify hooks install <agent>

Reports an agent's state to the bunk session it runs in, in bunk's
terminal UI or in bunk-gui.
Events: working, idle, needs-you, done, failed, clear.
The message is the notice for needs-you, done and failed. "-" reads it
from stdin. A hook's JSON input gives its "message",
"last_assistant_message" or similar field, or the question its tool asks.
A done whose Claude Code Stop input lists background_tasks reports
working: the agent resumes when they end. A watch for comments on a
published artifact does not count. A needs-you for a Codex
PermissionRequest that Codex's automatic reviewer decides reports
working: the user is not asked. A hook's "session_id" (or "sessionId")
goes with the report, so bunk-gui can resume the agent's conversation
when it restores the pane.
Inside bunk it reports over BUNK_NOTIFY_SOCKET. Without a reachable
socket, with BUNK_NOTIFY_SESSION set or on a host reached over ssh
(SSH_CONNECTION set), it writes the report to its terminal for the bunk
pane ssh runs in. Elsewhere it does nothing and exits 0.

"hooks" shows, for each agent with a hook snippet, whether its hooks are
installed, where they go and how. "hooks <agent>" prints the snippet;
"hooks install <agent>" writes it for an agent whose hooks are a file of
their own (codex, copilot). docs/agent-hooks.md describes the setup.
`

// RunCLI runs the notify command; args exclude the "notify" word itself,
// env is the process environment and stdin is read for a "-" message. It
// returns the exit status: 0 on success and whenever no bunk front end is
// running the session, 1 on errors. Messages name the program after
// os.Args[0], so bunk and bunk-gui can both run it.
func RunCLI(args []string, env []string, stdin io.Reader) int {
	prog := filepath.Base(os.Args[0])
	if prog == "." || prog == string(filepath.Separator) {
		prog = "bunk"
	}
	return runCLI(prog, args, env, stdin, os.Stdout, os.Stderr)
}

func runCLI(prog string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	help := fmt.Sprintf(usage, prog)
	if len(args) == 0 {
		write(stderr, help)
		return exitError
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		write(stdout, help)
		return 0
	case "hooks":
		switch {
		case len(args) == 1:
			write(stdout, hookStatus(prog, envDirs(env)))
			return 0
		case len(args) == 2:
			if snippet, ok := HookSnippet(args[1]); ok {
				write(stdout, snippet)
				return 0
			}
		case len(args) == 3 && args[1] == "install":
			return installHooks(prog, args[2], envDirs(env), stdout, stderr)
		}
		write(stderr, prog+" notify: hooks takes one of:\n"+HookAgents(prog))
		return exitError
	}
	ev, ok := session.ParseEvent(args[0])
	if !ok {
		write(stderr, fmt.Sprintf("%s notify: unknown event %q\n\n%s", prog, args[0], help))
		return exitError
	}
	socket, token := lookupEnv(env, EnvSocket), lookupEnv(env, EnvSession)
	if token == "" && lookupEnv(env, envSSH) == "" {
		return 0 // not inside bunk, and not on a host reached over ssh
	}
	text := strings.Join(args[1:], " ")
	if text == "-" {
		text = readStdin(stdin)
	}
	if ev == session.EventDone && backgroundTasks(text) {
		// Claude Code ended its turn while work it started runs on: it
		// resumes when that work ends, and its next Stop reports done.
		ev = session.EventWorking
	}
	if ev == session.EventNeedsYou && codexAutoReview(text) {
		// Codex's automatic reviewer decides the request without asking
		// the user, and no hook reports work again until the turn ends.
		ev = session.EventWorking
	}
	agentSession := hookSessionID(text)
	held := ev == session.EventNeedsYou && questionTool(text)
	text = hookText(text)
	if socket != "" && token != "" {
		err := Send(socket, Message{Session: token, Event: ev, Text: text, AgentSession: agentSession, Held: held})
		switch {
		case err == nil:
			return 0
		case !serverGone(err):
			write(stderr, prog+" notify: "+err.Error()+"\n")
			return exitError
		}
		// The front end that started this session has exited, as when an
		// agent outlives it inside tmux. The terminal may still lead to bunk.
	}
	return reportInBand(prog, wire.Report{Session: token, Event: ev.String(), Message: text, AgentSession: agentSession, Held: held}, env, stderr)
}

func write(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		slog.Debug("notify: write", "err", err)
	}
}

// lookupEnv returns the last value of key in env, as exec does.
func lookupEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// readStdin returns up to stdinLimit bytes of stdin, or "" when stdin
// stays open for stdinTimeout.
func readStdin(stdin io.Reader) string {
	if stdin == nil {
		return ""
	}
	got := make(chan string, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(stdin, stdinLimit))
		if err != nil {
			slog.Debug("notify: read stdin", "err", err)
		}
		got <- string(b)
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(stdinTimeout):
		return ""
	}
}

// hookText returns text, or, when text is a JSON object such as a hook's
// input, its first non-empty hookTextFields value, else the approval a
// PermissionRequest asks for (approvalNotice) or what the tool it names
// asks the user (toolQuestion). A JSON object without any, or a cut-off
// one, gives "".
func hookText(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return text
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return ""
	}
	for _, key := range hookTextFields {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
			return s
		}
		// An error object: {"message": "…", …}.
		var nested struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &nested); err == nil && strings.TrimSpace(nested.Message) != "" {
			return nested.Message
		}
	}
	if notice := approvalNotice(obj); notice != "" {
		return notice
	}
	return toolQuestion(obj)
}

// hookSessionIDFields are the keys of a hook's JSON input that hold the
// agent's session ID: Claude Code, Codex and Gemini CLI, then Copilot CLI.
var hookSessionIDFields = []string{"session_id", "sessionId"}

// hookSessionID returns the agent's session ID from text, a hook's JSON
// input, or "" when it has none that wire.ValidAgentSession accepts.
func hookSessionID(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &obj) != nil {
		return ""
	}
	for _, key := range hookSessionIDFields {
		var id string
		if json.Unmarshal(obj[key], &id) == nil && wire.ValidAgentSession(id) {
			return id
		}
	}
	return ""
}

// questionTool reports whether text is the PreToolUse input of a tool
// that waits for the user's answer (AskUserQuestion, ExitPlanMode), whose
// PostToolUse hook reports when the answer came.
func questionTool(text string) bool {
	var in struct {
		Event string `json:"hook_event_name"`
		Tool  string `json:"tool_name"`
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &in) != nil {
		return false
	}
	return (in.Event == "" || in.Event == "PreToolUse") && (in.Tool == askUserQuestion || in.Tool == exitPlanMode)
}

// backgroundTasks reports whether text is a Claude Code Stop input whose
// background_tasks lists work still in flight: shells, subagents,
// monitors. Its session_crons do not count: a scheduled wakeup, such as
// the next round of /loop, may be far off. Neither does an artifact's
// comment watch, which runs from a publish until the session ends.
func backgroundTasks(text string) bool {
	var in struct {
		Tasks []struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"background_tasks"`
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &in) != nil {
		return false
	}
	for _, task := range in.Tasks {
		if !watchesArtifact(task.Type, task.Description) {
			return true
		}
	}
	return false
}

// watchesArtifact reports whether a background task is the watch Claude
// Code starts for comments on an artifact it published. Its SDK events
// mark such a task as not activity, but the Stop input carries only the
// description, "live updates for artifact <URL> (watch requested)".
func watchesArtifact(typ, description string) bool {
	return typ == "monitor" && strings.HasPrefix(description, "live updates for artifact ")
}

// planNotice starts the notice of a plan that waits for approval.
const planNotice = "Approve the plan"

// toolQuestion returns what a tool that waits for the user asks, from a
// Claude Code PreToolUse input: the first question of AskUserQuestion, or
// for ExitPlanMode planNotice and the plan's title, its first line without
// heading marks. Other inputs give "".
func toolQuestion(obj map[string]json.RawMessage) string {
	var tool string
	if err := json.Unmarshal(obj["tool_name"], &tool); err != nil {
		return ""
	}
	var in struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(obj["tool_input"], &in); err != nil {
		return ""
	}
	switch tool {
	case askUserQuestion:
		for _, q := range in.Questions {
			if strings.TrimSpace(q.Question) != "" {
				return q.Question
			}
		}
	case exitPlanMode:
		for line := range strings.Lines(in.Plan) {
			title := strings.TrimSpace(strings.TrimLeft(line, "# \t"))
			title = strings.TrimSpace(strings.TrimPrefix(title, "Plan:"))
			if title != "" {
				return planNotice + ": " + title
			}
		}
		return planNotice
	}
	return ""
}

// envDirs locates the agents' configuration from the environment env.
func envDirs(env []string) Dirs {
	home := lookupEnv(env, "HOME")
	if home == "" {
		home = UserDirs().Home
	}
	return Dirs{Home: home, Getenv: func(k string) string { return lookupEnv(env, k) }}
}

// hookStatus lists each agent's hook setup: its state and file, and the
// step that sets it up when it is not.
func hookStatus(prog string, d Dirs) string {
	var b strings.Builder
	for _, h := range CheckHooks(d) {
		r, _ := recipe(h.Agent)
		line := fmt.Sprintf("%-8s %-10s %s", h.Agent, h.State, d.Short(h.Path))
		if !h.CLI {
			line += fmt.Sprintf(" (%s is not on PATH)", h.Agent)
		}
		b.WriteString(line + "\n")
		if h.State == HooksOutdated {
			fmt.Fprintf(&b, "%-19s lacks the %s hooks of the current snippet\n", "", strings.Join(h.Missing, ", "))
		}
		if h.Problem != "" {
			fmt.Fprintf(&b, "%-19s %s\n", "", h.Problem)
		}
		if h.State != HooksInstalled {
			fmt.Fprintf(&b, "%-19s %s\n", "", r.step(prog))
		}
	}
	fmt.Fprintf(&b, "\n%s notify hooks <agent> prints a snippet; docs/agent-hooks.md describes the setup.\n", prog)
	return b.String()
}

// installHooks runs "hooks install <agent>".
func installHooks(prog, agent string, d Dirs, stdout, stderr io.Writer) int {
	r, ok := recipe(agent)
	if !ok {
		write(stderr, prog+" notify: hooks install takes one of:\n"+HookAgents(prog))
		return exitError
	}
	done, err := InstallHooks(d, agent)
	if err != nil {
		write(stderr, fmt.Sprintf("%s notify: %v\n", prog, err))
		return exitError
	}
	msg := "Installed " + d.Short(done.Path) + "."
	if done.Backup != "" {
		msg += " The old file is in " + d.Short(done.Backup) + "."
	}
	if r.after != "" {
		msg += " " + r.after
	}
	write(stdout, msg+"\n")
	return 0
}
