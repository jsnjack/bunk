package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Command is how hook snippets run the notify CLI: bunk-gui's when bunk-gui
// is on PATH, else bunk's, and nothing when neither is installed, as on a
// server without bunk. Both binaries run the same CLI (RunCLI) and speak
// the same protocol to the socket BUNK_NOTIFY_SOCKET names, so either
// reaches the front end of the session. bunk-gui comes first because a
// stale bunk on PATH may predate the notify command; over ssh usually only
// bunk is installed.
const Command = `! b=$(command -v bunk-gui || command -v bunk) || "$b" notify`

// hookTimeout is the timeout snippets give each hook, in seconds. The CLI
// gives up after clientTimeout plus stdinTimeout.
const hookTimeout = 5

// hook maps one of an agent's hook events to a notify command.
type hook struct {
	event   string // the agent's event name
	matcher string // "" matches every occurrence
	args    string // notify arguments, such as "needs-you -"
	timeout int    // seconds; 0 is hookTimeout
}

func (h hook) seconds() int {
	if h.timeout > 0 {
		return h.timeout
	}
	return hookTimeout
}

// agentHooks is one agent's recipe for "notify hooks <agent>".
type agentHooks struct {
	name    string // also the agent's command
	homeDir string // the agent's configuration directory under the home directory
	envDir  string // the variable that moves it, or ""
	file    string // where the snippet goes, in that directory
	// own: the snippet is a file of its own, so InstallHooks may write it.
	// Otherwise it is merged into a file with other settings.
	own    bool
	how    string // the setup step after the file, with %[1]s for the program
	after  string // what to do once InstallHooks wrote the file, or ""
	hooks  []hook
	render func([]hook) (string, error)
	read   func(dir string) ([]foundHook, error) // the hooks the agent has now
}

// dest names the snippet's file and how it gets there, for listings.
func (r agentHooks) dest(prog string) string {
	return "~/" + r.homeDir + "/" + r.file + " (" + fmt.Sprintf(r.how, prog) + ")"
}

// step is the setup step of a snippet that is not installed.
func (r agentHooks) step(prog string) string {
	if r.own {
		return fmt.Sprintf(r.how, prog)
	}
	return fmt.Sprintf(`run "%[1]s notify hooks install %[2]s", which merges the hooks into it and keeps a copy, or merge the "hooks" key of "%[1]s notify hooks %[2]s" by hand`, prog, r.name)
}

// mergeKey is the setup step of a snippet merged into a settings file.
const mergeKey = `merge the "hooks" key`

// claudeQuestionTools are Claude Code's tools that wait for the user: a
// multiple-choice question and the approval of a plan. No notification
// type of Claude Code names them, so PreToolUse, which runs before the
// prompt shows, reports them, and PostToolUse, which runs once the user
// has answered, reports work again.
const claudeQuestionTools = askUserQuestion + "|" + exitPlanMode

// The tool names of Claude Code's question and plan approval.
const (
	askUserQuestion = "AskUserQuestion"
	exitPlanMode    = "ExitPlanMode"
)

// recipes are the agents "notify hooks" knows, in listing order.
var recipes = []agentHooks{
	{
		name: "claude", homeDir: ".claude", envDir: "CLAUDE_CONFIG_DIR", file: "settings.json", how: mergeKey + " or run %[1]s notify hooks install claude",
		read: readSettingsHooks("settings.json"),
		hooks: []hook{
			{event: "UserPromptSubmit", args: "working -"},
			{event: "PreToolUse", matcher: claudeQuestionTools, args: "needs-you -"},
			{event: "PostToolUse", matcher: claudeQuestionTools, args: "working"},
			{event: "Notification", matcher: "permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input", args: "needs-you -"},
			{event: "Stop", args: "done -"},
			{event: "StopFailure", args: "failed -"},
			{event: "SessionEnd", args: "clear"},
		},
		render: renderClaude,
	},
	{
		name: "codex", homeDir: ".codex", envDir: "CODEX_HOME", file: "hooks.json", own: true,
		how:   "%[1]s notify hooks install codex, then trust the hooks with /hooks in Codex",
		after: "Run /hooks in Codex once to trust the hooks.",
		read:  readCodexHooks,
		hooks: []hook{
			{event: "UserPromptSubmit", args: "working -"},
			// The CLI reads the input: a request that the automatic
			// reviewer decides (approvals_reviewer) reports working.
			{event: "PermissionRequest", args: "needs-you -"},
			{event: "Stop", args: "done -"},
			// Codex allows Interrupt and SessionEnd hooks 3 s at most.
			{event: "Interrupt", args: "idle", timeout: 3},
			{event: "SessionEnd", args: "clear", timeout: 3},
		},
		render: renderClaude, // hooks.json has the form of Claude Code's settings
	},
	{
		name: "copilot", homeDir: ".copilot", envDir: "COPILOT_HOME", file: "hooks/bunk.json", own: true,
		how:  "%[1]s notify hooks install copilot",
		read: readCopilotHooks,
		hooks: []hook{
			{event: "userPromptSubmitted", args: "working -"},
			{event: "notification", matcher: "permission_prompt|elicitation_dialog", args: "needs-you -"},
			{event: "agentStop", args: "done -"},
			{event: "sessionEnd", args: "clear"},
		},
		render: renderCopilot,
	},
	{
		name: "gemini", homeDir: ".gemini", file: "settings.json", how: mergeKey + " or run %[1]s notify hooks install gemini",
		read: readSettingsHooks("settings.json"),
		hooks: []hook{
			{event: "BeforeAgent", args: "working -"},
			{event: "Notification", args: "needs-you -"},
			{event: "AfterAgent", args: "done -"},
			{event: "SessionEnd", args: "clear"},
		},
		render: renderGemini,
	},
}

