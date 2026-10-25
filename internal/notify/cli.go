package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

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

const usage = `usage: bunk-gui notify <event> [message | -]
       bunk-gui notify hooks [agent]

Reports an agent's state to the bunk-gui session it runs in.
Events: working, idle, needs-you, done, failed, clear.
The message is the notice for needs-you, done and failed. "-" reads it
from stdin. A hook's JSON input gives its "message",
"last_assistant_message" or similar field.
Outside bunk-gui (BUNK_GUI_SOCKET or BUNK_GUI_SESSION unset) it does
nothing and exits 0.

"hooks" lists the agents with ready hook snippets; "hooks <agent>"
prints one.
`

// RunCLI runs the notify command; args exclude the "notify" word itself,
// env is the process environment and stdin is read for a "-" message. It
// returns the exit status: 0 on success and whenever bunk-gui is not
// running the session, 1 on errors.
func RunCLI(args []string, env []string, stdin io.Reader) int {
	return runCLI(args, env, stdin, os.Stdout, os.Stderr)
}

func runCLI(args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		write(stderr, usage)
		return exitError
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		write(stdout, usage)
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
		write(stderr, "bunk-gui notify: hooks takes one of:\n"+HookAgents())
		return exitError
	}
	ev, ok := session.ParseEvent(args[0])
	if !ok {
		write(stderr, fmt.Sprintf("bunk-gui notify: unknown event %q\n\n%s", args[0], usage))
		return exitError
	}
	socket, token := lookupEnv(env, EnvSocket), lookupEnv(env, EnvSession)
	if socket == "" || token == "" {
		return 0 // not inside bunk-gui
	}
	text := strings.Join(args[1:], " ")
	if text == "-" {
		text = readStdin(stdin)
	}
	err := Send(socket, Message{Session: token, Event: ev, Text: hookText(text)})
	switch {
	case err == nil:
		return 0
	case serverGone(err):
		// The bunk-gui that started this session has exited, as when an
		// agent outlives it inside tmux.
		return 0
	}
	write(stderr, "bunk-gui notify: "+err.Error()+"\n")
	return exitError
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
// input, its first non-empty hookTextFields value. A JSON object without
// one, or a cut-off one, gives "".
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
	return ""
}
