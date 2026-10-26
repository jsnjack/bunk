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

Reports an agent's state to the bunk session it runs in, in bunk's
terminal UI or in bunk-gui.
Events: working, idle, needs-you, done, failed, clear.
The message is the notice for needs-you, done and failed. "-" reads it
from stdin. A hook's JSON input gives its "message",
"last_assistant_message" or similar field, or the question its tool asks.
Inside bunk it reports over BUNK_NOTIFY_SOCKET. Without a reachable
socket, with BUNK_NOTIFY_SESSION set or on a host reached over ssh
(SSH_CONNECTION set), it writes the report to its terminal for the bunk
pane ssh runs in. Elsewhere it does nothing and exits 0.

"hooks" lists the agents with ready hook snippets; "hooks <agent>"
prints one.
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
		switch len(args) {
		case 1:
			write(stdout, HookAgents())
			return 0
		case 2:
			if snippet, ok := HookSnippet(args[1]); ok {
				write(stdout, snippet)
				return 0
			}
		}
		write(stderr, prog+" notify: hooks takes one of:\n"+HookAgents())
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
	text = hookText(text)
	if socket != "" && token != "" {
		err := Send(socket, Message{Session: token, Event: ev, Text: text})
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
	return reportInBand(prog, wire.Report{Session: token, Event: ev.String(), Message: text}, env, stderr)
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
// input, its first non-empty hookTextFields value, else what the tool it
// names asks the user (toolQuestion). A JSON object without either, or a
// cut-off one, gives "".
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
	return toolQuestion(obj)
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