// HookSnippet returns the hook configuration for agent ("claude", "codex",
// "copilot" or "gemini"), ready to merge into the file HookAgents names.
func HookSnippet(agent string) (string, bool) {
	for _, r := range recipes {
		if r.name == agent {
			s, err := r.render(r.hooks)
			if err != nil {
				return "", false
			}
			return s, true
		}
	}
	return "", false
}

// HookAgents lists the agents HookSnippet knows, one "name  destination"
// line each; prog names the program in setup steps.
func HookAgents(prog string) string {
	var b strings.Builder
	for _, r := range recipes {
		fmt.Fprintf(&b, "%-8s %s\n", r.name, r.dest(prog))
	}
	return b.String()
}

// shellCommand runs the notify CLI inside a bunk session and on hosts
// reached over ssh, where it reports in-band to the bunk pane ssh runs in.
// Elsewhere the hook succeeds without running anything.
func shellCommand(args string) string {
	return `[ -z "$` + EnvSession + `$` + envSSH + `" ] || ` + Command + " " + args
}

// field is one key of an ordered JSON object.
type field struct {
	key   string
	value any
}

// object is a JSON object that keeps its key order.
type object []field

// MarshalJSON writes the fields in order.
func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(f.key)
		if err != nil {
			return nil, fmt.Errorf("marshal key: %w", err)
		}
		v, err := json.Marshal(f.value)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", f.key, err)
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func indentJSON(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render hooks: %w", err)
	}
	return string(out) + "\n", nil
}

// renderClaude writes Claude Code's settings.json form:
// event → matcher groups → command handlers.
func renderClaude(hs []hook) (string, error) {
	events := object{}
	for _, h := range hs {
		group := object{}
		if h.matcher != "" {
			group = append(group, field{"matcher", h.matcher})
		}
		handler := object{{"type", "command"}, {"command", shellCommand(h.args)}, {"timeout", h.seconds()}}
		group = append(group, field{"hooks", []any{handler}})
		events = append(events, field{h.event, []any{group}})
	}
	return indentJSON(object{{"hooks", events}})
}

// renderGemini writes Gemini CLI's settings.json form, with named handlers
// and timeouts in milliseconds.
func renderGemini(hs []hook) (string, error) {
	events := object{}
	for _, h := range hs {
		name := "bunk-" + strings.Fields(h.args)[0]
		handler := object{{"name", name}, {"type", "command"}, {"command", shellCommand(h.args)}, {"timeout", h.seconds() * 1000}}
		group := object{}
		if h.matcher != "" {
			group = append(group, field{"matcher", h.matcher})
		}
		group = append(group, field{"hooks", []any{handler}})
		events = append(events, field{h.event, []any{group}})
	}
	return indentJSON(object{{"hooks", events}})
}

// renderCopilot writes a Copilot CLI hooks file: version 1, with one flat
// handler list per event.
func renderCopilot(hs []hook) (string, error) {
	events := object{}
	for _, h := range hs {
		handler := object{{"type", "command"}}
		if h.matcher != "" {
			handler = append(handler, field{"matcher", h.matcher})
		}
		handler = append(handler, field{"bash", shellCommand(h.args)}, field{"timeoutSec", h.seconds()})
		events = append(events, field{h.event, []any{handler}})
	}
	return indentJSON(object{{"version", 1}, {"hooks", events}})
}

// codexNotifyLine is the notify program for the config.toml of Codex
// releases without hooks, a top-level key. Codex appends its JSON payload
// to the program's arguments, so sh passes it on as "$1".
func codexNotifyLine() string {
	return fmt.Sprintf("notify = [\"sh\", \"-c\", %s, \"sh\"]\n", strconv.Quote(shellCommand(`done "$1"`)))
}
